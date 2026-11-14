// Command relhelper writes the release archives and the checksum file, the same
// bytes on every machine. It exists so the release needs no tar or sha256 tool
// whose flags differ between macOS and Linux.
//
//	relhelper archive -o OUT.tar.gz -dir NAME -mtime EPOCH SRC[=NAME_IN_ARCHIVE]:MODE ...
//	relhelper sums -dir DIR
//
// An archive holds one directory NAME with the given files in sorted order,
// owned by uid and gid 0 with no names, every mtime EPOCH, and a gzip stream with
// no file name or time.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "relhelper:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: relhelper archive|sums ...")
	}
	switch args[0] {
	case "archive":
		fs := flag.NewFlagSet("archive", flag.ContinueOnError)
		out := fs.String("o", "", "archive to write")
		dir := fs.String("dir", "", "directory name inside the archive")
		mtime := fs.Int64("mtime", 0, "modification time, Unix seconds")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return writeArchive(*out, *dir, time.Unix(*mtime, 0).UTC(), fs.Args())
	case "sums":
		fs := flag.NewFlagSet("sums", flag.ContinueOnError)
		dir := fs.String("dir", "dist", "directory to sum")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return writeSums(*dir)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

type member struct {
	src, name string
	mode      int64
}

// parseMember reads SRC[=NAME]:MODE. NAME defaults to the base name of SRC.
func parseMember(s string) (member, error) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return member{}, fmt.Errorf("%q: want SRC[=NAME]:MODE", s)
	}
	mode, err := strconv.ParseInt(s[i+1:], 8, 32)
	if err != nil {
		return member{}, fmt.Errorf("%q: bad mode: %w", s, err)
	}
	m := member{src: s[:i], mode: mode}
	if j := strings.IndexByte(m.src, '='); j >= 0 {
		m.src, m.name = m.src[:j], m.src[j+1:]
	} else {
		m.name = filepath.Base(m.src)
	}
	if strings.ContainsAny(m.name, "/\\") || m.name == "" || m.name == "." || m.name == ".." {
		return member{}, fmt.Errorf("%q: the name inside the archive must be one plain component", m.name)
	}
	return m, nil
}

func writeArchive(out, dir string, mtime time.Time, specs []string) error {
	if out == "" || dir == "" || len(specs) == 0 {
		return errors.New("archive needs -o, -dir and at least one file")
	}
	var members []member
	for _, s := range specs {
		m, err := parseMember(s)
		if err != nil {
			return err
		}
		members = append(members, m)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].name < members[j].name })

	f, err := os.Create(out) //nolint:gosec // the release script names the output
	if err != nil {
		return err
	}
	zw, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		_ = f.Close()
		return err
	}
	// The gzip header carries no name and no time, which is what `gzip -n` does.
	zw.Header = gzip.Header{}
	tw := tar.NewWriter(zw)

	hdr := func(name string, mode int64, size int64, typ byte) *tar.Header {
		return &tar.Header{
			Typeflag: typ, Name: name, Mode: mode, Size: size,
			Uid: 0, Gid: 0, Uname: "", Gname: "",
			ModTime: mtime, Format: tar.FormatUSTAR,
		}
	}
	err = tw.WriteHeader(hdr(dir+"/", 0o755, 0, tar.TypeDir))
	for _, m := range members {
		if err != nil {
			break
		}
		err = addFile(tw, hdr, dir, m)
	}
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = zw.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func addFile(tw *tar.Writer, hdr func(string, int64, int64, byte) *tar.Header, dir string, m member) error {
	in, err := os.Open(m.src) //nolint:gosec // the release script names its inputs
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", m.src)
	}
	if err := tw.WriteHeader(hdr(dir+"/"+m.name, m.mode, fi.Size(), tar.TypeReg)); err != nil {
		return err
	}
	_, err = io.Copy(tw, in)
	return err
}

// writeSums writes DIR/SHA256SUMS: "<hash>  <name>" for every regular file in DIR
// except SHA256SUMS itself, sorted by name, in the format sha256sum -c reads.
func writeSums(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type sum struct{ name, line string }
	var sums []sum
	for _, e := range entries {
		if !e.Type().IsRegular() || e.Name() == "SHA256SUMS" {
			continue
		}
		h := sha256.New()
		in, err := os.Open(filepath.Join(dir, e.Name())) //nolint:gosec // listing the release directory
		if err != nil {
			return err
		}
		_, err = io.Copy(h, in)
		_ = in.Close()
		if err != nil {
			return err
		}
		sums = append(sums, sum{e.Name(), hex.EncodeToString(h.Sum(nil)) + "  " + e.Name() + "\n"})
	}
	sort.Slice(sums, func(i, j int) bool { return sums[i].name < sums[j].name })
	var b strings.Builder
	for _, s := range sums {
		b.WriteString(s.line)
	}
	return os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(b.String()), 0o644) //nolint:gosec // a public checksum file
}
