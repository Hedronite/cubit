package contain

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func symlink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

func refuser(t *testing.T, home string, extra ...string) *Refuser {
	t.Helper()
	r, err := NewRefuser(home, extra, "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func inside(r *Refuser, path string) bool {
	_, bad := r.Inside(Components(path))
	return bad
}

// An entry written through any symlinked spelling refuses the real place, and
// the written spelling still refuses too.
func TestRefuseEntriesResolveSymlinks(t *testing.T) {
	base := mk(t)
	real := filepath.Join(base, "real")
	write(t, filepath.Join(real, "tgt", "secret", "blob"), 1)
	write(t, filepath.Join(real, "tgt", "sibling", "blob"), 1)
	symlink(t, real, filepath.Join(base, "abs-link"))
	symlink(t, "real", filepath.Join(base, "rel-link"))
	symlink(t, "rel-link", filepath.Join(base, "chain"))
	symlink(t, "../real/tgt/secret", filepath.Join(base, "other", "final"))
	symlink(t, filepath.Join(base, "abs-link", "tgt"), filepath.Join(base, "via-link"))

	secret := filepath.Join(real, "tgt", "secret")
	for name, entry := range map[string]string{
		"real":                   secret,
		"absolute link":          filepath.Join(base, "abs-link", "tgt", "secret"),
		"relative link":          filepath.Join(base, "rel-link", "tgt", "secret"),
		"chain of links":         filepath.Join(base, "chain", "tgt", "secret"),
		"the entry is a link":    filepath.Join(base, "other", "final"),
		"link through a link":    filepath.Join(base, "via-link", "secret"),
		"trailing slash":         filepath.Join(base, "abs-link", "tgt", "secret") + "/",
		"upper case on the tail": filepath.Join(base, "abs-link", "tgt", "SECRET"),
	} {
		t.Run(name, func(t *testing.T) {
			r := refuser(t, "/nonexistent-home", entry)
			for _, p := range []string{secret, filepath.Join(secret, "blob"), strings.TrimSuffix(entry, "/")} {
				class, bad := r.Inside(Components(p))
				if !bad || class != "config" {
					t.Errorf("%s: refused=%v class=%q", p, bad, class)
				}
			}
			if inside(r, filepath.Join(real, "tgt", "sibling")) || inside(r, filepath.Join(real, "tgt")) {
				t.Error("a sibling or parent of the entry is refused")
			}
			if got := r.Under(Components(filepath.Join(real, "tgt"))); len(got) != 1 || got[0] != "config" {
				t.Errorf("Under = %v", got)
			}
			if _, _, err := OpenRoot(secret, r); !errors.Is(err, ErrRefused) {
				t.Errorf("OpenRoot: %v", err)
			}
		})
	}
}

// An entry that does not exist yet resolves as far as its ancestors do.
func TestRefuseEntriesResolveAMissingTail(t *testing.T) {
	base := mk(t)
	write(t, filepath.Join(base, "real", "f"), 1)
	symlink(t, filepath.Join(base, "real"), filepath.Join(base, "link"))
	symlink(t, "real/later/deep", filepath.Join(base, "dangling"))
	for entry, want := range map[string]string{
		filepath.Join(base, "link", "not", "yet"): filepath.Join(base, "real", "not", "yet"),
		filepath.Join(base, "dangling"):           filepath.Join(base, "real", "later", "deep"),
		filepath.Join(base, "dangling", "more"):   filepath.Join(base, "real", "later", "deep", "more"),
		filepath.Join(base, "link", "f", "below"): filepath.Join(base, "real", "f", "below"),
	} {
		r := refuser(t, "/nonexistent-home", entry)
		if !inside(r, want) || !inside(r, entry) {
			t.Errorf("entry %s does not refuse %s", entry, want)
		}
	}
}

// A linked home directory: every ~/ entry, built in or configured, globs
// included, refuses the real home too.
func TestRefuseEntriesUnderALinkedHome(t *testing.T) {
	base := mk(t)
	real := filepath.Join(base, "data", "home")
	write(t, filepath.Join(real, "private", "f"), 1)
	write(t, filepath.Join(real, ".cargo", "registry", "f"), 1)
	write(t, filepath.Join(base, "elsewhere", "keys"), 1)
	symlink(t, filepath.Join(base, "elsewhere"), filepath.Join(real, ".gnupg"))
	home := filepath.Join(base, "home-link")
	symlink(t, real, home)
	r := refuser(t, home, "~/private")
	for _, p := range []string{
		filepath.Join(real, ".config", "gh"),
		filepath.Join(real, ".cargo", "credentials.toml"),
		filepath.Join(home, ".cargo", "credentials"),
		filepath.Join(real, "private"),
		filepath.Join(real, "Library", "Safari"),
		filepath.Join(base, "elsewhere", "keys"), // where ~/.gnupg really leads
	} {
		if !inside(r, p) {
			t.Errorf("%s is not refused", p)
		}
	}
	for _, p := range []string{filepath.Join(real, ".cargo", "registry"), filepath.Join(base, "data")} {
		if inside(r, p) {
			t.Errorf("%s is refused", p)
		}
	}
	// A glob below a linked directory.
	write(t, filepath.Join(base, "cargo-real", "x"), 1)
	other := filepath.Join(base, "other-home")
	symlink(t, filepath.Join(base, "cargo-real"), filepath.Join(other, ".cargo"))
	if !inside(refuser(t, other), filepath.Join(base, "cargo-real", "credentials.toml")) {
		t.Error("the credentials glob does not follow the linked directory")
	}
}

// Loops, links to the filesystem root and links out of nowhere never panic,
// hang or widen the list to everything.
func TestRefuseEntriesWithOddLinks(t *testing.T) {
	base := mk(t)
	symlink(t, "b", filepath.Join(base, "a"))
	symlink(t, "a", filepath.Join(base, "b"))
	symlink(t, "/", filepath.Join(base, "to-root"))
	symlink(t, "../../../../../../../../../..", filepath.Join(base, "above-root"))
	r := refuser(t, "/nonexistent-home",
		filepath.Join(base, "a", "x"), filepath.Join(base, "to-root"), filepath.Join(base, "above-root"))
	for _, p := range []string{filepath.Join(base, "a", "x"), filepath.Join(base, "to-root"), filepath.Join(base, "above-root")} {
		if !inside(r, p) {
			t.Errorf("the written entry %s no longer refuses", p)
		}
	}
	if inside(r, filepath.Join(base, "unrelated")) {
		t.Error("an unrelated path is refused")
	}
}

// The system temp directory is a link on some systems; an entry spelled with it
// must refuse the real one.
func TestRefuseEntryUnderTmp(t *testing.T) {
	real, err := filepath.EvalSymlinks("/tmp")
	if err != nil || real == "/tmp" {
		t.Skip("/tmp is not a symlink here")
	}
	d, err := os.MkdirTemp("/tmp", "refuse-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	r := refuser(t, "/nonexistent-home", filepath.Join(d, "secret"))
	if !inside(r, filepath.Join(real, filepath.Base(d), "secret", "blob")) {
		t.Error("an entry under /tmp does not refuse the real directory")
	}
}
