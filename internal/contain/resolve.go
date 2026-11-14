package contain

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
)

// Typed results of Resolve. They are never recognised by message.
var (
	// ErrEscape: the path leaves the root, by "..", or by an absolute symlink.
	ErrEscape = errors.New("path leaves the root")
	// ErrLoop: more symlink hops than the budget allows.
	ErrLoop = errors.New("too many symlinks")
)

// MaxHops is how many symlinks Resolve follows before it calls a path a loop.
const MaxHops = 40

// Resolve follows rel (slash separated, relative to top) through every symlink
// inside the root and returns the real path, with no symlink, "." or ".."
// left in it. Unlike os.Root, which follows links silently, Resolve checks the
// refuse list on every component of every hop, so a chain of links cannot reach
// a refused directory that no name on the way mentions. A link that leaves the
// root, or is absolute, is ErrEscape; more than MaxHops is ErrLoop; a refused
// step is ErrRefused. Nothing refused is ever statted.
//
// topComps are the absolute components of top, for the refuse check. The
// result is "" when the path resolves to the root itself.
func Resolve(top *os.Root, topComps []string, rel string, r *Refuser) (string, error) {
	if strings.IndexByte(rel, 0) >= 0 {
		return "", ErrBadName
	}
	pending := strings.Split(rel, "/")
	resolved := make([]string, 0, 8)
	abs := append(make([]string, 0, len(topComps)+8), topComps...)
	hops := 0
	for len(pending) > 0 {
		c := pending[0]
		pending = pending[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return "", ErrEscape
			}
			resolved = resolved[:len(resolved)-1]
			abs = abs[:len(abs)-1]
			continue
		}
		resolved = append(resolved, c)
		abs = append(abs, c)
		if r.Refused(abs) {
			return "", ErrRefused
		}
		p := strings.Join(resolved, "/")
		fi, err := top.Lstat(p)
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		if hops++; hops > MaxHops {
			return "", ErrLoop
		}
		link, err := top.Readlink(p)
		if err != nil {
			return "", err
		}
		if path.IsAbs(link) || strings.IndexByte(link, 0) >= 0 {
			return "", ErrEscape
		}
		resolved = resolved[:len(resolved)-1]
		abs = abs[:len(abs)-1]
		pending = append(strings.Split(link, "/"), pending...)
	}
	return strings.Join(resolved, "/"), nil
}

// ResolvePath is filepath.EvalSymlinks for an absolute path, with the refuse
// list checked on every component before that component is statted. EvalSymlinks
// lstats each step of a link's target, so a link into a refused prefix would
// have the refused name statted before any check could turn it away. Here the
// matcher sees each component first. A link that loops, or exceeds MaxHops, is
// ErrLoop; a refused step is ErrRefused. The result has no symlink in it.
func ResolvePath(abs string, r *Refuser) (string, error) {
	if !strings.HasPrefix(abs, "/") || strings.IndexByte(abs, 0) >= 0 {
		return "", ErrBadName
	}
	pending := splitComps(abs)
	var resolved []string
	hops := 0
	for len(pending) > 0 {
		c := pending[0]
		pending = pending[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
			continue
		}
		resolved = append(resolved, c)
		if class, bad := r.Inside(resolved); bad {
			return "", &RefusedError{Class: class}
		}
		p := "/" + strings.Join(resolved, "/")
		fi, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		if hops++; hops > MaxHops {
			return "", ErrLoop
		}
		link, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		resolved = resolved[:len(resolved)-1]
		if strings.HasPrefix(link, "/") {
			resolved = nil // an absolute link starts again from the root
		}
		pending = append(splitComps(link), pending...)
	}
	return "/" + strings.Join(resolved, "/"), nil
}

func splitComps(p string) []string { return strings.Split(p, "/") }

// RefusedError is a refusal that knows which list refused: "builtin" or
// "config". It is ErrRefused to errors.Is, and its text names no path.
type RefusedError struct{ Class string }

func (e *RefusedError) Error() string { return ErrRefused.Error() + " (" + e.Class + ")" }

// Is makes errors.Is(err, ErrRefused) true.
func (e *RefusedError) Is(t error) bool { return t == ErrRefused }
