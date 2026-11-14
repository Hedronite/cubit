package contain

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzContain pins the security property: whatever path is asked for, and
// whatever symlinks the tree holds, a successful open never reaches anything
// outside the root. Two independent oracles judge it: identity against known
// outside objects, and filepath.EvalSymlinks (which resolves the way the OS
// does) placing the result outside the root.
func FuzzContain(f *testing.F) {
	for _, s := range []string{
		"f", "d/g", "d", "abs", "rel", "up", "up/outside/secret", "d/../../outside/secret",
		"l1", "l2/x", "d/../f", "d/up2/secret", "./f", "d//g", "dirlink/g", "dirlink/../f",
		"../outside/secret", "/etc/passwd", "d/\x00", "..", ".", "", "d/./g/../g",
	} {
		f.Add(s)
	}

	base, err := filepath.EvalSymlinks(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	rootDir := filepath.Join(base, "root")
	outDir := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(rootDir, "d"), outDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			f.Fatal(err)
		}
	}
	secret := filepath.Join(outDir, "secret")
	for p, n := range map[string]int{
		filepath.Join(rootDir, "f"): 1, filepath.Join(rootDir, "d", "g"): 1,
	} {
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			f.Fatal(err)
		}
	}
	if err := os.WriteFile(secret, []byte("canary-outside-every-root"), 0o644); err != nil {
		f.Fatal(err)
	}
	links := map[string]string{
		"abs":     secret,
		"rel":     "../outside/secret",
		"up":      "..",
		"l1":      "l2",
		"l2":      "l1",
		"dirlink": "d",
		"d/up2":   "../../outside",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(rootDir, name)); err != nil {
			f.Fatal(err)
		}
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { root.Close() })

	var outside []os.FileInfo
	for _, p := range []string{secret, outDir, base} {
		fi, err := os.Stat(p)
		if err != nil {
			f.Fatal(err)
		}
		outside = append(outside, fi)
	}
	inside := func(p string) bool { return p == rootDir || strings.HasPrefix(p, rootDir+string(filepath.Separator)) }

	f.Fuzz(func(t *testing.T, rel string) {
		if file, err := Open(root, rel); err == nil {
			defer file.Close()
			if b, _ := io.ReadAll(file); string(b) == "canary-outside-every-root" {
				t.Fatalf("Open(%q) read the canary that lives outside the root", rel)
			}
			fi, err := file.Stat()
			if err != nil {
				return
			}
			for _, o := range outside {
				if os.SameFile(fi, o) {
					t.Fatalf("Open(%q) reached an object outside the root", rel)
				}
			}
			if real, err := filepath.EvalSymlinks(rootDir + "/" + rel); err == nil && !inside(real) {
				t.Fatalf("Open(%q) succeeded but the path resolves to %q, outside the root", rel, real)
			}
		}
		if SingleComponent(rel) {
			if c, _, err := Child(root, rel); err == nil {
				defer c.Close()
				fi, err := c.Stat(".")
				if err != nil {
					return
				}
				for _, o := range outside {
					if os.SameFile(fi, o) {
						t.Fatalf("Child(%q) reached an object outside the root", rel)
					}
				}
			}
		}
	})
}
