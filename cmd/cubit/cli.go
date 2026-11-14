package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hedronite/cubit/internal/config"
	"github.com/Hedronite/cubit/internal/report"
	"github.com/Hedronite/cubit/internal/scan"
	"github.com/Hedronite/cubit/internal/serve"
	"github.com/Hedronite/cubit/internal/textfile"
	"github.com/Hedronite/cubit/internal/version"
)

// Exit codes: 0 to 2 mean a scan ran, 3 and up mean it could not.
const (
	exitUsage    = 3
	exitConfig   = 4
	exitServe    = 5
	exitInternal = 6
	// exitTextfile: `scan --textfile-dir` could not write cubit.prom after the
	// scan. The document is still printed. It is not 6, which means a bug.
	exitTextfile = 7
)

// exitError carries an exit code out of a command.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func usageErr(format string, a ...any) error {
	return &exitError{exitUsage, fmt.Sprintf(format, a...)}
}

const defaultConfig = "cubit.yaml"

func newRoot(stdout, stderr io.Writer) *cobra.Command {
	var configFile string
	root := &cobra.Command{
		Use:   "cubit",
		Short: "Measure Cargo disk usage on build hosts",
		Long: `Cubit measures Cargo disk usage on build hosts and exports it as
Prometheus metrics. It measures; it never cleans.`,
		Version:       version.Get().Line(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetVersionTemplate("{{.Version}}\n")
	root.PersistentFlags().StringVar(&configFile, "config", defaultConfig, "config file")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageErr("%v", err) })
	root.CompletionOptions.DisableDefaultCmd = true

	root.AddCommand(scanCmd(&configFile, stdout, stderr))
	root.AddCommand(validateCmd(&configFile, stdout, stderr))
	root.AddCommand(versionCmd(stdout))
	root.AddCommand(serveCmd(&configFile, stderr))
	return root
}

func execute(args []string, stdout, stderr io.Writer) int {
	root := newRoot(stdout, stderr)
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintf(stderr, "cubit: %s\n", ee.msg)
		}
		if ee.code == exitUsage {
			fmt.Fprintln(stderr, "Run 'cubit --help' for usage.")
		}
		return ee.code
	}
	// cobra's own errors: unknown command, bad args.
	fmt.Fprintf(stderr, "cubit: %v\nRun 'cubit --help' for usage.\n", err)
	return exitUsage
}

