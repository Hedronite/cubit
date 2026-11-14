package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Hedronite/cubit/schemas"
)

func findOne(t *testing.T, doc string) Finding {
	t.Helper()
	cfg, fs := Parse([]byte(doc))
	if cfg != nil || len(fs) != 1 {
		t.Fatalf("want exactly one finding for:\n%s\ngot %+v", doc, fs)
	}
	return fs[0]
}

const head = "schema: cubit.config/v0\nroots:\n"

func TestFindingClasses(t *testing.T) {
	root := func(extra string) string {
		return head + "  - alias: ex\n    path: /srv/ex\n    mode: workspace-root\n" + extra
	}
	cases := []struct {
		name, doc, code, msg, fix string
		line                      int
	}{
		{"slug", head + "  - alias: Example_Projects\n    path: /srv/a\n    mode: sccache\n", CodeSlugPattern,
			"roots[0].alias 'Example_Projects': use lowercase letters, digits and '-'", "rename it to 'example-projects'.", 3},
		{"path", head + "  - alias: a\n    path: relative/dir\n    mode: sccache\n", CodePathRelative,
			"roots[0].path 'relative/dir': paths must be absolute or start with '~/'", "use an absolute path or '~/...'.", 4},
		{"duration", "schema: cubit.config/v0\nscan:\n  interval: 5 min\nroots:\n  - {alias: a, path: /srv/a, mode: sccache}\n", CodeBadDuration,
			"scan.interval '5 min' is not a duration", "write a duration like 30s, 5m, 1h30m", 3},
		{"enum", head + "  - alias: a\n    path: /srv/a\n    mode: workspace-rot\n", CodeBadEnum,
			"roots[0].mode 'workspace-rot' is not a mode", "'workspace-root' looks closest.", 5},
		{"key for mode", head + "  - alias: a\n    path: /srv/a\n    mode: target-dir\n    discovery_depth: 2\n", CodeKeyForMode,
			"roots[0].discovery_depth only applies when mode is workspace-root (this root is target-dir)", "remove discovery_depth, or change the root to workspace-root.", 6},
		{"missing", head + "  - alias: a\n    path: /srv/a\n", CodeMissingKey,
			"roots[0].mode is required", "add 'mode:' as one of workspace-root", 3},
		{"range above", root("    discovery_depth: 9\n"), CodeOutOfRange,
			"roots[0].discovery_depth 9 is above the cap of 6", "use 6 or less (the default is 3).", 6},
		{"range below", root("    max_workspaces: 0\n"), CodeOutOfRange,
			"roots[0].max_workspaces 0 is below the minimum of 1", "use 1 or more (the default is 64).", 6},
		{"unknown near", root("    max_file: 5000\n"), CodeUnknownKey,
			"roots[0].max_file is not a key here", "did you mean 'max_files'?", 6},
		{"unknown elsewhere", root("    max_workers: 4\n"), CodeUnknownKey,
			"roots[0].max_workers is not a key here", "'max_workers' lives under scan:", 6},
		{"unknown top", "schema: cubit.config/v0\nlisten: x\nroots:\n  - {alias: a, path: /srv/a, mode: sccache}\n", CodeUnknownKey,
			"listen is not a key here", "remove it.", 2},
		{"names key", root("    names:\n      a/../b: x\n"), CodeNamesKey,
			"not a usable relative path", "no '.' or '..' parts", 7},
		{"yaml", "schema: cubit.config/v0\nroots:\n\t- x\n", CodeYAMLSyntax,
			"this file is not valid YAML", "indent with spaces; YAML forbids tabs.", 3},
		{"schema value", strings.Replace(head, "v0", "v9", 1) + "  - {alias: a, path: /srv/a, mode: sccache}\n", CodeBadSchema,
			"schema must be 'cubit.config/v0'", "", 1},
	}
	for _, c := range cases {
		f := findOne(t, c.doc)
		if f.Code != c.code || f.Severity != Error {
			t.Errorf("%s: code %q severity %q, want %q error\n%+v", c.name, f.Code, f.Severity, c.code, f)
		}
		if !strings.Contains(f.Message, c.msg) {
			t.Errorf("%s: message %q lacks %q", c.name, f.Message, c.msg)
		}
		if !strings.Contains(f.Fix, c.fix) {
			t.Errorf("%s: fix %q lacks %q", c.name, f.Fix, c.fix)
		}
		if f.Line != c.line || f.Excerpt == "" {
			t.Errorf("%s: line %d excerpt %q, want line %d", c.name, f.Line, f.Excerpt, c.line)
		}
		if strings.Contains(f.Fix, "docs/config.md for what") {
			t.Errorf("%s: a fix must say what to do", c.name)
		}
	}
}

