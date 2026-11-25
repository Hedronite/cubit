package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type headerDoc struct {
	Observed    *bool           `json:"observed"`
	ValidFor    json.RawMessage `json:"valid_for"`
	Declaration json.RawMessage `json:"declaration"`
	Roots       []struct {
		Bytes   int64 `json:"bytes"`
		Skipped []struct {
			Reason string `json:"reason"`
			Count  int    `json:"count"`
		} `json:"skipped"`
	} `json:"roots"`
}

func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func scanJSON(t *testing.T, want int, config string, args ...string) headerDoc {
	t.Helper()
	code, out, errs := exec(t, append([]string{"scan", "--json", "--config", config}, args...)...)
	if code != want {
		t.Fatalf("exit %d want %d: %s", code, want, errs)
	}
	var d headerDoc
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if d.Observed == nil {
		t.Fatalf("no observed: %s", out)
	}
	return d
}

// Every scan document carries the header, whether or not a root was usable.
func TestScanDocumentHeader(t *testing.T) {
	d := realTemp(t)
	writeFile(t, filepath.Join(d, "tgt", "f"), []byte("x"))
	digest := "sha256:" + strings.Repeat("b", 64)
	config := func(name, extra string, paths ...string) string {
		body := "apiVersion: cubit.config/v0\n" + extra + "roots:\n"
		for i, p := range paths {
			body += "  - alias: r" + string(rune('0'+i)) + "\n    path: " + p + "\n    mode: target-dir\n"
		}
		writeFile(t, filepath.Join(d, name), []byte(body))
		return filepath.Join(d, name)
	}
	usable, gone := filepath.Join(d, "tgt"), filepath.Join(d, "gone")

	ok := config("ok.yaml", "", usable)
	got := scanJSON(t, 0, ok)
	if !*got.Observed || string(got.ValidFor) != "1800" || string(got.Declaration) != "null" {
		t.Errorf("complete: observed %v valid_for %s declaration %s", *got.Observed, got.ValidFor, got.Declaration)
	}
	got = scanJSON(t, 1, config("some.yaml", "declaration: "+digest+"\n", usable, gone), "--valid-for", "90")
	if !*got.Observed || string(got.ValidFor) != "90" || string(got.Declaration) != `"`+digest+`"` {
		t.Errorf("one of two: observed %v valid_for %s declaration %s", *got.Observed, got.ValidFor, got.Declaration)
	}
	got = scanJSON(t, 2, config("none.yaml", "declaration: "+digest+"\n", gone), "--valid-for=1")
	if *got.Observed || string(got.ValidFor) != "1" || string(got.Declaration) != `"`+digest+`"` {
		t.Errorf("no root usable: observed %v valid_for %s declaration %s", *got.Observed, got.ValidFor, got.Declaration)
	}
	for _, bad := range []string{"0", "-1", "60s", "1.5", ""} {
		code, out, errs := exec(t, "scan", "--json", "--config", ok, "--valid-for="+bad)
		if code != exitUsage || out != "" || !strings.Contains(errs, "valid-for") {
			t.Errorf("--valid-for=%q: exit %d, stdout %q, stderr %q", bad, code, out, errs)
		}
	}
	if code, _, errs := exec(t, "validate", "--config", config("dec.yaml", "declaration: "+digest+"\n", usable)); code != 0 {
		t.Errorf("validate declared: %d %s", code, errs)
	}
	if code, _, errs := exec(t, "validate", "--config", config("bad.yaml", "declaration: sha256:abc\n", usable)); code != exitConfig || !strings.Contains(errs, "declaration") {
		t.Errorf("validate bad declaration: %d %s", code, errs)
	}
}

// End to end: a refuse entry written through a link, and one under a linked
// home, keep the subtree out of the numbers.
func TestScanSkipsARefusedSubtreeSpelledThroughALink(t *testing.T) {
	d := realTemp(t)
	writeFile(t, filepath.Join(d, "real", "tgt", "debug", "build.bin"), make([]byte, 1<<20))
	writeFile(t, filepath.Join(d, "real", "tgt", "debug", "secret", "blob"), make([]byte, 4<<20))
	if err := os.Symlink("real", filepath.Join(d, "link")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(d, "link"))
	for _, entry := range []string{
		filepath.Join(d, "real", "tgt", "debug", "secret"),
		filepath.Join(d, "link", "tgt", "debug", "secret"),
		"~/tgt/debug/secret",
	} {
		cfg := filepath.Join(d, "c.yaml")
		writeFile(t, cfg, []byte("apiVersion: cubit.config/v0\nrefuse:\n  extra:\n    - "+entry+
			"\nroots:\n  - alias: t\n    path: "+filepath.Join(d, "real", "tgt")+"\n    mode: target-dir\n"))
		got := scanJSON(t, 0, cfg)
		r := got.Roots[0]
		if r.Bytes >= 4<<20 || len(r.Skipped) != 1 || r.Skipped[0].Reason != "refused" || r.Skipped[0].Count != 1 {
			t.Errorf("entry %s: bytes %d skipped %+v", entry, r.Bytes, r.Skipped)
		}
	}
}
