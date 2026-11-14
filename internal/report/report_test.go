package report

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hedronite/cubit/internal/config"
	"github.com/Hedronite/cubit/internal/scan"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs.\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

func example(t *testing.T) *scan.Document {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "scan.json"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := scan.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	// The example is a document as `cubit scan` prints it, so the fields the
	// walk fills in beyond the JSON are filled here too.
	for i := range d.Roots {
		r := &d.Roots[i]
		if r.Partial {
			r.Errors = []string{r.Error}
		}
	}
	return d
}

func TestTableOfTheExampleDocument(t *testing.T) {
	var b bytes.Buffer
	Table(&b, example(t), "cubit.yaml")
	golden(t, "table.golden", b.String())
	out := b.String()
	for _, want := range []string{
		"ROOT", "(1 unlabeled)",
		"warning: example-projects partial: deadline_exceeded; every size is a lower bound",
		"warning: example-projects skipped 2 permission_denied, 14 vanished",
		"warning: example-projects: 1 unmatched_name in names",
		"error:   example-shared-target failed: root_missing; fix: check that the path of root example-shared-target in cubit.yaml exists",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q", want)
		}
	}
	if strings.Contains(out, "/") && strings.Contains(out, "/srv") {
		t.Error("a path reached the table")
	}
}

func TestChildrenAreBiggestFirstThenByName(t *testing.T) {
	d := &scan.Document{Roots: []scan.Root{{
		Alias: "r", Mode: "workspace-root", Success: true, Bytes: 60,
		Workspaces:          []scan.Workspace{{Name: "b", Bytes: 20}, {Name: "a", Bytes: 20}, {Name: "c", Bytes: 30}},
		WorkspacesUnlabeled: 2, WorkspacesUnlabeledBytes: 5,
	}}}
	var b bytes.Buffer
	Table(&b, d, "cubit.yaml")
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	var names []string
	for _, l := range lines[1:] {
		names = append(names, strings.Fields(l)[0])
	}
	names = names[1:]
	if got := strings.Join(names, ","); got != "c,a,b,(2" {
		t.Errorf("order %q", got)
	}
}

func TestNoTrailingSpaces(t *testing.T) {
	var b bytes.Buffer
	Table(&b, example(t), "cubit.yaml")
	for _, l := range strings.Split(b.String(), "\n") {
		if l != strings.TrimRight(l, " ") {
			t.Errorf("trailing space: %q", l)
		}
	}
}

func TestFindingsHouseStyle(t *testing.T) {
	var b bytes.Buffer
	Findings(&b, "cubit.yaml", []config.Finding{
		{Severity: "error", Line: 7, Message: "roots[2].alias duplicates roots[0].alias", Excerpt: "alias: a", Fix: "give each root a distinct alias slug."},
		{Severity: "warning", Message: "no line known"},
	})
	want := "cubit.yaml:7: error: roots[2].alias duplicates roots[0].alias\n  | alias: a\n  fix: give each root a distinct alias slug.\ncubit.yaml: warning: no line known\n"
	if b.String() != want {
		t.Errorf("got\n%q\nwant\n%q", b.String(), want)
	}
}

func TestHuman(t *testing.T) {
	for in, want := range map[int64]string{
		0: "0B", 1023: "1023B", 1024: "1.0K", 1536: "1.5K", 466944: "456K", 10 * 1024: "10K",
		1<<20 - 1: "1.0M", 42_000_000_000: "39G", 1 << 40: "1.0T", 1 << 62: "4.0E",
	} {
		if got := Human(in); got != want {
			t.Errorf("Human(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestSummary(t *testing.T) {
	e := config.Finding{Severity: config.Error}
	w := config.Finding{Severity: config.Warning}
	i := config.Finding{Severity: config.Info}
	for _, c := range []struct {
		fs    []config.Finding
		roots int
		want  string
	}{
		{[]config.Finding{i}, 3, "cubit.yaml: ok, 3 root(s)\n"},
		{[]config.Finding{w, i}, 3, "cubit.yaml: ok, 3 root(s), 1 warning(s)\n"},
		{[]config.Finding{e, e, w}, 3, "cubit.yaml: not ok: 2 error(s), 1 warning(s), 3 root(s)\n"},
		{[]config.Finding{e}, -1, "cubit.yaml: not ok: 1 error(s)\n"},
	} {
		var b bytes.Buffer
		Summary(&b, "cubit.yaml", c.fs, c.roots)
		if b.String() != c.want {
			t.Errorf("got %q, want %q", b.String(), c.want)
		}
	}
}
