package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const declarationBase = "apiVersion: cubit.config/v0\nroots:\n  - alias: a\n    path: /nonexistent/a\n    mode: target-dir\n"

func TestDeclaration(t *testing.T) {
	digest := "sha256:" + strings.Repeat("0123456789abcdef", 4)
	for _, line := range []string{"", "declaration: null\n", "declaration: ~\n", "declaration:\n"} {
		c, fs := Parse([]byte(declarationBase + line))
		if c == nil || c.Declaration != nil {
			t.Errorf("%q: %+v", line, fs)
		}
	}
	for _, line := range []string{"declaration: " + digest + "\n", "declaration: \"" + digest + "\"\n"} {
		c, fs := Parse([]byte(declarationBase + line))
		if c == nil || c.Declaration == nil || *c.Declaration != digest {
			t.Errorf("%q: %+v", line, fs)
		}
	}
	for value, code := range map[string]string{
		`""`: CodeInvalidValue, `"null"`: CodeInvalidValue, "sha256:abc": CodeInvalidValue,
		digest + "0": CodeInvalidValue, "sha256:" + strings.Repeat("A", 64): CodeInvalidValue,
		strings.Repeat("a", 64): CodeInvalidValue, "7": CodeWrongType, "[]": CodeWrongType, "true": CodeWrongType,
	} {
		c, fs := Parse([]byte(declarationBase + "declaration: " + value + "\n"))
		if c != nil || len(fs) != 1 || fs[0].Field != "declaration" || fs[0].Code != code || fs[0].Line != 6 {
			t.Errorf("declaration %s: %+v", value, fs)
		}
	}
}

// A root that really is a refused place is refused even when the entry names it
// through a link, and a linked entry below a root is reported as inside it.
func TestCheckResolvesRefuseEntries(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "real", "tree", "secret"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	check := func(root, entry string) []Finding {
		c, fs := Parse([]byte("apiVersion: cubit.config/v0\nrefuse:\n  extra:\n    - " + entry +
			"\nroots:\n  - alias: a\n    path: " + root + "\n    mode: target-dir\n"))
		if c == nil {
			t.Fatalf("%+v", fs)
		}
		return Check(c, "/nonexistent-home")
	}
	codes := func(fs []Finding) string {
		var s []string
		for _, f := range fs {
			s = append(s, f.Code)
		}
		return strings.Join(s, " ")
	}
	tree := filepath.Join(base, "real", "tree")
	linked := filepath.Join(base, "link", "tree", "secret")
	if got := codes(check(filepath.Join(tree, "secret"), linked)); !strings.Contains(got, CodeRefusedRoot) {
		t.Errorf("root is the entry's real place: %s", got)
	}
	if got := codes(check(tree, linked)); !strings.Contains(got, CodeRefusedInside) {
		t.Errorf("entry really below the root: %s", got)
	}
}
