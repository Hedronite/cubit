package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Hedronite/cubit/internal/contain"
	"github.com/Hedronite/cubit/internal/textfile"
	"github.com/Hedronite/cubit/schemas"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func exec(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(args, &o, &e)
	return code, o.String(), e.String()
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run the tests with -update to create it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the golden file.\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

func TestHelpSnapshots(t *testing.T) {
	for name, args := range map[string][]string{
		"help-cubit.golden":    {"--help"},
		"help-scan.golden":     {"scan", "--help"},
		"help-validate.golden": {"validate", "--help"},
		"help-version.golden":  {"version", "--help"},
		"help-serve.golden":    {"serve", "--help"},
	} {
		code, out, errOut := exec(t, args...)
		if code != 0 || errOut != "" {
			t.Fatalf("%v: exit %d, stderr %q", args, code, errOut)
		}
		golden(t, name, out)
	}
}

func TestVersion(t *testing.T) {
	code, out, _ := exec(t, "version")
	if code != 0 || !regexp.MustCompile(`^cubit \S+ \(\S+, go\S+\)\n$`).MatchString(out) {
		t.Errorf("exit %d, %q", code, out)
	}
	code, out, _ = exec(t, "version", "--json")
	var v struct{ Version, Revision, GoVersion string }
	var raw map[string]string
	if code != 0 || json.Unmarshal([]byte(out), &raw) != nil || raw["version"] == "" || raw["revision"] == "" || raw["go_version"] == "" || strings.Count(out, "\n") != 1 {
		t.Errorf("exit %d, %q %v", code, out, v)
	}
	code, out, _ = exec(t, "--version")
	if code != 0 || !strings.HasPrefix(out, "cubit ") {
		t.Errorf("--version: %d %q", code, out)
	}
}

func TestUsageErrorsAreExit3AndPrintNothingOnStdout(t *testing.T) {
	for _, args := range [][]string{{"--nope"}, {"frobnicate"}, {"scan", "extra"}, {"scan", "--json=maybe"}, {"version", "--json", "--bogus"}, {"validate", "x"}} {
		code, out, errOut := exec(t, args...)
		if code != 3 || out != "" || !strings.Contains(errOut, "cubit") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
}

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cubit.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigErrorsAreExit4WithNothingOnStdout(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	bad := writeCfg(t, "schema: cubit.config/v0\nroots:\n  - alias: Bad_Alias\n    path: relative\n    mode: sccache\n  - alias: x\n    path: /srv/x\n    mode: nonsense\n")
	for _, cmd := range []string{"scan", "validate"} {
		code, out, errOut := exec(t, cmd, "--config", missing)
		if code != 4 || out != "" || !strings.Contains(errOut, "cannot read config") || !strings.Contains(errOut, "fix") && !strings.Contains(errOut, "examples/cubit.yaml") {
			t.Errorf("%s missing: %d %q %q", cmd, code, out, errOut)
		}
		code, out, errOut = exec(t, cmd, "--config", bad)
		if code != 4 || out != "" {
			t.Errorf("%s bad: exit %d, stdout %q", cmd, code, out)
		}
		for _, want := range []string{bad + ":3:", "Bad_Alias", "fix:", bad + ":8:"} {
			if !strings.Contains(errOut, want) {
				t.Errorf("%s: stderr lacks %q:\n%s", cmd, want, errOut)
			}
		}
	}
	// A refused root is exit 4 for scan, and nothing is scanned.
	home, _ := os.UserHomeDir()
	if home != "" {
		cfg := writeCfg(t, "schema: cubit.config/v0\nroots:\n  - alias: x\n    path: "+home+"/.ssh/agents\n    mode: sccache\n")
		code, out, errOut := exec(t, "scan", "--config", cfg)
		if code != 4 || out != "" || !strings.Contains(errOut, "refuse-list") {
			t.Errorf("refused root: %d %q %q", code, out, errOut)
		}
	}
}

func scanSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	var meta struct {
		ID string `json:"$id"`
	}
	json.Unmarshal(schemas.Scan, &meta)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemas.Scan))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(meta.ID, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestScanJSONIsTheDocumentAloneAndExitCodesFollowTheContract(t *testing.T) {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "app", "target", "debug"), 0o755)
	os.WriteFile(filepath.Join(d, "app", "Cargo.toml"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(d, "app", "target", "debug", "f"), make([]byte, 50000), 0o644)
	gone := filepath.Join(t.TempDir(), "does-not-exist")

	cases := map[string]struct {
		roots string
		want  int
	}{
		"all ok":      {"  - {alias: p, path: " + d + ", mode: workspace-root}\n", 0},
		"mixed":       {"  - {alias: p, path: " + d + ", mode: workspace-root}\n  - {alias: q, path: " + gone + ", mode: sccache}\n", 1},
		"none usable": {"  - {alias: q, path: " + gone + ", mode: sccache}\n", 2},
	}
	sch := scanSchema(t)
	for name, c := range cases {
		cfg := writeCfg(t, "schema: cubit.config/v0\nroots:\n"+c.roots)
		code, out, errOut := exec(t, "scan", "--json", "--config", cfg)
		if code != c.want {
			t.Errorf("%s: exit %d, want %d (stderr %q)", name, code, c.want, errOut)
		}
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(out))
		if err != nil {
			t.Fatalf("%s: stdout is not one JSON document: %v\n%s", name, err, out)
		}
		if err := sch.Validate(inst); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if strings.Contains(out, d) {
			t.Errorf("%s: the document holds a path", name)
		}
	}
	// The human table is the same numbers, and the warning lines stay out of the JSON.
	cfg := writeCfg(t, "schema: cubit.config/v0\nroots:\n"+cases["mixed"].roots)
	code, out, _ := exec(t, "scan", "--config", cfg)
	if code != 1 || !strings.Contains(out, "ROOT") || !strings.Contains(out, "error:   q failed: root_missing; fix:") {
		t.Errorf("table: exit %d\n%s", code, out)
	}
	if strings.Contains(out, d) {
		t.Error("the table holds a path")
	}
}

