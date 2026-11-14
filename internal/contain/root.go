package contain

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Hedronite/cubit/internal/reason"
)

// Errors a caller can test with errors.Is.
var (
	// ErrRefused: the path is on the refuse list. Nothing was opened.
	ErrRefused = errors.New("path is on the refuse list")
	// ErrNotDir: the entry is not a real directory (a symlink or a file).
	ErrNotDir = errors.New("not a directory")
	// ErrSymlink: the entry is a symlink. Cubit resolves one only for a root or
	// a discovered target, so anywhere else it is reported, never followed.
	ErrSymlink = fmt.Errorf("%w: it is a symlink", ErrNotDir)
	// ErrChanged: the directory was replaced between lstat and open.
	ErrChanged = errors.New("directory changed during the walk")
	// ErrBadName: a name that is not a single path component.
	ErrBadName = errors.New("not a single path component")
)

// OpenRoot resolves path (absolute) through symlinks once, refuses it when the
// resolved path, or any ancestor of it, is on the list, and opens it with
// os.OpenRoot. A configured root that is itself a symlink is allowed: the
// operator named it. The returned path is the resolved one. A refusal is a
// *RefusedError, so the caller learns which list refused.
func OpenRoot(path string, r *Refuser) (*os.Root, string, error) {
	if !filepath.IsAbs(path) {
		return nil, "", fmt.Errorf("root path must be absolute: %w", fs.ErrInvalid)
	}
	if class, bad := r.Inside(Components(filepath.Clean(path))); bad {
		return nil, "", &RefusedError{Class: class}
	}
	real, err := ResolvePath(filepath.Clean(path), r)
	if err != nil {
		return nil, "", err
	}
	if class, bad := r.Inside(Components(real)); bad {
		return nil, "", &RefusedError{Class: class}
	}
	root, err := os.OpenRoot(real)
	if err != nil {
		return nil, "", err
	}
	// The path was resolved before it was opened. Compare what was opened with
	// what the resolved path is now, so a swap in between is caught.
	want, err := os.Lstat(real)
	if err != nil {
		_ = root.Close()
		return nil, "", err
	}
	got, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, "", err
	}
	if !os.SameFile(want, got) {
		_ = root.Close()
		return nil, "", ErrChanged
	}
	return root, real, nil
}

// Child opens the directory name inside parent. name must be one component. It
// takes an lstat first and requires a real directory, so a symlink is never
// followed here; after the open it stats "." and requires the same file, so a
// swap in between is reported as ErrChanged. os.Root confines whatever races
// remain to the parent root. The lstat of the entry is returned for its device
// and size.
func Child(parent *os.Root, name string) (*os.Root, fs.FileInfo, error) {
	if !SingleComponent(name) {
		return nil, nil, ErrBadName
	}
	fi, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	child, err := ChildWith(parent, name, fi)
	return child, fi, err
}

// ChildWith is Child for a caller that already holds the entry's lstat, so a
// walk takes one fstatat per entry, not two.
func ChildWith(parent *os.Root, name string, fi fs.FileInfo) (*os.Root, error) {
	if !SingleComponent(name) {
		return nil, ErrBadName
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, ErrSymlink
	}
	if !fi.IsDir() {
		return nil, ErrNotDir
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	now, err := child.Stat(".")
	if err != nil {
		_ = child.Close()
		return nil, err
	}
	if !os.SameFile(fi, now) {
		_ = child.Close()
		return nil, ErrChanged
	}
	return child, nil
}

// SingleComponent reports whether name is one usable path component.
func SingleComponent(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00")
}

// Open opens rel inside root for reading. rel may hold several components; it
// is rejected when empty, absolute or holding NUL, and otherwise left to
// os.Root, which refuses any resolution that leaves the root.
func Open(root *os.Root, rel string) (*os.File, error) {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.IndexByte(rel, 0) >= 0 {
		return nil, ErrBadName
	}
	return root.Open(rel)
}

// SkipReason maps a filesystem error to a closed skip reason of
// cubit.scan/v0: permission_denied, vanished, symlink_loop, symlink_escape or
// io_error.
func SkipReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrRefused):
		return reason.Refused
	case errors.Is(err, ErrSymlink), errors.Is(err, ErrEscape):
		return reason.SymlinkEscape
	case errors.Is(err, ErrLoop):
		return reason.SymlinkLoop
	case errors.Is(err, ErrChanged):
		// A directory swapped between the lstat and the open has unmeasured
		// bytes and may be hostile, so it fails closed: io_error makes the root
		// partial, where vanished would let a complete-looking number through.
		return reason.IOError
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return reason.Vanished
	case errors.Is(err, fs.ErrPermission):
		return reason.PermissionDenied
	case errors.Is(err, syscall.ELOOP):
		return reason.SymlinkLoop
	case IsEscape(err):
		return reason.SymlinkEscape
	default:
		return reason.IOError
	}
}

// escapeMessage is the text of os's unexported errPathEscapes. A test pins it
// so a change in a Go release fails loudly rather than turning an escape into
// io_error.
const escapeMessage = "path escapes from parent"

// IsEscape reports whether err is os.Root's refusal to leave its root.
func IsEscape(err error) bool {
	return err != nil && strings.Contains(err.Error(), escapeMessage)
}
