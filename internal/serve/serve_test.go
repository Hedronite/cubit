package serve

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hedronite/cubit/internal/config"
	"github.com/Hedronite/cubit/internal/contain"
	"github.com/Hedronite/cubit/internal/scan"
	"github.com/Hedronite/cubit/internal/version"
)

func TestCheckBind(t *testing.T) {
	old := resolver
	t.Cleanup(func() { resolver = old })
	resolver = func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "localhost":
			return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, nil
		}
		return nil, errors.New("no such host")
	}
	ctx := context.Background()
	ok := []string{"127.0.0.1:9101", "[::1]:9101", "localhost:9101", "127.0.0.1:0", "127.5.5.5:80"}
	for _, a := range ok {
		if err := CheckBind(ctx, a, false); err != nil {
			t.Errorf("%s refused: %v", a, err)
		}
	}
	refused := map[string]string{
		"0.0.0.0:9101":    "is not a loopback address",
		":9101":           "has no host and would bind every interface",
		"[::]:9101":       "binds every interface",
		"192.0.2.5:80":    "is not a loopback address",
		"example.com:80":  "is not a loopback address",
		"[2001:db8::1]:1": "is not a loopback address",
		"nonsense":        "is not host:port",
		"127.0.0.1":       "is not host:port",
	}
	for a, want := range refused {
		err := CheckBind(ctx, a, false)
		var r *RefusedError
		if !errors.As(err, &r) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want a refusal containing %q", a, err, want)
			continue
		}
		if a != "nonsense" && a != "127.0.0.1" && !strings.Contains(err.Error(), "--unsafe-listen") {
			t.Errorf("%s: the fix must name --unsafe-listen: %v", a, err)
		}
		if !strings.Contains(err.Error(), "127.0.0.1:9101") {
			t.Errorf("%s: the fix must offer a loopback address: %v", a, err)
		}
		if err := CheckBind(ctx, a, true); a != "nonsense" && a != "127.0.0.1" && err != nil {
			t.Errorf("%s with --unsafe-listen: %v", a, err)
		}
	}
	// localhost that resolves wide is refused on what it resolves to.
	resolver = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("198.51.100.7")}, nil
	}
	if err := CheckBind(ctx, "localhost:9101", false); err == nil || !strings.Contains(err.Error(), "resolves to 198.51.100.7") {
		t.Errorf("a localhost that resolves wide: %v", err)
	}
	resolver = func(context.Context, string) ([]net.IP, error) { return nil, errors.New("boom") }
	if err := CheckBind(ctx, "localhost:9101", false); err == nil {
		t.Error("an unresolvable localhost must be refused")
	}
}

