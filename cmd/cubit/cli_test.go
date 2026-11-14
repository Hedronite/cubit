package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

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

func TestServeIsNotInThisBuild(t *testing.T) {
	code, out, errOut := exec(t, "serve")
	if code != 5 || out != "" || !strings.Contains(errOut, "not in this build") {
		t.Errorf("%d %q %q", code, out, errOut)
	}
	if _, help, _ := exec(t, "--help"); strings.Contains(help, "serve") {
		t.Error("serve is hidden until it exists")
	}
}
