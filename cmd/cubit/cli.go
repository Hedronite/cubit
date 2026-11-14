package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Hedronite/cubit/internal/config"
	"github.com/Hedronite/cubit/internal/report"
	"github.com/Hedronite/cubit/internal/scan"
	"github.com/Hedronite/cubit/internal/version"
)

// Exit codes: 0 to 2 mean a scan ran, 3 and up mean it could not.
const (
	exitUsage    = 3
	exitConfig   = 4
	exitServe    = 5
	exitInternal = 6
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
	root.AddCommand(serveStub())
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
	c := &cobra.Command{
		Use:   "scan",
		Short: "Scan configured roots once and print the result",
		Long: `Scan every configured root once, print the result, and exit.
Human table by default; --json prints cubit.scan/v0 alone on stdout.`,
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
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			doc, err := scan.Scan(ctx, spec, scan.Env{})
			if err != nil {
				return &exitError{exitInternal, "interrupted; no result"}
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
			return exitCode(doc.ExitCode())
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print cubit.scan/v0 instead of the human table")
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
					OK       bool             `json:"ok"`
					Findings []config.Finding `json:"findings"`
				}{!bad, append([]config.Finding{}, fs...)}
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

// serveStub keeps the command name reserved. serve arrives with C1b.
func serveStub() *cobra.Command {
	return &cobra.Command{
		Use:    "serve",
		Short:  "Run the exporter and serve /metrics",
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(*cobra.Command, []string) error {
			return &exitError{exitServe, "serve is not in this build yet"}
		},
	}
}
