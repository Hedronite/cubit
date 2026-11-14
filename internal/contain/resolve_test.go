package contain

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resolveTree(t *testing.T) (root *os.Root, base string, ref *Refuser) {
	t.Helper()
	base = mk(t)
	write(t, filepath.Join(base, "dir", "sub", "f"), 1)
	write(t, filepath.Join(base, ".ssh", "key"), 1)
	write(t, filepath.Join(base, "shared", "x"), 1)
	out := mk(t)
	write(t, filepath.Join(out, "secret"), 1)
	for name, target := range map[string]string{
		"toshared":  "shared",
		"chain1":    "chain2",
		"chain2":    "toshared",
		"viassh":    ".ssh",
		"hop1":      "hop2",
		"hop2":      "viassh", // a chain that ends in a refused directory
		"up":        "..",
		"abs":       out,
		"outrel":    "../" + filepath.Base(out),
		"dir/back":  "../shared",
		"self":      "self",
		"a":         "b",
		"b":         "a",
		"dirlink":   "dir/sub",
		"dir/upsub": "sub/../../shared",
	} {
		if err := os.Symlink(target, filepath.Join(base, name)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 9; i++ { // a legitimate 9-hop chain: more than os.Root allows
		next := fmt.Sprintf("n%d", i+1)
		if i == 9 {
			next = "shared"
		}
		os.Symlink(next, filepath.Join(base, fmt.Sprintf("n%d", i)))
	}
	r, err := NewRefuser("/nonexistent-home", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	root, err = os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, base, r
}

func TestResolve(t *testing.T) {
	root, base, ref := resolveTree(t)
	comps := Components(base)
	cases := []struct {
		rel  string
		want string
		err  error
	}{
		{"dir/sub/f", "dir/sub/f", nil},
		{"toshared", "shared", nil},
		{"chain1", "shared", nil},
		{"dirlink", "dir/sub", nil},
		{"dirlink/f", "dir/sub/f", nil},
		{"dir/back", "shared", nil},
		{"dir/upsub", "shared", nil},
		{"n1", "shared", nil}, // 9 hops resolves: Cubit's budget is 40, not os.Root's 8
		{"dir/../shared", "shared", nil},
		{"./dir//sub", "dir/sub", nil},
		{"up", "", ErrEscape},
		{"abs", "", ErrEscape},
		{"outrel", "", ErrEscape},
		{"../x", "", ErrEscape},
		{"self", "", ErrLoop},
		{"a", "", ErrLoop},
		{".ssh", "", ErrRefused},
		{".ssh/key", "", ErrRefused},
		{"viassh", "", ErrRefused},
		{"hop1", "", ErrRefused}, // the refuse list is checked on every hop, not only the first
		{"missing", "", fs.ErrNotExist},
	}
	for _, c := range cases {
		got, err := Resolve(root, comps, c.rel, ref)
		if c.err == nil && err != nil || c.err != nil && !errors.Is(err, c.err) {
			t.Errorf("%q: %v, want %v", c.rel, err, c.err)
			continue
		}
		if c.err == nil && got != c.want {
			t.Errorf("%q resolved to %q, want %q", c.rel, got, c.want)
		}
	}
	if got, err := Resolve(root, comps, "", ref); err != nil || got != "" {
		t.Errorf("the root itself: %q %v", got, err)
	}
	if _, err := Resolve(root, comps, "a\x00b", ref); !errors.Is(err, ErrBadName) {
		t.Errorf("NUL: %v", err)
	}
	for err, want := range map[error]string{ErrEscape: "symlink_escape", ErrLoop: "symlink_loop", ErrRefused: "refused"} {
		if SkipReason(err) != want {
			t.Errorf("%v -> %q", err, SkipReason(err))
		}
	}
}

// A refused directory is never statted, even as the last hop of a chain: a
// chmod-000 .ssh would give EACCES on a stat, and Resolve must say refused.
func TestResolveRefusesBeforeItStats(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	root, base, ref := resolveTree(t)
	if err := os.Chmod(filepath.Join(base, ".ssh"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(base, ".ssh"), 0o755) })
	for _, rel := range []string{".ssh", "viassh", "hop1", ".ssh/key"} {
		if _, err := Resolve(root, Components(base), rel, ref); !errors.Is(err, ErrRefused) {
			t.Errorf("%q: %v, want ErrRefused (not a permission error)", rel, err)
		}
	}
}

// FuzzResolve: whatever the input, Resolve returns a clean in-root path with
// nothing refused on it, or a typed error. The oracle is the OS resolver.
func FuzzResolve(f *testing.F) {
	base, err := filepath.EvalSymlinks(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	for p, n := range map[string]int{"dir/sub/f": 1, ".ssh/key": 1, "shared/x": 1} {
		full := filepath.Join(base, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, make([]byte, n), 0o644)
	}
	out, _ := filepath.EvalSymlinks(f.TempDir())
	os.WriteFile(filepath.Join(out, "secret"), []byte("canary"), 0o644)
	for name, target := range map[string]string{
		"toshared": "shared", "chain1": "chain2", "chain2": "toshared", "viassh": ".ssh", "hop1": "hop2",
		"hop2": "viassh", "up": "..", "abs": out, "outrel": "../" + filepath.Base(out), "self": "self",
		"a": "b", "b": "a", "dirlink": "dir/sub", "dir/upsub": "sub/../../shared",
	} {
		os.Symlink(target, filepath.Join(base, name))
	}
	ref, err := NewRefuser("/nonexistent-home", nil, "")
	if err != nil {
		f.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { root.Close() })
	comps := Components(base)
	for _, s := range []string{"toshared", "chain1", "viassh", "hop1", ".ssh", "up", "abs", "self", "a", "dir/upsub", "dirlink/f", "dir/../..", "../", "./x//y", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		got, err := Resolve(root, comps, rel, ref)
		if err != nil {
			return
		}
		for _, c := range strings.Split(got, "/") {
			if got != "" && (c == "" || c == "." || c == "..") {
				t.Fatalf("Resolve(%q) = %q holds an unclean component", rel, got)
			}
		}
		if _, bad := ref.Inside(append(append([]string(nil), comps...), Components(got)...)); bad {
			t.Fatalf("Resolve(%q) = %q returned a refused path", rel, got)
		}
		real, err := filepath.EvalSymlinks(filepath.Join(base, filepath.FromSlash(got)))
		if err == nil && real != base && !strings.HasPrefix(real, base+string(filepath.Separator)) {
			t.Fatalf("Resolve(%q) = %q resolves to %q, outside the root", rel, got, real)
		}
		if b, err := root.ReadFile(got); err == nil && string(b) == "canary" {
			t.Fatalf("Resolve(%q) = %q reached the canary outside the root", rel, got)
		}
	})
}

// FuzzRefuse: matching is per whole component. A name never matches an entry it
// merely starts with, and an any-depth entry matches wherever it sits.
func FuzzRefuse(f *testing.F) {
	r, err := NewRefuser("/home/user", nil, "")
	if err != nil {
		f.Fatal(err)
	}
	for _, s := range []string{".ssh", ".sshfs", ".SSH", "a/.aws", "credentials.toml", "x\xffy", "Library/Keychains", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		comps := Components("/srv/work/" + rel)
		got := r.Refused(comps) // must never panic
		last := ""
		if len(comps) > 0 {
			last = comps[len(comps)-1]
		}
		if last == ".ssh" && !got {
			t.Fatalf("%q: an any-depth entry must match", rel)
		}
		if strings.HasPrefix(last, ".ssh") && last != ".ssh" && got && !strings.EqualFold(last, ".ssh") {
			// only credentials* is a prefix glob; nothing else may match a longer name
			if !strings.HasPrefix(strings.ToLower(strings.Join(comps[max(0, len(comps)-2):], "/")), ".cargo/credentials") {
				t.Fatalf("%q: matched a name that only starts with an entry", rel)
			}
		}
	})
}

// ResolvePath is EvalSymlinks with the refuse list in front of every stat.
func TestResolvePath(t *testing.T) {
	base := mk(t)
	write(t, filepath.Join(base, "real", "sub", "f"), 1)
	write(t, filepath.Join(base, "x", ".ssh", "key"), 1)
	for name, target := range map[string]string{
		"link":     "real",
		"chain":    "link",
		"rel":      "real/sub/../sub",
		"tossh":    "x/.ssh",
		"viatossh": "tossh",
		"dangling": "x/.ssh/missing", // a refused name that does not exist
		"loop":     "loop",
		"abs":      filepath.Join(base, "real"),
	} {
		if err := os.Symlink(target, filepath.Join(base, name)); err != nil {
			t.Fatal(err)
		}
	}
	ref, _ := NewRefuser("/nonexistent-home", nil, "")
	for _, name := range []string{"real", "link", "chain", "rel", "real/sub", "abs"} {
		got, err := ResolvePath(filepath.Join(base, name), ref)
		want, werr := filepath.EvalSymlinks(filepath.Join(base, name))
		if err != nil || werr != nil || got != want {
			t.Errorf("%s: %q %v, EvalSymlinks says %q %v", name, got, err, want, werr)
		}
	}
	for _, name := range []string{"tossh", "viatossh", "dangling", "x/.ssh"} {
		_, err := ResolvePath(filepath.Join(base, name), ref)
		var re *RefusedError
		if !errors.Is(err, ErrRefused) || !errors.As(err, &re) || re.Class != "builtin" {
			t.Errorf("%s: %v, want a refusal", name, err)
		}
		if strings.Contains(err.Error(), ".ssh") {
			t.Errorf("the refusal names a refused place: %q", err)
		}
	}
	if _, err := ResolvePath(filepath.Join(base, "loop"), ref); !errors.Is(err, ErrLoop) {
		t.Errorf("loop: %v", err)
	}
	if _, err := ResolvePath(filepath.Join(base, "nope"), ref); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if _, err := ResolvePath("relative", ref); err == nil {
		t.Error("a relative path must be rejected")
	}
}

// The refused target is never statted: with no search permission on its parent a
// followed stat would fail with a permission error, and ResolvePath must say
// refused instead.
func TestResolvePathNeverStatsARefusedTarget(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	base := mk(t)
	write(t, filepath.Join(base, "x", ".ssh", "key"), 1)
	if err := os.Symlink(filepath.Join(base, "x", ".ssh"), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(base, "x"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(base, "x"), 0o755) })
	ref, _ := NewRefuser("/nonexistent-home", nil, "")
	if _, err := ResolvePath(filepath.Join(base, "link"), ref); !errors.Is(err, ErrRefused) {
		t.Errorf("%v, want a refusal and not a permission error", err)
	}
}