func TestValidate(t *testing.T) {
	d := t.TempDir()
	ok := writeCfg(t, "schema: cubit.config/v0\nroots:\n  - {alias: p, path: "+d+", mode: workspace-root}\n")
	code, out, errOut := exec(t, "validate", "--config", ok)
	if code != 0 || !strings.Contains(out, "mode workspace-root") || !strings.Contains(out, "ok, 1 root(s)") {
		t.Errorf("exit %d\n%s%s", code, out, errOut)
	}
	code, out, _ = exec(t, "validate", "--json", "--config", ok)
	var res struct {
		OK       bool
		Findings []map[string]any
	}
	if code != 0 || json.Unmarshal([]byte(out), &res) != nil || !res.OK {
		t.Errorf("json: %d %s", code, out)
	}
	bad := writeCfg(t, "schema: cubit.config/v0\nroots:\n  - {alias: a, path: "+d+", mode: sccache}\n  - {alias: a, path: "+d+"x, mode: sccache}\n")
	code, out, _ = exec(t, "validate", "--json", "--config", bad)
	if code != 4 || json.Unmarshal([]byte(out), &res) != nil || res.OK || len(res.Findings) == 0 {
		t.Errorf("json on a bad config: %d %s", code, out)
	}
}

type panicWriter struct{}

func (panicWriter) Write([]byte) (int, error) { panic("boom") }

// main recovers a panic into code 6 and never lets a crash read as a scan result.
func TestPanicIsExit6(t *testing.T) {
	var e bytes.Buffer
	if code := run([]string{"version"}, panicWriter{}, &e); code != 6 || !strings.Contains(e.String(), "internal error") {
		t.Errorf("exit %d, stderr %q", code, e.String())
	}
}

func TestServeRefusesAWideBindWithExit5AndNoStdout(t *testing.T) {
	d := t.TempDir()
	cfg := writeCfg(t, "schema: cubit.config/v0\nroots:\n  - {alias: p, path: "+d+", mode: sccache}\n")
	for addr, want := range map[string]string{
		"0.0.0.0:9101": "is not a loopback address; pass --unsafe-listen",
		":9101":        "has no host and would bind every interface",
		"[::]:9101":    "binds every interface",
	} {
		code, out, errOut := exec(t, "serve", "--config", cfg, "--listen", addr)
		if code != 5 || out != "" || !strings.Contains(errOut, "cubit: --listen "+addr) || !strings.Contains(errOut, want) || strings.Count(errOut, "\n") != 1 {
			t.Errorf("%s: exit %d stdout %q stderr %q", addr, code, out, errOut)
		}
	}
	code, _, errOut := exec(t, "serve", "--config", cfg, "--log-level", "loud")
	if code != 3 || !strings.Contains(errOut, "--log-level") {
		t.Errorf("bad level: %d %q", code, errOut)
	}
	code, out, _ := exec(t, "serve", "--config", filepath.Join(d, "missing.yaml"))
	if code != 4 || out != "" {
		t.Errorf("missing config: %d", code)
	}
}