func TestExcerptIsOneShortLine(t *testing.T) {
	long := head + "  - {alias: a, path: /srv/a, mode: sccache, max_file: " + strings.Repeat("9", 120) + "}\n"
	f := findOne(t, long)
	if n := len([]rune(f.Excerpt)); n > 73 || !strings.HasSuffix(f.Excerpt, "…") || strings.Contains(f.Excerpt, "\n") {
		t.Errorf("excerpt %q (%d)", f.Excerpt, n)
	}
}

func TestSuggestSlug(t *testing.T) {
	for in, want := range map[string]string{"Example_Projects": "example-projects", "A B": "a-b", "---": "", "x": "x", "ünï": "n"} {
		if got := suggestSlug(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestOrderErrorsThenWarningsThenInfo(t *testing.T) {
	d := tmpDir(t)
	cfg := cfgFor(t, "roots:\n  - {alias: a, path: "+d+"/missing, mode: sccache}\n  - {alias: a, path: "+d+"/x, mode: sccache}\n")
	fs := Check(cfg, "/h")
	rank := map[string]int{Error: 0, Warning: 1, Info: 2}
	for i := 1; i < len(fs); i++ {
		if rank[fs[i-1].Severity] > rank[fs[i].Severity] {
			t.Fatalf("not ordered by severity: %+v", fs)
		}
	}
}

func TestInfoLinesCarryNoExcerpt(t *testing.T) {
	d := tmpDir(t)
	cfg := cfgFor(t, "roots:\n  - {alias: a, path: "+d+", mode: sccache}\n")
	for _, f := range Check(cfg, "/h") {
		if f.Severity == Info && f.Excerpt != "" {
			t.Errorf("an info line repeats the source: %+v", f)
		}
	}
}

// The codes are a contract: every code the code can emit is listed, each one is
// described in docs/config.md, and the scenarios above reach most of them.
func TestCodesAreListedAndDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "config.md"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range Codes {
		if seen[c] {
			t.Errorf("code %q is listed twice", c)
		}
		seen[c] = true
		if !strings.Contains(string(doc), "`"+c+"`") {
			t.Errorf("docs/config.md does not describe the code %q", c)
		}
	}
}

func TestEveryFindingCarriesAListedCode(t *testing.T) {
	listed := map[string]bool{}
	for _, c := range Codes {
		listed[c] = true
	}
	d := tmpDir(t)
	os.MkdirAll(filepath.Join(d, "a", "inner"), 0o755)
	os.MkdirAll(filepath.Join(d, ".ssh", "ws"), 0o755)
	docs := []string{
		"", "- a\n", "schema: cubit.config/v0\nroots: []\n", head + "  - {alias: A, path: rel, mode: nope}\n",
		"schema: cubit.config/v0\nscan: {interval: 1h, max_workers: 99}\nroots:\n  - {alias: a, path: /a, mode: cargo-home, name: x, max_files: 1}\n",
	}
	for _, doc := range docs {
		_, fs := Parse([]byte(doc))
		for _, f := range fs {
			if !listed[f.Code] || f.Message == "" || f.Severity == "" {
				t.Errorf("finding %+v from %q", f, doc)
			}
		}
	}
	cfg := cfgFor(t, "scan: {interval: 10s}\nroots:\n  - {alias: one, path: "+d+"/a, mode: workspace-root, max_duration: 20s}\n  - {alias: one, path: "+d+"/a/inner, mode: sccache}\n  - {alias: s, path: "+d+"/.ssh/ws, mode: sccache}\n  - {alias: m, path: "+d+"/nope, mode: sccache}\n")
	for _, f := range Check(cfg, "/h") {
		if !listed[f.Code] || f.Message == "" {
			t.Errorf("finding %+v", f)
		}
	}
}

// The schema's code enum and the code list are one list.
func TestCodesMatchTheValidateSchema(t *testing.T) {
	var sch struct {
		Defs struct {
			Finding struct {
				Properties struct {
					Code struct {
						Enum []string `json:"enum"`
					} `json:"code"`
				} `json:"properties"`
			} `json:"finding"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(schemas.Validate, &sch); err != nil {
		t.Fatal(err)
	}
	want := append([]string(nil), sch.Defs.Finding.Properties.Code.Enum...)
	have := append([]string(nil), Codes...)
	sort.Strings(want)
	sort.Strings(have)
	if !reflect.DeepEqual(want, have) {
		t.Errorf("schema %v\ncode   %v", want, have)
	}
}