// exitCode lets a command finish with a code and no message.
func exitCode(code int) error {
	if code == 0 {
		return nil
	}
	return &exitError{code: code}
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

// load reads, parses and checks the config. Errors are printed to stderr and
// returned as exit code 4. Warnings and info lines are returned for the caller.
func load(file string, stderr io.Writer, quiet bool) (*config.Config, []config.Finding, error) {
	data, err := os.ReadFile(file) //nolint:gosec // the operator names the config file
	if err != nil {
		return nil, nil, &exitError{exitConfig, fmt.Sprintf("cannot read config %s: %s; pass --config, or create it from examples/cubit.yaml", file, errReason(err))}
	}
	cfg, fs := config.Parse(data)
	if cfg != nil {
		fs = config.Check(cfg, home())
	}
	if config.HasErrors(fs) {
		report.Findings(stderr, file, fs)
		return nil, fs, &exitError{code: exitConfig, msg: "invalid config; nothing was scanned"}
	}
	if !quiet {
		var shown []config.Finding
		for _, f := range fs {
			if f.Severity != config.Info {
				shown = append(shown, f)
			}
		}
		report.Findings(stderr, file, shown)
	}
	return cfg, fs, nil
}

func errReason(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

func scanCmd(configFile *string, stdout, stderr io.Writer) *cobra.Command {
	var asJSON bool
	var textfileDir string
	c := &cobra.Command{
		Use:   "scan",
		Short: "Scan configured roots once and print the result",
		Long: `Scan every configured root once, print the result, and exit.
Human table by default; --json prints cubit.scan/v0 alone on stdout.
With --textfile-dir DIR, also write cubit.prom (node_exporter
textfile format) after the scan. The directory must already exist
and sit outside the refuse list; Cubit never creates it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := load(*configFile, stderr, false)
			if err != nil {
				return err
			}
			spec, err := scan.FromConfig(cfg, home())
			if err != nil {
				return &exitError{exitConfig, err.Error()}
			}
			var tw *textfile.Writer
			if textfileDir != "" {
				if tw, err = openTextfile(textfileDir, spec, exitConfig); err != nil {
					return err
				}
				defer func() { _ = tw.Close() }()
				for _, w := range tw.Warnings {
					fmt.Fprintf(stderr, "cubit: warning: --textfile-dir %s: %s\n", textfileDir, w)
				}
			}
			ctx, which, stop := watchSignals(context.Background())
			defer stop()
			doc, err := scan.Scan(ctx, spec, scan.Env{})
			if err != nil {
				if sig := which(); sig != nil {
					return &exitError{exitForSignal(sig), "interrupted"}
				}
				return &exitError{exitInternal, err.Error()}
			}
			// The file is written before the result is printed, so a failure shows
			// first on stderr, and the document on stdout stays whole either way.
			writeFailed := false
			if tw != nil {
				body, err := oneShotMetrics(spec, doc)
				if err == nil {
					err = tw.Write(body)
				}
				if err != nil {
					writeFailed = true
					fmt.Fprintf(stderr, "cubit: %s\n", writeFailure(textfileDir, err))
				}
			}
			if asJSON {
				b, err := doc.JSON()
				if err != nil {
					return &exitError{exitInternal, err.Error()}
				}
				_, _ = stdout.Write(b)
			} else {
				report.Table(stdout, doc, *configFile)
			}
			if writeFailed {
				return exitCode(exitTextfile)
			}
			return exitCode(doc.ExitCode())
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print cubit.scan/v0 instead of the human table")
	c.Flags().StringVar(&textfileDir, "textfile-dir", "", "write cubit.prom into `DIR` after the scan (node_exporter textfile format)")
	return c
}

func validateCmd(configFile *string, stdout, stderr io.Writer) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "validate",
		Short: "Check the config and report every problem at once",
		Long: `Check the config and report every problem at once, not just the first.

Checks include duplicate aliases, nested or overlapping roots, a root
inside a refused prefix, and two roots resolving to one real path.
It also prints each root's resolved mode. The full list is in
docs/config.md.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := os.ReadFile(*configFile)
			if err != nil {
				return &exitError{exitConfig, fmt.Sprintf("cannot read config %s: %s; pass --config, or create it from examples/cubit.yaml", *configFile, errReason(err))}
			}
			cfg, fs := config.Parse(data)
			if cfg != nil {
				fs = config.Check(cfg, home())
			}
			bad := config.HasErrors(fs)
			roots := -1
			if cfg != nil {
				roots = len(cfg.Roots)
			}
			for i := range fs {
				fs[i].File = *configFile
			}
			switch {
			case asJSON:
				out := struct {
					APIVersion string           `json:"apiVersion"`
					OK         bool             `json:"ok"`
					Findings   []config.Finding `json:"findings"`
				}{"cubit.validate/v0", !bad, append([]config.Finding{}, fs...)}
				b, _ := json.MarshalIndent(out, "", "  ")
				fmt.Fprintf(stdout, "%s\n", b)
			case bad:
				report.Findings(stderr, *configFile, fs)
				report.Summary(stderr, *configFile, fs, roots)
			default:
				report.Findings(stdout, *configFile, fs)
				report.Summary(stdout, *configFile, fs, roots)
			}
			if bad {
				return exitCode(exitConfig)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print findings as JSON")
	return c
}

func versionCmd(stdout io.Writer) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Long:  "Print the version. One stable line by default.",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if asJSON {
				fmt.Fprintln(stdout, version.Get().JSON())
			} else {
				fmt.Fprintln(stdout, version.Get().Line())
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, `print {"version":...,"revision":...,"go_version":...} instead`)
	return c
}

func serveCmd(configFile *string, stderr io.Writer) *cobra.Command {
	var listen, level, textfileDir string
	var unsafe bool
	c := &cobra.Command{
		Use:   "serve",
		Short: "Run the exporter and serve /metrics",
		Long: `Run the exporter: serve /metrics and /healthz on --listen.

/metrics serves the last finished scan and never walks on scrape;
before the first scan finishes it serves build info and zero counters
only. /healthz is 200 while the config is valid and the process is
serving; it never touches the scanner. Scans run on an interval
(default 5m), one in flight at a time.

With --textfile-dir DIR, the same bytes /metrics serves are also
written to cubit.prom in DIR after every scan.

The metrics endpoint is unauthenticated. The bind address must be a
loopback address; anything wider requires --unsafe-listen, which logs
one warning with the bound address.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			lvl, err := parseLevel(level)
			if err != nil {
				return usageErr("%v", err)
			}
			cfg, _, err := load(*configFile, stderr, false)
			if err != nil {
				return err
			}
			spec, err := scan.FromConfig(cfg, home())
			if err != nil {
				return &exitError{exitConfig, err.Error()}
			}
			var tw *textfile.Writer
			if textfileDir != "" {
				if tw, err = openTextfile(textfileDir, spec, exitServe); err != nil {
					return err
				}
				defer func() { _ = tw.Close() }()
			}
			log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: lvl}))
			spec.Log = log
			if tw != nil {
				for _, w := range tw.Warnings {
					log.Warn("the textfile directory is open to more writers than it should be", "detail", w,
						"hint", "restrict the directory to the user running cubit and the user reading it")
				}
			}
			ctx, _, stop := watchSignals(context.Background())
			defer stop()
			err = serve.Run(ctx, serve.Options{
				Spec: spec, Interval: cfg.EffectiveInterval(), Addr: listen, Unsafe: unsafe,
				ConfigName: filepath.Base(*configFile), Log: log, Info: version.Get(),
				Textfile: textfileSink(tw),
			})
			var refused *serve.RefusedError
			var lerr *serve.ListenError
			switch {
			case err == nil:
				return nil // a stop by signal is a clean shutdown
			case errors.As(err, &refused), errors.As(err, &lerr):
				return &exitError{exitServe, err.Error()}
			}
			return &exitError{exitInternal, err.Error()}
		},
	}
	c.Flags().StringVar(&listen, "listen", "127.0.0.1:9101", "listen on `ADDR`")
	c.Flags().BoolVar(&unsafe, "unsafe-listen", false, "allow a non-loopback bind (the metrics endpoint is unauthenticated)")
	c.Flags().StringVar(&level, "log-level", "info", "log `LEVEL`: debug, info, warn or error")
	c.Flags().StringVar(&textfileDir, "textfile-dir", "", "rewrite cubit.prom in `DIR` after every scan (node_exporter textfile format)")
	return c
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("--log-level %q is not one of debug, info, warn, error", s)
}

// textfileSink adapts a Writer for serve; a nil Writer is no sink.
func textfileSink(w *textfile.Writer) serve.Sink {
	if w == nil {
		return nil
	}
	return w
}