func TestServeHelpIsInTheRootHelp(t *testing.T) {
	_, help, _ := exec(t, "--help")
	if !strings.Contains(help, "serve") {
		t.Error("serve is built, so it is listed")
	}
}

func TestSignalExitCodes(t *testing.T) {
	if exitForSignal(syscall.SIGINT) != 130 || exitForSignal(syscall.SIGTERM) != 143 {
		t.Error("128 plus the signal number")
	}
}

func TestWatchSignalsCancelsAndSaysWhich(t *testing.T) {
	ctx, which, stop := watchSignals(context.Background())
	defer stop()
	if which() != nil {
		t.Fatal("no signal yet")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the context was not canceled")
	}
	if which() != syscall.SIGTERM {
		t.Errorf("which = %v", which())
	}
}

func TestValidateJSONIsTheDocumentOfItsSchema(t *testing.T) {
	d := t.TempDir()
	var meta struct {
		ID string `json:"$id"`
	}
	json.Unmarshal(schemas.Validate, &meta)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemas.Validate))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(meta.ID, doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		cfg  string
		code int
	}{
		"ok":     {"schema: cubit.config/v0\nroots:\n  - {alias: p, path: " + d + ", mode: sccache}\n", 0},
		"errors": {"schema: cubit.config/v0\nroots:\n  - {alias: P, path: rel, mode: nope}\n", 4},
		"yaml":   {"schema: cubit.config/v0\nroots:\n\t- x\n", 4},
		"cross":  {"schema: cubit.config/v0\nroots:\n  - {alias: a, path: " + d + ", mode: sccache}\n  - {alias: a, path: " + d + "/x, mode: sccache}\n", 4},
	}
	for name, c := range cases {
		code, out, _ := exec(t, "validate", "--json", "--config", writeCfg(t, c.cfg))
		if code != c.code {
			t.Errorf("%s: exit %d, want %d", name, code, c.code)
		}
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(out))
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		if err := sch.Validate(inst); err != nil {
			t.Errorf("%s: output violates cubit.validate/v0: %v\n%s", name, err, out)
		}
		if !strings.Contains(out, `"schema": "cubit.validate/v0"`) {
			t.Errorf("%s: no schema field", name)
		}
	}
}

