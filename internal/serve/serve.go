package serve

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Hedronite/cubit/internal/collect"
	"github.com/Hedronite/cubit/internal/scan"
	"github.com/Hedronite/cubit/internal/textfile"
	"github.com/Hedronite/cubit/internal/version"
)

// Sink receives the exposition after every scan, for the textfile.
type Sink interface{ Write([]byte) error }

// Options is everything Run needs.
type Options struct {
	Spec       scan.Spec
	Env        scan.Env
	Interval   time.Duration
	Addr       string
	Unsafe     bool
	ConfigName string // the config file's base name; a directory would be a path
	Log        *slog.Logger
	Info       version.Info
	Textfile   Sink // nil: no textfile

	// Wait returns how long to sleep before the next scan. Nil means the
	// interval plus or minus 10%, uniformly.
	Wait func(interval time.Duration) time.Duration
	// Listen opens the listener. Nil means net.Listen on a TCP address. For tests.
	Listen func(addr string) (net.Listener, error)
	// OnListen is called once the listener is open, with its address. For tests.
	OnListen func(addr string)
	// OnScan is called after each scan has been folded into the collector. For tests.
	OnScan func(*scan.Document)
}

// Jitter is the default wait: interval plus or minus 10%.
func Jitter(interval time.Duration) time.Duration {
	f := 0.9 + rand.Float64()*0.2 //nolint:gosec // jitter, not a secret
	return time.Duration(float64(interval) * f)
}

// Run serves until ctx ends, then shuts the server down and returns nil. A bind
// the operator did not opt into is a *RefusedError, a failed listen a
// *ListenError. The listener opens before the first scan so a bad bind fails
// at once.
func Run(ctx context.Context, o Options) error {
	if err := CheckBind(ctx, o.Addr, o.Unsafe); err != nil {
		return err
	}
	open := o.Listen
	if open == nil {
		open = listen
	}
	ln, err := open(o.Addr)
	if err != nil {
		return err
	}
	// The scan loop ends when ctx does. Cancel it on every way out, so a failed
	// server never leaves Run waiting for a loop nothing will stop.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Env.Now == nil {
		o.Env.Now = time.Now
	}
	if o.Wait == nil {
		o.Wait = Jitter
	}
	bound := ln.Addr().String()
	if o.Unsafe && !isLoopback(ln.Addr()) {
		o.Log.Warn("listening on a non-loopback address; the metrics endpoint is unauthenticated",
			"addr", bound, "hint", "anyone who can reach this port can read workspace names and sizes: keep this address private (firewall or private network), or restart with --listen 127.0.0.1:9101")
	}
	o.Log.Info("listening", "addr", bound, "config", o.ConfigName, "roots", len(o.Spec.Roots), "interval", o.Interval.String())
	if o.OnListen != nil {
		o.OnListen(bound)
	}

	aliases := make([]string, len(o.Spec.Roots))
	for i, r := range o.Spec.Roots {
		aliases[i] = r.Alias
	}
	col := collect.New(o.Info, aliases)

	srv := &http.Server{
		Handler:           handler(col),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		loop(ctx, o, col)
	}()

	select {
	case err := <-serveErr:
		cancel()
		<-scanDone
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdown, stopShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopShutdown()
	err = srv.Shutdown(shutdown)
	<-scanDone
	o.Log.Info("stopped")
	return err
}

func isLoopback(a net.Addr) bool {
	if t, ok := a.(*net.TCPAddr); ok {
		return t.IP.IsLoopback()
	}
	return false
}

// handler serves /metrics and /healthz. It touches only the collector's
// published snapshot, so a scrape never walks and never waits on a scan.
func handler(col *collect.Collector) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ctype string
		var body func() []byte
		switch r.URL.Path {
		case "/metrics":
			ctype, body = "text/plain; version=0.0.4; charset=utf-8", col.Metrics
		case "/healthz":
			ctype, body = "text/plain; charset=utf-8", func() []byte { return []byte("ok\n") }
		default:
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		b := body()
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(b)
		}
	})
}

