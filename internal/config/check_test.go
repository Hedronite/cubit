package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cfgFor(t *testing.T, roots string) *Config {
	t.Helper()
	cfg, fs := Parse([]byte("schema: cubit.config/v0\n" + roots))
	if cfg == nil {
		t.Fatalf("%+v", fs)
	}
	return cfg
}

func has(fs []Finding, sev, sub string) bool {
	for _, f := range fs {
		if f.Severity == sev && strings.Contains(f.Message, sub) {
			return true
		}
	}
	return false
}

func tmpDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestExpandPath(t *testing.T) {
	for in, want := range map[string]string{"/a/b/": "/a/b", "~/x/y": "/home/user/x/y", "//a///b": "/a/b"} {
		if got, err := ExpandPath(in, "/home/user"); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"/a/../b", "/a/./b", "~/../x", "rel/path", ""} {
		if _, err := ExpandPath(in, "/home/user"); err == nil {
			t.Errorf("%q should be rejected", in)
		}
	}
	if _, err := ExpandPath("~/x", ""); err == nil {
		t.Error("~/ needs a home")
	}
}

func TestCheckReportsEveryProblemAtOnce(t *testing.T) {
	d := tmpDir(t)
	os.MkdirAll(filepath.Join(d, "a", "inner"), 0o755)
	os.MkdirAll(filepath.Join(d, "b"), 0o755)
	os.MkdirAll(filepath.Join(d, ".ssh", "ws"), 0o755)
	os.Symlink(filepath.Join(d, "b"), filepath.Join(d, "blink"))
	cfg := cfgFor(t, `scan:
  interval: 10s
roots:
  - {alias: one, path: `+d+`/a, mode: workspace-root}
  - {alias: one, path: `+d+`/b, mode: target-dir, max_duration: 20s}
  - {alias: inner, path: `+d+`/a/inner, mode: sccache}
  - {alias: link, path: `+d+`/blink, mode: cargo-home}
  - {alias: secret, path: `+d+`/.ssh/ws, mode: workspace-root}
  - {alias: tilde, path: ~/x/../y, mode: sccache}
`)
	fs := Check(cfg, "/home/user")
	for _, want := range []struct{ sev, sub string }{
		{Error, "scan.interval '10s' is below the 30s floor"},
		{Error, "roots[1].alias 'one' is already used by roots[0]"},
		{Error, "max_duration '20s' is not under scan.interval"},
		{Error, "lies inside"},
		{Error, "resolves to the same directory"},
		{Error, "inside a builtin refuse-list prefix"},
	} {
		if !has(fs, want.sev, want.sub) {
			t.Errorf("missing %s %q in %+v", want.sev, want.sub, fs)
		}
	}
	if !HasErrors(fs) {
		t.Fatal("no errors")
	}
	// A refused root's message names the class, never the path.
	for _, f := range fs {
		if strings.Contains(f.Message, ".ssh") {
			t.Errorf("a finding names a refused path: %q", f.Message)
		}
	}
}

func TestCheckLinesPointAtTheRoot(t *testing.T) {
	d := tmpDir(t)
	cfg := cfgFor(t, "roots:\n  - {alias: a, path: "+d+", mode: sccache}\n  - {alias: a, path: "+d+"/x, mode: sccache}\n")
	for _, f := range Check(cfg, "/h") {
		if strings.Contains(f.Message, "already used") && f.Line != 4 {
			t.Errorf("duplicate alias reported at line %d, want 4: %+v", f.Line, f)
		}
	}
}

func TestCheckInfoAndWarnings(t *testing.T) {
	d := tmpDir(t)
	os.MkdirAll(filepath.Join(d, "tgt", "debug"), 0o755)
	os.MkdirAll(filepath.Join(d, "home", ".ssh"), 0o755)
	cfg := cfgFor(t, `roots:
  - {alias: looks-like-target, path: `+d+`/tgt, mode: workspace-root}
  - {alias: missing, path: `+d+`/nope, mode: sccache}
  - {alias: homeish, path: `+d+`/home, mode: workspace-root}
`)
	fs := Check(cfg, d+"/home") // home is the "homeish" root, so ~/.ssh lies inside it
	if HasErrors(fs) {
		t.Fatalf("%+v", fs)
	}
	if !has(fs, Warning, "looks like one target dir") || !has(fs, Warning, "root_missing") {
		t.Errorf("warnings: %+v", fs)
	}
	if !has(fs, Info, `builtin refuse prefix applies under this root`) {
		t.Errorf("a refused prefix inside a root is legal, reported as info: %+v", fs)
	}
	if !has(fs, Info, "mode workspace-root") {
		t.Errorf("each root's mode is echoed: %+v", fs)
	}
}