func tfConfig(t *testing.T) (cfg, tree string) {
	t.Helper()
	tree = t.TempDir()
	os.MkdirAll(filepath.Join(tree, "app", "target", "debug"), 0o755)
	os.WriteFile(filepath.Join(tree, "app", "Cargo.toml"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(tree, "app", "target", "debug", "f"), make([]byte, 50000), 0o644)
	return writeCfg(t, "schema: cubit.config/v0\nroots:\n  - {alias: p, path: "+tree+", mode: workspace-root}\n  - {alias: gone, path: "+tree+"-missing, mode: sccache}\n"), tree
}

func TestScanWritesTheTextfileAndStillPrintsTheDocument(t *testing.T) {
	cfg, tree := tfConfig(t)
	dir := t.TempDir()
	code, out, errOut := exec(t, "scan", "--json", "--config", cfg, "--textfile-dir", dir)
	if code != 1 || !strings.Contains(out, `"schema": "cubit.scan/v0"`) {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	b, err := os.ReadFile(filepath.Join(dir, "cubit.prom"))
	if err != nil {
		t.Fatal(err)
	}
	f := string(b)
	for _, want := range []string{`cubit_root_bytes{root="p"}`, `cubit_scan_success{root="gone"} 0`, `cubit_build_info`} {
		if !strings.Contains(f, want) {
			t.Errorf("cubit.prom lacks %q", want)
		}
	}
	for _, counter := range []string{"cubit_scans_total", "cubit_scan_errors_total"} {
		if strings.Contains(f, counter) {
			t.Errorf("%s is process-scoped; a file from a one-shot scan would reset it on every run", counter)
		}
	}
	if strings.Contains(f, `cubit_root_bytes{root="gone"}`) {
		t.Error("cron mode has no last good values: a failed root publishes health series only")
	}
	if strings.Contains(f, tree) {
		t.Error("a path in the file")
	}
	fi, _ := os.Stat(filepath.Join(dir, "cubit.prom"))
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

func TestTextfilePreflightIsExit4AndScansNothing(t *testing.T) {
	cfg, _ := tfConfig(t)
	base := t.TempDir()
	file := filepath.Join(base, "afile")
	os.WriteFile(file, nil, 0o644)
	ssh := filepath.Join(base, ".ssh")
	os.Mkdir(ssh, 0o755)
	for name, c := range map[string]struct{ dir, want string }{
		"missing":   {filepath.Join(base, "nope"), "no such directory; create it first (cubit never creates directories)"},
		"not a dir": {file, "not a directory; pass the directory node_exporter reads, not a file"},
		"refused":   {ssh, "sits inside a builtin refuse-list prefix; choose a directory outside every refused prefix"},
	} {
		code, out, errOut := exec(t, "scan", "--config", cfg, "--textfile-dir", c.dir)
		if code != 4 || out != "" || !strings.Contains(errOut, c.want) {
			t.Errorf("%s: exit %d stdout %q stderr %q", name, code, out, errOut)
		}
		if name == "refused" && (strings.Contains(errOut, ".ssh") || strings.Count(errOut, "\ncubit: ")+boolInt(strings.HasPrefix(errOut, "cubit: ")) != 1) {
			t.Errorf("a refused place is named by class only, on one line: %q", errOut)
		}
		if names, _ := os.ReadDir(ssh); name == "refused" && len(names) != 0 {
			t.Error("something was created in a refused directory")
		}
	}
}

func TestScanTextfileLockedIsExit4(t *testing.T) {
	cfg, _ := tfConfig(t)
	dir := t.TempDir()
	w, err := textfile.Open(dir, "/h", mustRefuser(t))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	code, out, errOut := exec(t, "scan", "--config", cfg, "--textfile-dir", dir)
	if code != 4 || out != "" || !strings.Contains(errOut, "another cubit holds the textfile lock") {
		t.Errorf("%d %q %q", code, out, errOut)
	}
}

func mustRefuser(t *testing.T) *contain.Refuser {
	t.Helper()
	r, err := contain.NewRefuser("/nonexistent-home", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A write that fails after the scan is exit 7, with the whole document on
// stdout and the failure on stderr: it never hides a result and never reads as success.
func TestAWriteFailureAfterTheScanIsExit7WithTheDocument(t *testing.T) {
	cfg, _ := tfConfig(t)
	dir := t.TempDir()
	// A non-empty directory where the temp file goes: preflight passes, and the
	// write cannot remove it.
	if err := os.MkdirAll(filepath.Join(dir, ".cubit.prom.tmp", "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := exec(t, "scan", "--json", "--config", cfg, "--textfile-dir", dir)
	if code != 7 {
		t.Errorf("exit %d, want 7 (not 6, which means a bug)", code)
	}
	if !strings.Contains(out, `"schema": "cubit.scan/v0"`) || !strings.HasSuffix(out, "}\n") {
		t.Errorf("the document must be whole on stdout:\n%s", out)
	}
	if !strings.Contains(errOut, "cubit: failed to write cubit.prom in "+dir+": ") || !strings.Contains(errOut, "the scan result is still valid") {
		t.Errorf("stderr: %q", errOut)
	}
}

func TestServeTextfilePreflightIsExit5(t *testing.T) {
	d := t.TempDir()
	cfg := writeCfg(t, "schema: cubit.config/v0\nroots:\n  - {alias: p, path: "+d+", mode: sccache}\n")
	code, out, errOut := exec(t, "serve", "--config", cfg, "--textfile-dir", filepath.Join(d, "nope"), "--listen", "127.0.0.1:0")
	if code != 5 || out != "" || !strings.Contains(errOut, "no such directory") || strings.Count(errOut, "\n") != 1 {
		t.Errorf("%d %q %q", code, out, errOut)
	}
	dir := t.TempDir()
	w, err := textfile.Open(dir, "/h", mustRefuser(t))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	code, _, errOut = exec(t, "serve", "--config", cfg, "--textfile-dir", dir, "--listen", "127.0.0.1:0")
	if code != 5 || !strings.Contains(errOut, "another cubit holds the textfile lock") {
		t.Errorf("a second writer: %d %q", code, errOut)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestScanTextfileWorldWritableDirIsExit4(t *testing.T) {
	cfg, _ := tfConfig(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := exec(t, "scan", "--config", cfg, "--textfile-dir", dir)
	if code != 4 || out != "" || !strings.Contains(errOut, "writable by everyone (mode 0777)") {
		t.Errorf("%d %q %q", code, out, errOut)
	}
}