func TestListenFailuresNameTheFix(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, err = listen(l.Addr().String())
	var le *ListenError
	if !errors.As(err, &le) || !strings.Contains(err.Error(), "address already in use") || !strings.Contains(err.Error(), "free port") {
		t.Errorf("%v", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Error("one line")
	}
}

// ---- helpers ----------------------------------------------------------------

type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func tree(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"app", "lib"} {
		if err := os.MkdirAll(filepath.Join(d, p, "target", "debug"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, p, "Cargo.toml"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, p, "target", "debug", "blob"), make([]byte, 40000), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func spec(t *testing.T, roots ...scan.RootSpec) scan.Spec {
	t.Helper()
	ref, err := contain.NewRefuser("/nonexistent-home", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return scan.Spec{Roots: roots, Workers: 2, Refuse: ref}
}

func wsRoot(alias, path string) scan.RootSpec {
	return scan.RootSpec{Alias: alias, Mode: config.WorkspaceRoot, Path: path, MaxFiles: 2_000_000, MaxDuration: 30 * time.Second, DiscoveryDepth: 3, MaxWorkspaces: 64, Profiles: true}
}

type running struct {
	addr   string
	cancel context.CancelFunc
	done   chan error
	scans  chan *scan.Document
	log    *logCapture
}

func start(t *testing.T, o Options, level slog.Level) *running {
	t.Helper()
	lc := &logCapture{}
	o.Log = slog.New(slog.NewTextHandler(lc, &slog.HandlerOptions{Level: level}))
	if o.Info == (version.Info{}) {
		o.Info = version.Info{Version: "v-test", Revision: "r", GoVersion: "go-test"}
	}
	if o.Interval == 0 {
		o.Interval = time.Minute
	}
	if o.Addr == "" {
		o.Addr = "127.0.0.1:0"
	}
	if o.ConfigName == "" {
		o.ConfigName = "cubit.yaml"
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cancel: cancel, done: make(chan error, 1), scans: make(chan *scan.Document, 16), log: lc}
	listening := make(chan string, 1)
	o.OnListen = func(a string) { listening <- a }
	o.OnScan = func(d *scan.Document) { r.scans <- d }
	go func() { r.done <- Run(ctx, o) }()
	select {
	case r.addr = <-listening:
	case err := <-r.done:
		t.Fatalf("Run returned early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no listener")
	}
	t.Cleanup(func() { cancel(); <-r.done })
	return r
}

func (r *running) get(t *testing.T, method, path string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, "http://"+r.addr+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (r *running) waitScan(t *testing.T) *scan.Document {
	t.Helper()
	select {
	case d := <-r.scans:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("no scan finished")
		return nil
	}
}

// ---- behaviour --------------------------------------------------------------

func TestMetricsAndHealthzOverHTTP(t *testing.T) {
	d := tree(t)
	r := start(t, Options{Spec: spec(t, wsRoot("p", d)), Wait: func(time.Duration) time.Duration { return time.Hour }}, slog.LevelInfo)
	r.waitScan(t)

	resp, body := r.get(t, "GET", "/metrics")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	for _, want := range []string{
		`cubit_root_bytes{root="p"}`, `cubit_workspace_target_bytes{root="p",workspace="app"}`,
		`cubit_scan_success{root="p"} 1`, `cubit_scans_total{result="ok"} 1`, `cubit_build_info{version="v-test"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
	if strings.Contains(body, d) {
		t.Error("/metrics holds a filesystem path")
	}

	resp, body = r.get(t, "GET", "/healthz")
	if resp.StatusCode != http.StatusOK || body != "ok\n" {
		t.Errorf("healthz %d %q", resp.StatusCode, body)
	}
	if resp, _ = r.get(t, "GET", "/nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path: %d", resp.StatusCode)
	}
	if resp, _ = r.get(t, "GET", "/metrics/extra"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("subpath: %d", resp.StatusCode)
	}
	for _, p := range []string{"/metrics", "/healthz"} {
		resp, _ = r.get(t, "POST", p)
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" {
			t.Errorf("POST %s: %d allow %q", p, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}
	resp, body = r.get(t, "HEAD", "/metrics")
	if resp.StatusCode != http.StatusOK || body != "" || resp.ContentLength <= 0 {
		t.Errorf("HEAD: %d %q len %d", resp.StatusCode, body, resp.ContentLength)
	}
}

// Before the first scan finishes /metrics is not empty: build info and the zero counters.
func TestMetricsBeforeTheFirstScan(t *testing.T) {
	d := tree(t)
	gate := make(chan struct{})
	o := Options{Spec: spec(t, wsRoot("p", d)), Wait: func(time.Duration) time.Duration { return time.Hour }}
	o.Env.Now = func() time.Time { <-gate; return time.Now() } // the scan blocks on its first clock read
	r := start(t, o, slog.LevelInfo)
	_, body := r.get(t, "GET", "/metrics")
	if !strings.Contains(body, "cubit_build_info") || strings.Contains(body, "cubit_root_bytes") || strings.Contains(body, "cubit_scan_success") {
		t.Errorf("pre-scan /metrics:\n%s", body)
	}
	if !strings.Contains(body, `cubit_scans_total{result="ok"} 0`) || !strings.Contains(body, `cubit_scan_errors_total{root="p",reason="root_missing"} 0`) {
		t.Errorf("zero counters are missing:\n%s", body)
	}
	if resp, _ := r.get(t, "GET", "/healthz"); resp.StatusCode != http.StatusOK {
		t.Error("healthz must answer while a scan is still running")
	}
	close(gate)
	r.waitScan(t)
}

func TestScansRepeatAndStopOnCancel(t *testing.T) {
	d := tree(t)
	o := Options{Spec: spec(t, wsRoot("p", d)), Wait: func(time.Duration) time.Duration { return 10 * time.Millisecond }}
	r := start(t, o, slog.LevelInfo)
	for i := 0; i < 3; i++ {
		r.waitScan(t)
	}
	r.cancel()
	select {
	case err := <-r.done:
		if err != nil {
			t.Errorf("a stop by cancel is a clean shutdown, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	r.done <- nil // the cleanup waits on the channel once more
}

func TestRunRefusesAWideBindBeforeScanning(t *testing.T) {
	d := tree(t)
	err := Run(context.Background(), Options{Spec: spec(t, wsRoot("p", d)), Addr: "0.0.0.0:0", Interval: time.Minute})
	var rf *RefusedError
	if !errors.As(err, &rf) {
		t.Fatalf("%v", err)
	}
}

func TestUnsafeBindWarnsOnceThenListens(t *testing.T) {
	d := tree(t)
	r := start(t, Options{Spec: spec(t, wsRoot("p", d)), Addr: "0.0.0.0:0", Unsafe: true, Wait: func(time.Duration) time.Duration { return time.Hour }}, slog.LevelInfo)
	r.waitScan(t)
	out := r.log.String()
	w := strings.Index(out, "level=WARN msg=\"listening on a non-loopback address; the metrics endpoint is unauthenticated\"")
	i := strings.Index(out, "level=INFO msg=listening")
	if w < 0 || i < 0 || w > i {
		t.Errorf("the unsafe-bind warning must come once, before the listening line:\n%s", out)
	}
	if strings.Count(out, "non-loopback") != 1 {
		t.Errorf("exactly one warning:\n%s", out)
	}
	if !strings.Contains(out, "hint=") || !strings.Contains(out, "addr=") {
		t.Errorf("warning attributes:\n%s", out)
	}
}

func TestLoopbackWithTheFlagDoesNotWarn(t *testing.T) {
	d := tree(t)
	r := start(t, Options{Spec: spec(t, wsRoot("p", d)), Unsafe: true, Wait: func(time.Duration) time.Duration { return time.Hour }}, slog.LevelInfo)
	r.waitScan(t)
	if strings.Contains(r.log.String(), "level=WARN") {
		t.Errorf("a loopback bind needs no warning:\n%s", r.log.String())
	}
}

// ---- the log inventory ------------------------------------------------------

func logLines(s string) []map[string]string {
	var out []map[string]string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l == "" {
			continue
		}
		m := map[string]string{}
		rest := l
		for rest != "" {
			eq := strings.IndexByte(rest, '=')
			if eq < 0 {
				break
			}
			key := rest[:eq]
			rest = rest[eq+1:]
			var val string
			if strings.HasPrefix(rest, `"`) {
				end := 1
				for end < len(rest) && (rest[end] != '"' || rest[end-1] == '\\') {
					end++
				}
				val, rest = rest[1:end], strings.TrimPrefix(rest[min(end+1, len(rest)):], " ")
			} else if sp := strings.IndexByte(rest, ' '); sp >= 0 {
				val, rest = rest[:sp], rest[sp+1:]
			} else {
				val, rest = rest, ""
			}
			m[key] = val
		}
		out = append(out, m)
	}
	return out
}

func keys(m map[string]string) string {
	var ks []string
	for k := range m {
		if k != "time" && k != "level" && k != "msg" {
			ks = append(ks, k)
		}
	}
	// stable order for comparison
	for i := range ks {
		for j := i + 1; j < len(ks); j++ {
			if ks[j] < ks[i] {
				ks[i], ks[j] = ks[j], ks[i]
			}
		}
	}
	return strings.Join(ks, ",")
}

func TestLogInventory(t *testing.T) {
	d := tree(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	capped := wsRoot("capped", d)
	capped.MaxFiles = 1000
	for i := 0; i < 1200; i++ { // enough entries to trip the cap
		if err := os.WriteFile(filepath.Join(d, "lib", "target", "debug", "f"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('A'+i/26))), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ok := wsRoot("good", filepath.Join(d, "app"))
	_ = ok
	r := start(t, Options{
		Spec: spec(t,
			scan.RootSpec{Alias: "gone", Mode: config.Sccache, Path: missing, MaxFiles: 2_000_000, MaxDuration: time.Minute},
			capped,
			scan.RootSpec{Alias: "fine", Mode: config.TargetDir, Path: filepath.Join(d, "app", "target"), MaxFiles: 2_000_000, MaxDuration: time.Minute, Name: "fine", Profiles: true},
		),
		ConfigName: "cubit.yaml",
		Wait:       func(time.Duration) time.Duration { return time.Hour },
	}, slog.LevelDebug)
	r.waitScan(t)
	out := r.log.String()
	lines := logLines(out)

	byMsg := map[string][]map[string]string{}
	for _, l := range lines {
		byMsg[l["msg"]] = append(byMsg[l["msg"]], l)
	}
	want := map[string]string{
		"listening":     "addr,config,interval,roots",
		"scan finished": "duration,result,roots_failed,roots_ok,roots_partial",
		"root scan failed; keeping last good values": "alias,hint,reason",
		"root scan partial; sizes are lower bounds":  "alias,hint,reasons",
	}
	for msg, ks := range want {
		got := byMsg[msg]
		if len(got) == 0 {
			t.Errorf("no line %q in:\n%s", msg, out)
			continue
		}
		if keys(got[0]) != ks {
			t.Errorf("%q has attributes %q, want %q", msg, keys(got[0]), ks)
		}
	}
	if f := byMsg["scan finished"]; len(f) == 1 && (f[0]["result"] != "partial" || f[0]["roots_ok"] != "1" || f[0]["roots_partial"] != "1" || f[0]["roots_failed"] != "1") {
		t.Errorf("scan finished: %v", f[0])
	}
	if l := byMsg["listening"]; len(l) == 1 && (l[0]["config"] != "cubit.yaml" || l[0]["roots"] != "3" || l[0]["interval"] != "1m0s") {
		t.Errorf("listening: %v", l[0])
	}

	// Every warning says what to do; nothing at info or above holds a path.
	for _, l := range lines {
		if l["level"] == "WARN" && l["hint"] == "" {
			t.Errorf("a warning without a hint: %v", l)
		}
		if l["level"] != "DEBUG" && (strings.Contains(l["msg"], d) || strings.Contains(strings.Join(vals(l), " "), d) || strings.Contains(strings.Join(vals(l), " "), missing)) {
			t.Errorf("a path at %s: %v", l["level"], l)
		}
		if l["level"] != "DEBUG" && strings.Contains(strings.Join(vals(l), " "), "/") && l["msg"] != "scan finished" && !strings.Contains(l["hint"], "/metrics") && !strings.Contains(l["hint"], "ulimit") {
			t.Errorf("suspicious slash at %s: %v", l["level"], l)
		}
	}
	// Debug carries skip counts, per root, non-zero reasons only.
	for _, l := range byMsg["skip counts"] {
		if l["alias"] == "" || l["level"] != "DEBUG" {
			t.Errorf("skip counts: %v", l)
		}
	}
}

func vals(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestScanFinishedOrderingInTheLog(t *testing.T) {
	d := tree(t)
	r := start(t, Options{Spec: spec(t, wsRoot("p", d)), ConfigName: "cubit.yaml", Wait: func(time.Duration) time.Duration { return time.Hour }}, slog.LevelInfo)
	r.waitScan(t)
	out := r.log.String()
	if strings.Index(out, "msg=listening") > strings.Index(out, `msg="scan finished"`) {
		t.Errorf("listening comes before the first scan finishes:\n%s", out)
	}
}

func TestJitterIsWithinTenPercent(t *testing.T) {
	for i := 0; i < 1000; i++ {
		d := Jitter(100 * time.Second)
		if d < 90*time.Second || d > 110*time.Second {
			t.Fatalf("%v", d)
		}
	}
}

// /healthz is liveness only. It answers 200 while every root is failing and
// while a scan is blocked: a supervisor that restarts on it must not turn a full
// disk into a restart loop.
func TestHealthzIgnoresScanHealth(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	r := start(t, Options{
		Spec: spec(t, scan.RootSpec{Alias: "gone", Mode: config.Sccache, Path: missing, MaxFiles: 2_000_000, MaxDuration: time.Minute}),
		Wait: func(time.Duration) time.Duration { return time.Hour },
	}, slog.LevelInfo)
	doc := r.waitScan(t)
	if doc.ExitCode() != 2 {
		t.Fatalf("every root failing, exit %d", doc.ExitCode())
	}
	if resp, body := r.get(t, "GET", "/healthz"); resp.StatusCode != http.StatusOK || body != "ok\n" {
		t.Errorf("healthz while everything fails: %d %q", resp.StatusCode, body)
	}
	_, metrics := r.get(t, "GET", "/metrics")
	if !strings.Contains(metrics, `cubit_scan_success{root="gone"} 0`) {
		t.Error("the failure is visible in /metrics, which is where staleness belongs")
	}
}

// Shutdown during a scan updates nothing: no counter, no snapshot change.
func TestCancelDuringAScanPublishesNothing(t *testing.T) {
	d := tree(t)
	gate := make(chan struct{})
	var once sync.Once
	entered := make(chan struct{})
	o := Options{Spec: spec(t, wsRoot("p", d)), Wait: func(time.Duration) time.Duration { return time.Hour }}
	o.Env.Now = func() time.Time {
		once.Do(func() { close(entered) })
		<-gate
		return time.Now()
	}
	r := start(t, o, slog.LevelInfo)
	<-entered
	_, before := r.get(t, "GET", "/metrics")
	r.cancel()
	close(gate)
	if err := <-r.done; err != nil {
		t.Fatal(err)
	}
	r.done <- nil
	select {
	case <-r.scans:
		t.Error("a canceled scan must not be reported as finished")
	default:
	}
	if strings.Contains(before, "cubit_scan_success") || !strings.Contains(before, `cubit_scans_total{result="failed"} 0`) {
		t.Errorf("the snapshot before: %s", before)
	}
	if strings.Contains(r.log.String(), "scan finished") {
		t.Error("no scan finished")
	}
}