func TestCheckNamesDuplicateSlug(t *testing.T) {
	d := tmpDir(t)
	cfg := cfgFor(t, "roots:\n  - alias: a\n    path: "+d+"\n    mode: workspace-root\n    names:\n      x/one: same\n      x/two: same\n      Foo: bar\n      bar: other\n")
	if fs := Check(cfg, "/h"); !has(fs, Error, "both declare the slug") || !has(fs, Error, "equals the derived label") {
		t.Errorf("%+v", fs)
	}
}

func TestRefuseVaultAndExtraAreChecked(t *testing.T) {
	d := tmpDir(t)
	os.MkdirAll(filepath.Join(d, "vault", "ws"), 0o755)
	cfg := cfgFor(t, "refuse:\n  vault: "+d+"/vault\nroots:\n  - {alias: a, path: "+d+"/vault/ws, mode: sccache}\n")
	if fs := Check(cfg, "/h"); !has(fs, Error, "inside a config refuse-list prefix") {
		t.Errorf("%+v", fs)
	}
}

func TestLabel(t *testing.T) {
	cases := map[string]string{
		"app":               "app",
		"App/Core":          "app/core",
		"my project/x":      "my_project/x",
		"café":              "caf__", // two UTF-8 bytes, two underscores
		"a b/a_b":           "a_b/a_b",
		"weird\xff\x00name": "weird__name",
		"UPPER.case-1_2":    "upper.case-1_2",
	}
	for in, want := range cases {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a/", 40)
	if got := Label(long); len(got) > 64 || strings.HasSuffix(got, "/") {
		t.Errorf("long: %q", got)
	}
	// Suffixed stays within 64 bytes, is stable, and differs by path.
	s1, s2 := Suffixed(strings.Repeat("x", 64), "p/one", 8), Suffixed(strings.Repeat("x", 64), "p/two", 8)
	if len(s1) != 64 || s1 == s2 || s1 != Suffixed(strings.Repeat("x", 64), "p/one", 8) {
		t.Errorf("suffixes: %q %q", s1, s2)
	}
}

func FuzzLabel(f *testing.F) {
	for _, s := range []string{"app", "a/b/c", "My Proj/ü", "\xff", strings.Repeat("a/", 50), "a//b", "..", "x/./y"} {
		f.Add(s)
	}
	pat := regexpWorkspace()
	f.Fuzz(func(t *testing.T, rel string) {
		got := Label(rel)
		if len(got) > MaxLabel {
			t.Fatalf("%q is %d bytes", got, len(got))
		}
		if strings.HasPrefix(got, "/") || strings.HasSuffix(got, "/") && got != "" {
			// a leading or trailing slash only comes from a malformed rel path
			if !strings.HasPrefix(rel, "/") && !strings.HasSuffix(rel, "/") && !strings.Contains(rel, "//") {
				t.Fatalf("Label(%q) = %q", rel, got)
			}
		}
		clean := !strings.HasPrefix(rel, "/") && !strings.HasSuffix(rel, "/") && !strings.Contains(rel, "//") && rel != ""
		for _, c := range strings.Split(rel, "/") {
			if c == "." || c == ".." {
				clean = false
			}
		}
		if clean {
			if !pat.MatchString(got) {
				t.Fatalf("Label(%q) = %q does not match the schema pattern", rel, got)
			}
			if sfx := Suffixed(got, rel, 8); len(sfx) > MaxLabel || !pat.MatchString(sfx) {
				t.Fatalf("Suffixed(%q) = %q", got, sfx)
			}
		}
	})
}
