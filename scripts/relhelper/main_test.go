package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func files(t *testing.T, dir string, mtime time.Time) []string {
	t.Helper()
	var specs []string
	for name, body := range map[string]string{"bin": "binary bytes", "LICENSE-APACHE": "a", "LICENSE-MIT": "m", "README.md": "r"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		// The source files' own times and modes must not reach the archive.
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	specs = append(specs, filepath.Join(dir, "bin")+"=cubit:0755")
	for _, n := range []string{"README.md", "LICENSE-MIT", "LICENSE-APACHE"} {
		specs = append(specs, filepath.Join(dir, n)+":0644")
	}
	return specs
}

// The same inputs give the same bytes whatever the directory, the source mtimes
// and the order the files are named in.
func TestArchiveIsDeterministic(t *testing.T) {
	epoch := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var outs [][]byte
	for i, mt := range []time.Time{time.Now(), time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)} {
		dir := t.TempDir()
		specs := files(t, dir, mt)
		if i == 1 { // a different order on the command line
			specs[0], specs[3] = specs[3], specs[0]
		}
		out := filepath.Join(t.TempDir(), "a.tar.gz")
		if err := writeArchive(out, "cubit_1.0.0_linux_amd64", epoch, specs); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		outs = append(outs, b)
	}
	if !bytes.Equal(outs[0], outs[1]) {
		t.Error("two archives of the same inputs differ")
	}
}

func TestArchiveHoldsOnlyTheNamedFilesOwnedByRoot(t *testing.T) {
	epoch := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := writeArchive(out, "cubit_1.0.0_linux_amd64", epoch, files(t, dir, time.Now())); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if zr.Name != "" || !zr.ModTime.IsZero() || zr.Comment != "" {
		t.Errorf("the gzip header carries a name or time: %+v", zr.Header)
	}
	tr := tar.NewReader(zr)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || !h.ModTime.Equal(epoch) {
			t.Errorf("%s: owner %d/%d %q/%q mtime %v", h.Name, h.Uid, h.Gid, h.Uname, h.Gname, h.ModTime)
		}
		want := int64(0o644)
		switch h.Name {
		case "cubit_1.0.0_linux_amd64/", "cubit_1.0.0_linux_amd64/cubit":
			want = 0o755
		}
		if h.Mode != want {
			t.Errorf("%s: mode %o, want %o", h.Name, h.Mode, want)
		}
	}
	want := "cubit_1.0.0_linux_amd64/ cubit_1.0.0_linux_amd64/LICENSE-APACHE cubit_1.0.0_linux_amd64/LICENSE-MIT cubit_1.0.0_linux_amd64/README.md cubit_1.0.0_linux_amd64/cubit"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("members %q, want sorted %q", got, want)
	}
}

func TestMemberNamesAreOnePlainComponent(t *testing.T) {
	for _, bad := range []string{"x=../evil:0644", "x=a/b:0644", "x=:0644", "x:notoctal", "nomode"} {
		if _, err := parseMember(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if m, err := parseMember("/some/dir/file.txt:0644"); err != nil || m.name != "file.txt" || m.mode != 0o644 {
		t.Errorf("%+v %v", m, err)
	}
}

func TestSumsAreSortedByNameAndCheckable(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"b.tar.gz": "bb", "a": "aa", "c.json": "cc"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Writing twice: the checksum file never sums itself.
	for i := 0; i < 2; i++ {
		if err := writeSums(dir); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("%q", raw)
	}
	sum := sha256.Sum256([]byte("aa"))
	if lines[0] != hex.EncodeToString(sum[:])+"  a" || !strings.HasSuffix(lines[1], "  b.tar.gz") || !strings.HasSuffix(lines[2], "  c.json") {
		t.Errorf("%q", lines)
	}
}
