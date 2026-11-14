package textfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/Hedronite/cubit/internal/contain"
)

// The three names Cubit uses in the directory. The temp and lock names start
// with a dot and do not end in .prom, so node_exporter ignores them.
const (
	FileName = "cubit.prom"
	tmpName  = ".cubit.prom.tmp"
	lockName = ".cubit.prom.lock"
)

var euid = os.Geteuid

// Mode is the mode of cubit.prom, whatever the umask.
const Mode fs.FileMode = 0o644

// Errors Open returns, for errors.Is. None of their texts holds a path.
var (
	ErrNotExist    = errors.New("the directory does not exist")
	ErrNotDir      = errors.New("not a directory")
	ErrRefused     = contain.ErrRefused
	ErrNotWritable = errors.New("the directory is not writable")
	ErrLocked      = errors.New("another cubit holds the textfile lock")
	ErrNoLocking   = errors.New("the filesystem does not support file locks")
	// ErrUnsafeDir: the directory is writable by everyone, so anyone could
	// replace cubit.prom with false numbers or hold the lock.
	ErrUnsafeDir = errors.New("the directory is writable by everyone")
	// ErrNotFile: cubit.prom already exists and is a directory.
	ErrNotFile = errors.New("cubit.prom is a directory")
	// ErrForeignFile: cubit.prom already exists and belongs to another user.
	ErrForeignFile = errors.New("cubit.prom belongs to another user")
)

// LockMode is the mode of the lock file: only the owner can take it.
const LockMode fs.FileMode = 0o600

// RefusedError is a directory on the refuse list. Class is "builtin" or
// "config"; the text never names the path.
type RefusedError struct{ Class string }

func (e *RefusedError) Error() string {
	return "the directory is inside a " + e.Class + " refuse-list prefix"
}

// Is makes errors.Is(err, ErrRefused) true.
func (e *RefusedError) Is(target error) bool { return target == ErrRefused }

// WriteError is a failed step of Write. Err is the underlying error, so
// errors.As finds the errno.
type WriteError struct {
	Op  string
	Err error
}

func (e *WriteError) Error() string { return e.Op + ": " + Text(e.Err) }
func (e *WriteError) Unwrap() error { return e.Err }

// Errno names the system error behind err (ENOSPC, EACCES, ...), or "errno N"
// when it has no common name, or "" when there is no errno at all.
func Errno(err error) string {
	var en syscall.Errno
	if !errors.As(err, &en) {
		return ""
	}
	names := map[syscall.Errno]string{
		syscall.ENOSPC: "ENOSPC", syscall.EACCES: "EACCES", syscall.EPERM: "EPERM", syscall.EROFS: "EROFS",
		syscall.EIO: "EIO", syscall.EDQUOT: "EDQUOT", syscall.EXDEV: "EXDEV", syscall.EEXIST: "EEXIST",
		syscall.ENOENT: "ENOENT", syscall.EISDIR: "EISDIR", syscall.ENOTDIR: "ENOTDIR", syscall.EMFILE: "EMFILE",
		syscall.ENFILE: "ENFILE", syscall.EBUSY: "EBUSY", syscall.ENAMETOOLONG: "ENAMETOOLONG", syscall.EFBIG: "EFBIG",
	}
	if n, ok := names[en]; ok {
		return n
	}
	return fmt.Sprintf("errno %d", int(en))
}

// Syscall seams, replaced by tests.
var (
	fsyncFile = func(f *os.File) error { return f.Sync() }
)

// Writer writes cubit.prom into one directory. It holds the single-writer lock
// until Close.
type Writer struct {
	mu   sync.Mutex
	root *os.Root
	lock *os.File
	real string // the resolved directory, for Moved and nothing else

	// Warnings are problems Open found that do not stop it. The caller shows them.
	Warnings []string
}

