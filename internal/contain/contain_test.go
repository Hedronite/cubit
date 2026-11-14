package contain

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func repoFile(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The Go list and the fenced block in docs/refuse-list.md must be the same list,
// in the same order, so neither can drift.
func TestBuiltinMatchesDoc(t *testing.T) {
	doc := string(repoFile(t, "docs/refuse-list.md"))
	m := regexp.MustCompile("(?s)```refuse\n(.*?)```").FindStringSubmatch(doc)
	if m == nil {
		t.Fatal("docs/refuse-list.md has no ```refuse block")
	}
	want := strings.Split(strings.TrimSpace(m[1]), "\n")
	if len(want) != len(Builtin) {
		t.Fatalf("doc lists %d entries, code has %d", len(want), len(Builtin))
	}
	for i := range want {
		if want[i] != Builtin[i] {
			t.Errorf("entry %d: doc %q, code %q", i, want[i], Builtin[i])
		}
	}
}

func TestRefuser(t *testing.T) {
	r, err := NewRefuser("/home/u", []string{"~/private", "/srv/extra"}, "/home/u/vault")
	if err != nil {
		t.Fatal(err)
	}
	yes := []string{
		"/home/u/.ssh",
		"/home/u/.SSH",
		"/HOME/U/.ssh",
		"/home/u/.cargo/credentials",
		"/home/u/.cargo/credentials.toml",
		"/home/u/Library/Mobile Documents",
		"/home/u/library/mobile documents",
		"/etc/ssh",
		"/proc",
		"/Proc",
		"/srv/anywhere/.ssh",
		"/srv/anywhere/deep/.aws",
		"/srv/anywhere/.config/gh",
		"/srv/anywhere/Library/Keychains",
		"/home/u/private",
		"/srv/extra",
		"/home/u/vault",
	}
	no := []string{
		"/home/u/.sshfs",
		"/home/u/ssh",
		"/home/u/.cargo",
		"/home/u/.cargo/registry",
		"/home/u/.cargo/cred",
		"/home/other/.cargo/config.toml", // ~/ entries are for this user's home only
		"/srv/proc",
		"/etc",
		"/home/u/private2",
		"/srv/extra2",
		"/srv/anywhere/.config/ghx",
		"/srv/anywhere/config/gh",
	}
	for _, p := range yes {
		if !r.Refused(Components(p)) {
			t.Errorf("%s should be refused", p)
		}
	}
	for _, p := range no {
		if r.Refused(Components(p)) {
			t.Errorf("%s should not be refused", p)
		}
	}
}

func TestInsideReportsClassNotPath(t *testing.T) {
	r, _ := NewRefuser("/home/u", []string{"/srv/extra"}, "")
	for p, want := range map[string]string{
		"/home/u/.ssh/keys/id": "builtin",
		"/srv/extra/a/b":       "config",
		"/srv/a/.docker/x/y/z": "builtin",
	} {
		class, ok := r.Inside(Components(p))
		if !ok || class != want {
			t.Errorf("%s: class %q ok %v, want %q", p, class, ok, want)
		}
	}
	if _, ok := r.Inside(Components("/srv/fine/dir")); ok {
		t.Error("an ordinary path was reported as refused")
	}
}

func TestUnderFindsAnchoredEntriesBelowARoot(t *testing.T) {
	r, _ := NewRefuser("/home/u", []string{"/srv/p/secret"}, "")
	got := r.Under(Components("/home/u"))
	if len(got) != 1 || got[0] != "builtin" {
		t.Errorf("home root: %v", got)
	}
	got = r.Under(Components("/srv/p"))
	if len(got) != 1 || got[0] != "config" {
		t.Errorf("srv root: %v", got)
	}
	if got := r.Under(Components("/srv/other")); len(got) != 0 {
		t.Errorf("unrelated root: %v", got)
	}
}

func TestNewRefuserRejectsBadEntries(t *testing.T) {
	for _, bad := range []string{"relative/path", "~/a/../b", "/"} {
		if _, err := NewRefuser("/home/u", []string{bad}, ""); err == nil {
			t.Errorf("entry %q should be rejected", bad)
		}
	}
	if _, err := NewRefuser("", nil, ""); err == nil {
		t.Error("~/ entries need a home directory")
	}
}

func mk(t *testing.T) (dir string) {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func write(t *testing.T, p string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRootRefusesAndResolves(t *testing.T) {
	base := mk(t)
	r, _ := NewRefuser("/nonexistent-home", nil, "")
	write(t, filepath.Join(base, "ok", "f"), 1)
	write(t, filepath.Join(base, "x", ".ssh", "k"), 1)

	root, real, err := OpenRoot(filepath.Join(base, "ok"), r)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	if real != filepath.Join(base, "ok") {
		t.Errorf("real path %q", real)
	}
	// A symlinked root is allowed, and is resolved.
	if err := os.Symlink(filepath.Join(base, "ok"), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	root, real, err = OpenRoot(filepath.Join(base, "link"), r)
	if err != nil || real != filepath.Join(base, "ok") {
		t.Fatalf("symlinked root: %v %q", err, real)
	}
	root.Close()
	// A root that names a refused path, or links into one, is refused.
	for _, p := range []string{filepath.Join(base, "x", ".ssh"), filepath.Join(base, "x", ".ssh", "k")} {
		if _, _, err := OpenRoot(p, r); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v, want ErrRefused", p, err)
		}
	}
	if err := os.Symlink(filepath.Join(base, "x", ".ssh"), filepath.Join(base, "sneaky")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenRoot(filepath.Join(base, "sneaky"), r); !errors.Is(err, ErrRefused) {
		t.Errorf("symlink into a refused dir: %v", err)
	}
	if _, _, err := OpenRoot(filepath.Join(base, "missing"), r); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if _, _, err := OpenRoot("relative", r); err == nil {
		t.Error("a relative root must be rejected")
	}
}

func TestChild(t *testing.T) {
	base := mk(t)
	write(t, filepath.Join(base, "d", "f"), 1)
	write(t, filepath.Join(base, "file"), 1)
	os.Symlink("d", filepath.Join(base, "linkdir"))
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	c, fi, err := Child(root, "d")
	if err != nil || fi == nil {
		t.Fatalf("real dir: %v", err)
	}
	c.Close()
	if _, _, err := Child(root, "linkdir"); !errors.Is(err, ErrSymlink) || !errors.Is(err, ErrNotDir) {
		t.Errorf("symlink: %v", err)
	}
	if SkipReason(ErrSymlink) != "symlink_escape" {
		t.Error("a symlink where a directory was expected is reported as symlink_escape")
	}
	if _, _, err := Child(root, "file"); !errors.Is(err, ErrNotDir) {
		t.Errorf("file: %v", err)
	}
	if _, _, err := Child(root, "missing"); !errors.Is(err, fs.ErrNotExist) || SkipReason(err) != "vanished" {
		t.Errorf("missing: %v", err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "d/", "/d", "d\x00"} {
		if _, _, err := Child(root, bad); !errors.Is(err, ErrBadName) {
			t.Errorf("name %q: %v, want ErrBadName", bad, err)
		}
	}
}

// A directory replaced between the lstat and the open is caught by the identity check.
func TestChildDetectsASwap(t *testing.T) {
	base := mk(t)
	write(t, filepath.Join(base, "a", "f"), 1)
	root, _ := os.OpenRoot(base)
	defer root.Close()
	old, err := root.Lstat("a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(base, "a"), filepath.Join(base, "a.old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ChildWith(root, "a", old); !errors.Is(err, ErrChanged) {
		t.Fatalf("swapped directory: %v, want ErrChanged", err)
	}
	if SkipReason(ErrChanged) != "vanished" {
		t.Error("a swap is reported as vanished")
	}
}

func TestSkipReasons(t *testing.T) {
	base := mk(t)
	outside := mk(t)
	write(t, filepath.Join(outside, "secret"), 1)
	os.Symlink(filepath.Join(outside, "secret"), filepath.Join(base, "abs"))
	os.Symlink("../"+filepath.Base(outside)+"/secret", filepath.Join(base, "rel"))
	os.Symlink("l2", filepath.Join(base, "l1"))
	os.Symlink("l1", filepath.Join(base, "l2"))
	root, _ := os.OpenRoot(base)
	defer root.Close()

	for _, name := range []string{"abs", "rel"} {
		_, err := root.Open(name)
		if err == nil || SkipReason(err) != "symlink_escape" {
			t.Errorf("%s: %v -> %q, want symlink_escape", name, err, SkipReason(err))
		}
	}
	_, err := root.Stat("l1")
	if err == nil || SkipReason(err) != "symlink_loop" {
		t.Errorf("loop: %v -> %q, want symlink_loop", err, SkipReason(err))
	}
	if SkipReason(fs.ErrPermission) != "permission_denied" || SkipReason(errors.New("boom")) != "io_error" || SkipReason(nil) != "" {
		t.Error("permission and io classification")
	}
}

// os does not export its escape error, so Cubit recognises its text. This pins
// the text: a Go release that changes it fails here instead of turning every
// symlink escape into io_error.
func TestEscapeMessageIsPinned(t *testing.T) {
	base := mk(t)
	os.Symlink("../elsewhere", filepath.Join(base, "up"))
	root, _ := os.OpenRoot(base)
	defer root.Close()
	_, err := root.Open("up")
	if err == nil || !IsEscape(err) {
		t.Fatalf("os.Root did not report an escape the way contain expects: %v", err)
	}
	if IsEscape(errors.New("nope")) || IsEscape(nil) {
		t.Error("IsEscape matched something that is not an escape")
	}
}
