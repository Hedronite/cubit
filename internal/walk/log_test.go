package walk

import (
	"bytes"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func capture() (*Logger, *bytes.Buffer) {
	var b bytes.Buffer
	l := slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})).With("alias", "p")
	return &Logger{L: l}, &b
}

// A directory swapped between the lstat and the open is counted as vanished and
// logged: at Warn with a depth and a hint but no path, at Debug with the relative path.
func TestSwapIsCountedAndLogged(t *testing.T) {
	d := tmp(t)
	put(t, d, "a/f", 10000)
	put(t, d, "z", 10000)
	lg, out := capture()
	c := cfg(noRefuse(t))
	c.Log = lg
	c.Stat = func(fi fs.FileInfo) (Stat, bool) {
		if fi.Name() == "a" && fi.IsDir() {
			os.Rename(filepath.Join(d, "a"), filepath.Join(d, "a.old"))
			os.Mkdir(filepath.Join(d, "a"), 0o755)
		}
		return StatOf(fi)
	}
	got := measure(t, c, d)
	if got.Skips["io_error"] != 1 || got.Skips["vanished"] != 0 {
		t.Fatalf("a swap must fail closed as io_error: %v", got.Skips)
	}
	var warn, debug string
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.Contains(l, "level=WARN") {
			warn = l
		}
		if strings.Contains(l, "level=DEBUG") {
			debug = l
		}
	}
	if !strings.Contains(warn, `msg="directory swapped during the walk"`) || !strings.Contains(warn, "alias=p") || !strings.Contains(warn, "depth=1") || !strings.Contains(warn, "hint=") {
		t.Errorf("warn: %q", warn)
	}
	if strings.Contains(warn, d) || strings.Contains(warn, "rel=") {
		t.Errorf("a path at warn: %q", warn)
	}
	if !strings.Contains(debug, "msg=\"swapped directory\"") || !strings.Contains(debug, "rel=a") {
		t.Errorf("debug: %q", debug)
	}
}

// File descriptor exhaustion stays io_error, and is reported once per root with a count.
func TestFDExhaustionIsCountedOnceAndStaysIOError(t *testing.T) {
	lg, out := capture()
	c := cfg(noRefuse(t))
	c.Log = lg
	var tl Tally
	for i := 0; i < 3; i++ {
		c.fail(&tl, &os.PathError{Op: "open", Path: "x", Err: syscall.EMFILE}, []string{"r", "x"}, 1)
	}
	c.fail(&tl, &os.PathError{Op: "open", Path: "x", Err: syscall.ENFILE}, []string{"r", "x"}, 1)
	if tl.Skips["io_error"] != 4 || len(tl.Skips) != 1 {
		t.Errorf("skips: %v", tl.Skips)
	}
	if out.Len() != 0 {
		t.Errorf("nothing is logged until the root is done: %q", out.String())
	}
	lg.Done()
	lg.Done() // a second call has nothing to say
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "level=WARN") || !strings.Contains(lines[0], "skipped=4") || !strings.Contains(lines[0], "ulimit -n") {
		t.Errorf("%q", out.String())
	}
}

func TestANilLoggerIsSilent(t *testing.T) {
	var l *Logger
	l.swap(1, "x")
	l.fds()
	l.Done()
}