// refusal turns a refusal from the contain package into this package's own,
// keeping the list that refused: "builtin" or "config".
func refusal(err error) *RefusedError {
	class := "builtin"
	var cr *contain.RefusedError
	if errors.As(err, &cr) {
		class = cr.Class
	}
	return &RefusedError{Class: class}
}

// DirModeError carries the mode of an unsafe directory. It is ErrUnsafeDir.
type DirModeError struct{ Mode fs.FileMode }

func (e *DirModeError) Error() string   { return ErrUnsafeDir.Error() }
func (e *DirModeError) Is(t error) bool { return t == ErrUnsafeDir }

// Open prepares dir for writing. dir may start with ~/; it is made absolute,
// resolved through symlinks once, and refused if the resolved path, or any
// ancestor of it, is on the refuse list. It must already exist: nothing is
// created but the lock file. Open takes the single-writer lock without
// blocking, so a second writer fails at once with ErrLocked.
func Open(dir, home string, ref *contain.Refuser) (*Writer, error) {
	abs, err := absolute(dir, home)
	if err != nil {
		return nil, err
	}
	// Refusal first, so a refused path is never statted, then the plain checks
	// that give the operator a plain answer.
	if class, bad := ref.Inside(contain.Components(abs)); bad {
		return nil, &RefusedError{Class: class}
	}
	// Resolve the path ourselves, with the refuse list in front of every lstat,
	// so a refused name is never statted, whether it is the directory or the
	// target of a link. Then the plain answers: missing, or not a directory.
	resolved, err := contain.ResolvePath(filepath.Clean(abs), ref)
	if err != nil {
		switch {
		case errors.Is(err, contain.ErrRefused):
			return nil, refusal(err)
		case errors.Is(err, fs.ErrNotExist):
			return nil, ErrNotExist
		case errors.Is(err, syscall.ENOTDIR):
			return nil, ErrNotDir // a component on the way is a file
		case errors.Is(err, fs.ErrPermission):
			return nil, ErrNotWritable
		}
		return nil, fmt.Errorf("cannot resolve the directory: %s", Text(err))
	}
	if fi, err := os.Stat(resolved); err != nil || !fi.IsDir() { //nolint:gosec // resolved is symlink-free and was checked against the refuse list
		if err != nil && errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotExist
		}
		return nil, ErrNotDir
	}
	root, real, err := contain.OpenRoot(resolved, ref)
	if err != nil {
		switch {
		case errors.Is(err, contain.ErrRefused):
			return nil, refusal(err)
		case errors.Is(err, fs.ErrPermission):
			return nil, ErrNotWritable
		}
		return nil, fmt.Errorf("cannot open the directory: %s", Text(err))
	}
	if di, err := root.Stat("."); err != nil || !di.IsDir() {
		_ = root.Close()
		return nil, ErrNotDir
	}
	var warnings []string
	di, err := root.Stat(".")
	if err == nil && di.Mode().Perm()&0o002 != 0 {
		_ = root.Close()
		return nil, &DirModeError{Mode: di.Mode().Perm()}
	}
	if err == nil && di.Mode().Perm()&0o020 != 0 {
		warnings = append(warnings, fmt.Sprintf("the directory is group-writable (mode %04o): its group can replace cubit.prom; keep the group to the exporter's user", di.Mode().Perm()))
	}
	// An existing cubit.prom must be ours to replace, and must be a file or a
	// symlink (replaced, never followed), not a directory.
	if fi, lerr := root.Lstat(FileName); lerr == nil {
		if fi.IsDir() {
			_ = root.Close()
			return nil, ErrNotFile
		}
		if uid, ok := ownerUID(fi); ok && uid != euid() {
			_ = root.Close()
			return nil, ErrForeignFile
		}
	}
	// O_NOFOLLOW: the lock must be a file of ours, not a link to another.
	lock, err := root.OpenFile(lockName, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, LockMode)
	if err != nil {
		_ = root.Close()
		if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS) {
			return nil, ErrNotWritable
		}
		return nil, fmt.Errorf("cannot create the lock file: %s", Text(err))
	}
	if fi, err := lock.Stat(); err != nil || !fi.Mode().IsRegular() {
		_ = lock.Close()
		_ = root.Close()
		return nil, errors.New("the lock file is not a regular file")
	}
	if err := flock(lock); err != nil {
		_ = lock.Close()
		_ = root.Close()
		return nil, err
	}
	return &Writer{root: root, lock: lock, real: real, Warnings: warnings}, nil
}

