package textfile

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Hedronite/cubit/internal/contain"
)

func refuser(t *testing.T, extra ...string) *contain.Refuser {
	t.Helper()
	r, err := contain.NewRefuser("/nonexistent-home", extra, "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func tmpdir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func open(t *testing.T, dir string) *Writer {
	t.Helper()
	w, err := Open(dir, "/nonexistent-home", refuser(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func TestWriteReplacesTheFileAndLeavesOnlyTheTwoKnownNames(t *testing.T) {
	d := tmpdir(t)
	w := open(t, d)
	for _, body := range []string{"first\n", "second, longer than the first\n", "x\n"} {
		if err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(d, FileName))
		if err != nil || string(got) != body {
			t.Fatalf("read %q %v, want %q", got, err, body)
		}
	}
	if got := strings.Join(names(t, d), ","); got != ".cubit.prom.lock,cubit.prom" {
		t.Errorf("directory holds %s", got)
	}
}

func TestOpenCreatesNothingButTheLockFile(t *testing.T) {
	d := tmpdir(t)
	open(t, d)
	if got := strings.Join(names(t, d), ","); got != ".cubit.prom.lock" {
		t.Errorf("%s", got)
	}
}

// The mode is 0644 whatever the umask: a cron daemon often runs with 077, and
// node_exporter runs as another user.
func TestModeIsNotTheUmasks(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	d := tmpdir(t)
	w := open(t, d)
	if err := w.Write([]byte("m\n")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(d, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v, want 0644", fi.Mode().Perm())
	}
}

// A reader never sees a partial or empty file while a writer replaces it.
func TestReadersNeverSeeAPartialFile(t *testing.T) {
	d := tmpdir(t)
	w := open(t, d)
	a := bytes.Repeat([]byte("a"), 300_000)
	b := bytes.Repeat([]byte("b"), 450_000)
	if err := w.Write(a); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, err := os.ReadFile(filepath.Join(d, FileName))
				if err != nil {
					t.Errorf("read: %v", err)
					return
				}
				if !bytes.Equal(got, a) && !bytes.Equal(got, b) {
					t.Errorf("a reader saw %d bytes that are neither version", len(got))
					return
				}
			}
		}()
	}
	for i := 0; i < 150; i++ {
		body := a
		if i%2 == 0 {
			body = b
		}
		if err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestAStaleTempFileIsRemovedAndNeverRead(t *testing.T) {
	d := tmpdir(t)
	if err := os.WriteFile(filepath.Join(d, tmpName), []byte("a crash left this"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, FileName), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := open(t, d)
	if err := w.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(t, d), ","); got != ".cubit.prom.lock,cubit.prom" {
		t.Errorf("%s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(d, FileName)); string(b) != "new\n" {
		t.Errorf("%q", b)
	}
	if strings.HasSuffix(tmpName, ".prom") || strings.HasSuffix(lockName, ".prom") {
		t.Error("node_exporter reads *.prom; the temp and lock names must not end in it")
	}
}

// A failed write leaves the previous file whole and no temp file behind.
func TestAFailedWriteKeepsThePreviousFile(t *testing.T) {
	d := tmpdir(t)
	w := open(t, d)
	if err := w.Write([]byte("good\n")); err != nil {
		t.Fatal(err)
	}
	for name, hook := range map[string]func(){
		"fsync of the file": func() {
			fsyncFile = func(f *os.File) error { return syscall.ENOSPC }
		},
	} {
		old := fsyncFile
		hook()
		err := w.Write([]byte("bad\n"))
		fsyncFile = old
		var we *WriteError
		if !errors.As(err, &we) || Errno(err) != "ENOSPC" {
			t.Errorf("%s: %v (errno %q)", name, err, Errno(err))
		}
		if b, _ := os.ReadFile(filepath.Join(d, FileName)); string(b) != "good\n" {
			t.Errorf("%s: the previous file changed: %q", name, b)
		}
		if got := strings.Join(names(t, d), ","); got != ".cubit.prom.lock,cubit.prom" {
			t.Errorf("%s: leftovers: %s", name, got)
		}
	}
	// And the Writer is not poisoned: the next write works.
	if err := w.Write([]byte("again\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(d, FileName)); string(b) != "again\n" {
		t.Errorf("%q", b)
	}
}

func TestBothTheFileAndTheDirectoryAreSynced(t *testing.T) {
	d := tmpdir(t)
	w := open(t, d)
	var n int
	var sawDir bool
	old := fsyncFile
	fsyncFile = func(f *os.File) error {
		n++
		if fi, err := f.Stat(); err == nil && fi.IsDir() {
			sawDir = true
		}
		return old(f)
	}
	defer func() { fsyncFile = old }()
	if err := w.Write([]byte("s\n")); err != nil {
		t.Fatal(err)
	}
	if n != 2 || !sawDir {
		t.Errorf("fsync calls %d, directory synced %v; want the file and then the directory", n, sawDir)
	}
}

// cubit.prom as a symlink is replaced, never followed.
func TestASymlinkedFileIsReplacedNotFollowed(t *testing.T) {
	d, other := tmpdir(t), tmpdir(t)
	victim := filepath.Join(other, "victim")
	if err := os.WriteFile(victim, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(d, FileName)); err != nil {
		t.Fatal(err)
	}
	w := open(t, d)
	if err := w.Write([]byte("mine\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "untouched" {
		t.Errorf("the symlink target was written: %q", b)
	}
	if fi, err := os.Lstat(filepath.Join(d, FileName)); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("cubit.prom is still a symlink: %v %v", fi, err)
	}
}

// A symlinked temp name must not be written through: O_EXCL refuses it, and the
// stale entry is removed first.
func TestASymlinkedTempNameIsNotFollowed(t *testing.T) {
	d, other := tmpdir(t), tmpdir(t)
	victim := filepath.Join(other, "victim")
	os.WriteFile(victim, []byte("untouched"), 0o644)
	if err := os.Symlink(victim, filepath.Join(d, tmpName)); err != nil {
		t.Fatal(err)
	}
	w := open(t, d)
	if err := w.Write([]byte("mine\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "untouched" {
		t.Errorf("written through the temp symlink: %q", b)
	}
}

func TestAWriteThatCannotCompleteFailsCleanly(t *testing.T) {
	d := tmpdir(t)
	// Preflight passes; the write cannot remove a non-empty directory at the temp name.
	if err := os.MkdirAll(filepath.Join(d, tmpName, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := open(t, d)
	err := w.Write([]byte("x\n"))
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("%v", err)
	}
	if _, serr := os.Stat(filepath.Join(d, FileName)); serr == nil {
		t.Error("a failed write must not create cubit.prom")
	}
}

func TestOpenPreflight(t *testing.T) {
	ref := refuser(t)
	d := tmpdir(t)

	if _, err := Open(filepath.Join(d, "missing"), "/h", ref); !errors.Is(err, ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if got := names(t, d); len(got) != 0 {
		t.Errorf("Open must not create the directory: %v", got)
	}
	file := filepath.Join(d, "afile")
	os.WriteFile(file, nil, 0o644)
	if _, err := Open(file, "/h", ref); !errors.Is(err, ErrNotDir) {
		t.Errorf("a file: %v", err)
	}
	if _, err := Open("", "/h", ref); !errors.Is(err, ErrNotExist) {
		t.Errorf("empty: %v", err)
	}

	// A refused directory is never touched, not even for the lock file.
	ssh := filepath.Join(d, "x", ".ssh")
	os.MkdirAll(ssh, 0o755)
	_, err := Open(ssh, "/h", ref)
	var re *RefusedError
	if !errors.As(err, &re) || re.Class != "builtin" || !errors.Is(err, ErrRefused) {
		t.Errorf("refused: %v", err)
	}
	if strings.Contains(err.Error(), ssh) || strings.Contains(err.Error(), ".ssh") {
		t.Errorf("the message names a refused place: %q", err)
	}
	if got := names(t, ssh); len(got) != 0 {
		t.Errorf("a file was created in a refused directory: %v", got)
	}
	// ...through a symlink too.
	link := filepath.Join(d, "link")
	os.Symlink(ssh, link)
	if _, err := Open(link, "/h", ref); !errors.Is(err, ErrRefused) {
		t.Errorf("symlink into a refused directory: %v", err)
	}
	if got := names(t, ssh); len(got) != 0 {
		t.Errorf("a file was created behind the symlink: %v", got)
	}
	// A configured entry is class config.
	cfgd := filepath.Join(d, "private")
	os.Mkdir(cfgd, 0o755)
	_, err = Open(cfgd, "/h", refuser(t, cfgd))
	if !errors.As(err, &re) || re.Class != "config" {
		t.Errorf("config class: %v", err)
	}
	// A symlinked directory that resolves somewhere fine is allowed.
	ok := filepath.Join(d, "ok")
	os.Mkdir(ok, 0o755)
	os.Symlink(ok, filepath.Join(d, "oklink"))
	w, err := Open(filepath.Join(d, "oklink"), "/h", ref)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	// ~/ is expanded.
	w, err = Open("~/ok", d, ref)
	if err != nil {
		t.Fatalf("~/: %v", err)
	}
	w.Close()
}

func TestNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	d := tmpdir(t)
	ro := filepath.Join(d, "ro")
	os.Mkdir(ro, 0o500)
	t.Cleanup(func() { os.Chmod(ro, 0o755) })
	if _, err := Open(ro, "/h", refuser(t)); !errors.Is(err, ErrNotWritable) {
		t.Errorf("%v", err)
	}
}

func TestOneWriterPerDirectory(t *testing.T) {
	d := tmpdir(t)
	first := open(t, d)
	if _, err := Open(d, "/h", refuser(t)); !errors.Is(err, ErrLocked) {
		t.Fatalf("a second writer: %v", err)
	}
	// A different directory is independent.
	other := tmpdir(t)
	open(t, other)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}
	w, err := Open(d, "/h", refuser(t))
	if err != nil {
		t.Fatalf("after the first writer closed: %v", err)
	}
	w.Close()
	if err := first.Write([]byte("x")); err == nil {
		t.Error("a closed writer must not write")
	}
}

// The lock is held by another process, and dies with it: a crashed holder
// never blocks a restart.
func TestLockSurvivesAcrossProcessesAndIsReleasedByDeath(t *testing.T) {
	if os.Getenv("CUBIT_TEXTFILE_HOLDER") != "" {
		return
	}
	d := tmpdir(t)
	cmd := exec.Command(os.Args[0], "-test.run=TestHolderProcess", "-test.v")
	cmd.Env = append(os.Environ(), "CUBIT_TEXTFILE_HOLDER="+d)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	deadline := time.Now().Add(10 * time.Second)
	var got []byte
	for !bytes.Contains(got, []byte("HOLDING")) {
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatalf("the holder never reported: %q", got)
		}
		n, _ := stdout.Read(buf)
		got = append(got, buf[:n]...)
	}
	if _, err := Open(d, "/h", refuser(t)); !errors.Is(err, ErrLocked) {
		cmd.Process.Kill()
		t.Fatalf("another process holds the lock: %v", err)
	}
	cmd.Process.Kill() // kill -9: no Close, no cleanup
	_ = cmd.Wait()
	w, err := Open(d, "/h", refuser(t))
	if err != nil {
		t.Fatalf("a crashed holder must not block a restart: %v", err)
	}
	w.Close()
}

func TestHolderProcess(t *testing.T) {
	d := os.Getenv("CUBIT_TEXTFILE_HOLDER")
	if d == "" {
		t.Skip("a helper for the cross-process lock test")
	}
	w, err := Open(d, "/h", refuser(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = w
	os.Stdout.WriteString("HOLDING\n")
	time.Sleep(time.Minute)
}

func TestTextHasNoPath(t *testing.T) {
	d := tmpdir(t)
	_, err := os.ReadFile(filepath.Join(d, "nope"))
	if got := Text(err); strings.Contains(got, d) || got != "no such file or directory" {
		t.Errorf("%q", got)
	}
	for en, want := range map[syscall.Errno]string{syscall.ENOSPC: "ENOSPC", syscall.EACCES: "EACCES", syscall.Errno(250): "errno 250"} {
		if got := Errno(&WriteError{"x", en}); got != want {
			t.Errorf("%v -> %q", en, got)
		}
	}
	if Errno(errors.New("no errno")) != "" {
		t.Error("an error with no errno has no name")
	}
}

func TestUnsafeDirectoryModes(t *testing.T) {
	ref := refuser(t)
	d := tmpdir(t)
	world := filepath.Join(d, "world")
	os.Mkdir(world, 0o755)
	os.Chmod(world, 0o777) // the umask would otherwise trim it
	_, err := Open(world, "/h", ref)
	var me *DirModeError
	if !errors.Is(err, ErrUnsafeDir) || !errors.As(err, &me) || me.Mode != 0o777 {
		t.Errorf("a world-writable directory: %v", err)
	}
	if got := names(t, world); len(got) != 0 {
		t.Errorf("nothing may be created in an unsafe directory: %v", got)
	}
	sticky := filepath.Join(d, "sticky")
	os.Mkdir(sticky, 0o755)
	os.Chmod(sticky, 0o777|os.ModeSticky)
	if _, err := Open(sticky, "/h", ref); !errors.Is(err, ErrUnsafeDir) {
		t.Errorf("sticky does not make it safe: %v", err)
	}
	group := filepath.Join(d, "group")
	os.Mkdir(group, 0o755)
	os.Chmod(group, 0o775)
	w, err := Open(group, "/h", ref)
	if err != nil {
		t.Fatalf("group-writable opens with a warning: %v", err)
	}
	defer w.Close()
	if len(w.Warnings) != 1 || !strings.Contains(w.Warnings[0], "mode 0775") || strings.Contains(w.Warnings[0], group) {
		t.Errorf("warnings: %v", w.Warnings)
	}
	ok := filepath.Join(d, "ok")
	os.Mkdir(ok, 0o755)
	w2 := open(t, ok)
	if len(w2.Warnings) != 0 {
		t.Errorf("a 0755 directory has nothing to warn about: %v", w2.Warnings)
	}
}

func TestAnExistingFileMustBeOursAndNotADirectory(t *testing.T) {
	ref := refuser(t)
	d := tmpdir(t)
	os.WriteFile(filepath.Join(d, FileName), []byte("old"), 0o644)
	old := euid
	euid = func() int { return os.Geteuid() + 1 } // pretend the file is someone else's
	_, err := Open(d, "/h", ref)
	euid = old
	if !errors.Is(err, ErrForeignFile) {
		t.Errorf("foreign file: %v", err)
	}
	if got := names(t, d); strings.Contains(strings.Join(got, ","), "lock") {
		t.Errorf("nothing is created when preflight fails: %v", got)
	}
	// Ours is fine.
	w, err := Open(d, "/h", ref)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()

	dd := tmpdir(t)
	os.Mkdir(filepath.Join(dd, FileName), 0o755)
	if _, err := Open(dd, "/h", ref); !errors.Is(err, ErrNotFile) {
		t.Errorf("cubit.prom as a directory must fail at preflight, once: %v", err)
	}
}

func TestTheLockFileIsPrivateAndNotFollowed(t *testing.T) {
	d, other := tmpdir(t), tmpdir(t)
	w := open(t, d)
	fi, err := os.Stat(filepath.Join(d, lockName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("lock mode %v %v, want 0600", fi.Mode().Perm(), err)
	}
	w.Close()
	victim := filepath.Join(other, "victim")
	os.WriteFile(victim, nil, 0o644)
	os.Remove(filepath.Join(d, lockName))
	if err := os.Symlink(victim, filepath.Join(d, lockName)); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(d, "/h", refuser(t)); err == nil {
		t.Error("a lock file that is a symlink must be refused")
	}
}

// If the directory is replaced, the pinned directory keeps receiving writes and
// Moved says the path no longer names it.
func TestMovedNoticesAReplacedDirectory(t *testing.T) {
	d := tmpdir(t)
	parent := filepath.Dir(d)
	dir := filepath.Join(parent, "textdir-"+filepath.Base(d))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	w := open(t, dir)
	if w.Moved() {
		t.Error("not moved yet")
	}
	if err := os.Rename(dir, dir+".old"); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir + ".old")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !w.Moved() {
		t.Error("the path now names a different directory")
	}
	if err := w.Write([]byte("still goes to the pinned directory\n")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir+".old", FileName)); err != nil || !strings.Contains(string(b), "pinned") {
		t.Errorf("the write went to the original directory: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
		t.Error("it must never fall back to writing by path")
	}
}

// A symlink into a refused prefix is turned away without its target being
// statted or opened: the refused name never reaches a syscall that follows it.
func TestASymlinkIntoARefusedPrefixIsNeverStatted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	d := tmpdir(t)
	ssh := filepath.Join(d, "x", ".ssh")
	if err := os.MkdirAll(ssh, 0o755); err != nil {
		t.Fatal(err)
	}
	// No search permission on the refused directory: a stat that follows the link
	// would fail with a permission error, not with ErrRefused.
	if err := os.Chmod(filepath.Join(d, "x"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(d, "x"), 0o755) })
	link := filepath.Join(d, "link")
	if err := os.Symlink(ssh, link); err != nil {
		t.Fatal(err)
	}
	_, err := Open(link, "/h", refuser(t))
	if !errors.Is(err, ErrRefused) {
		t.Errorf("%v, want ErrRefused and not a permission error from following the link", err)
	}
}

// A symlink to a file is not a directory, and says so.
func TestASymlinkToAFileIsNotADirectory(t *testing.T) {
	d := tmpdir(t)
	f := filepath.Join(d, "afile")
	os.WriteFile(f, nil, 0o644)
	os.Symlink(f, filepath.Join(d, "link"))
	if _, err := Open(filepath.Join(d, "link"), "/h", refuser(t)); !errors.Is(err, ErrNotDir) {
		t.Errorf("%v", err)
	}
}