// loop runs scans one at a time: the first at once, then after each wait.
func loop(ctx context.Context, o Options, col *collect.Collector) {
	var tf textfileState
	for {
		start := o.Env.Now()
		doc, err := scan.Scan(ctx, o.Spec, o.Env)
		if err != nil {
			return // the context ended; a canceled scan publishes nothing
		}
		if err := col.Update(doc, o.Env.Now()); err != nil {
			o.Log.Error("the scan could not be rendered; /metrics keeps the previous snapshot", "err", err.Error())
		}
		took := o.Env.Now().Sub(start) // before the textfile write: a slow disk is not scan time
		writeTextfile(o, col, &tf)
		logScan(o, doc, took)
		if o.OnScan != nil {
			o.OnScan(doc)
		}
		t := time.NewTimer(o.Wait(o.Interval))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// logScan writes the scan's lines: counts at Info, the roots that need
// attention at Warn, skip counts at Debug. Aliases, reasons and counts only.
func logScan(o Options, doc *scan.Document, took time.Duration) {
	var ok, partial, failed int
	for i := range doc.Roots {
		r := &doc.Roots[i]
		switch {
		case !r.Success:
			failed++
			reasonText := ""
			if len(r.Errors) > 0 {
				reasonText = r.Errors[0]
			}
			o.Log.Warn("root scan failed; keeping last good values", "alias", r.Alias, "reason", reasonText,
				"hint", "check the path for alias "+r.Alias+" in "+o.ConfigName+"; /metrics serves the last good values until a scan succeeds")
		case r.Partial:
			partial++
			o.Log.Warn("root scan partial; sizes are lower bounds", "alias", r.Alias, "reasons", strings.Join(r.Errors, ","),
				"hint", "if this fires on every scan, raise the matching cap in "+o.ConfigName+" (max_duration for deadline_exceeded, max_files for max_files_exceeded); subtree_unreadable needs the unreadable directory fixed")
		default:
			ok++
		}
		if len(r.Skipped) > 0 {
			attrs := []any{"alias", r.Alias}
			for _, s := range r.Skipped {
				attrs = append(attrs, s.Reason, s.Count)
			}
			o.Log.Debug("skip counts", attrs...)
		}
	}
	result := collect.ResultOK
	switch doc.ExitCode() {
	case 1:
		result = collect.ResultPartial
	case 2:
		result = collect.ResultFailed
	}
	o.Log.Info("scan finished", "duration", took.Round(time.Millisecond).String(), "result", result,
		"roots_ok", ok, "roots_partial", partial, "roots_failed", failed)
}

// textfileState remembers what the last write said, so the log reports changes
// and not every interval forever.
type textfileState struct {
	failed bool
	moved  bool
}

// mover is an optional capability of a Sink: it can tell that its directory is
// no longer the one its path names.
type mover interface{ Moved() bool }

// writeTextfile rewrites cubit.prom with the bytes /metrics serves. A failure
// is logged on the first failure and again on recovery, not on every scan, and
// the process goes on serving: /metrics does not depend on the file, and the
// next scan writes it again from scratch. The line holds the errno and a
// constant file name, never the directory.
func writeTextfile(o Options, col *collect.Collector, st *textfileState) {
	if o.Textfile == nil {
		return
	}
	if m, ok := o.Textfile.(mover); ok {
		if now := m.Moved(); now && !st.moved {
			o.Log.Warn("the textfile directory was replaced or moved; cubit.prom is still written to the original directory",
				"file", textfile.FileName, "hint", "node_exporter reads the path, not the old directory: restart cubit to follow the new one")
			st.moved = now
		} else if !now {
			st.moved = false
		}
	}
	body := col.Metrics()
	if err := o.Textfile.Write(body); err != nil {
		if !st.failed {
			errno := textfile.Errno(err)
			if errno == "" {
				errno = "unknown"
			}
			o.Log.Error("textfile write failed", "file", textfile.FileName, "errno", errno,
				"hint", "check the directory (disk full, permissions); /metrics is unaffected and the next scan retries")
		}
		st.failed = true
		return
	}
	if st.failed {
		o.Log.Info("textfile write recovered", "file", textfile.FileName)
		st.failed = false
	}
	o.Log.Debug("textfile written", "file", textfile.FileName, "bytes", len(body))
}