// Moved reports whether the directory Cubit opened is no longer the one its
// path names (replaced, moved, or gone). The open directory is pinned, so
// writes keep going to it, and node_exporter, reading the path, no longer sees
// them. Cubit never re-resolves and never writes by path: it only tells the
// caller, who can log it.
func (w *Writer) Moved() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.root == nil {
		return false
	}
	now, err := os.Stat(w.real)
	if err != nil {
		return true
	}
	pinned, err := w.root.Stat(".")
	if err != nil {
		return true
	}
	return !os.SameFile(now, pinned)
}

func absolute(dir, home string) (string, error) {
	if dir == "" {
		return "", ErrNotExist
	}
	if rest, ok := strings.CutPrefix(dir, "~/"); ok {
		if !filepath.IsAbs(home) {
			return "", errors.New("the directory starts with ~/ but the home directory is unknown")
		}
		dir = filepath.Join(home, rest)
	}
	return filepath.Abs(dir)
}

// Write replaces cubit.prom with data, atomically: a reader sees the old file
// or the new one, never a mixture and never an empty file. On any error the
// temp file is removed and the previous cubit.prom is untouched.
func (w *Writer) Write(data []byte) (err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.root == nil {
		return errors.New("the writer is closed")
	}
	// A temp file left by a crash is stale: only the lock holder writes here.
	if err := w.root.Remove(tmpName); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return &WriteError{"cannot remove a stale temp file", err}
	}
	f, err := w.root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, Mode)
	if err != nil {
		return &WriteError{"cannot create the temp file", err}
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = w.root.Remove(tmpName)
		}
	}()
	if n, werr := f.Write(data); werr != nil || n != len(data) {
		if werr == nil {
			werr = errors.New("short write")
		}
		return &WriteError{"cannot write the temp file", werr}
	}
	// The umask must not decide the mode of a file node_exporter has to read.
	if err = f.Chmod(Mode); err != nil {
		return &WriteError{"cannot set the file mode", err}
	}
	if err = fsyncFile(f); err != nil {
		return &WriteError{"cannot sync the temp file", err}
	}
	if err = f.Close(); err != nil {
		return &WriteError{"cannot close the temp file", err}
	}
	// Rename replaces the directory entry. If cubit.prom is a symlink it is
	// replaced, never followed.
	if err = w.root.Rename(tmpName, FileName); err != nil {
		return &WriteError{"cannot replace " + FileName, err}
	}
	// The rename is durable only once the directory is. Some filesystems refuse
	// fsync on a directory; that is not a failed write.
	if d, derr := w.root.Open("."); derr == nil {
		serr := fsyncFile(d)
		_ = d.Close()
		if serr != nil && !errors.Is(serr, syscall.EINVAL) && !errors.Is(serr, syscall.ENOTSUP) {
			return &WriteError{"cannot sync the directory", serr}
		}
	}
	return nil
}

// Close releases the lock and the directory. The lock file stays: deleting it
// while another process may be opening it would split the lock.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.root == nil {
		return nil
	}
	err := w.lock.Close() // closing the descriptor releases the flock
	if cerr := w.root.Close(); err == nil {
		err = cerr
	}
	w.root, w.lock = nil, nil
	return err
}

// Text is an error reduced to its system cause, with no path: the text of the
// errno inside an *os.PathError or *os.SyscallError, or the error's own text.
func Text(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err.Error()
	}
	var se *os.SyscallError
	if errors.As(err, &se) {
		return se.Err.Error()
	}
	return err.Error()
}
