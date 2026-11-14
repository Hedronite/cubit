package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Generated trees for the benchmarks and the budgets in docs/performance.md. They
// are built once per benchmark, outside the timer, in a temporary directory.

// treeFiles writes n one-byte files over dirs directories under base, spread
// evenly, and returns the number of files written.
func treeFiles(tb testing.TB, base string, dirs, files int) {
	tb.Helper()
	per := files / dirs
	for d := 0; d < dirs; d++ {
		dir := filepath.Join(base, fmt.Sprintf("d%04d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			tb.Fatal(err)
		}
		for f := 0; f < per; f++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d", f)), []byte{1}, 0o644); err != nil {
				tb.Fatal(err)
			}
		}
	}
}

// standardTree is the budget tree: a workspace root of ten workspaces whose
// targets hold, between them, 10,000 files in 1,000 directories.
func standardTree(tb testing.TB) string {
	tb.Helper()
	root := tb.TempDir()
	for w := 0; w < 10; w++ {
		ws := filepath.Join(root, fmt.Sprintf("ws%02d", w))
		if err := os.MkdirAll(filepath.Join(ws, "src"), 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, "Cargo.toml"), []byte("x"), 0o644); err != nil {
			tb.Fatal(err)
		}
		treeFiles(tb, filepath.Join(ws, "target", "debug"), 100, 1000)
	}
	return root
}

// deepTree is one workspace, under the root, whose target is a chain of 60
// directories with a file in each. (A workspace root does not count as a
// workspace itself, so the workspace sits one level down.)
func deepTree(tb testing.TB) string {
	tb.Helper()
	root := tb.TempDir()
	ws := filepath.Join(root, "app")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "Cargo.toml"), []byte("x"), 0o644); err != nil {
		tb.Fatal(err)
	}
	p := filepath.Join(ws, "target")
	for i := 0; i < 60; i++ {
		p = filepath.Join(p, fmt.Sprintf("n%02d", i))
		if err := os.MkdirAll(p, 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "f"), []byte{1}, 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	return root
}

// wideTree is one workspace, under the root, whose target holds 10,000 files in a
// single directory.
func wideTree(tb testing.TB) string {
	tb.Helper()
	root := tb.TempDir()
	ws := filepath.Join(root, "app")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "Cargo.toml"), []byte("x"), 0o644); err != nil {
		tb.Fatal(err)
	}
	treeFiles(tb, filepath.Join(ws, "target"), 1, 10000)
	return root
}

func BenchmarkScan(b *testing.B) {
	for _, tc := range []struct {
		name       string
		make       func(testing.TB) string
		workspaces int
		minBytes   int64
	}{
		{"10kfiles_1kdirs", standardTree, 10, 10000 * 4096},
		{"deep", deepTree, 1, 60 * 4096},
		{"wide", wideTree, 1, 10000 * 4096},
	} {
		b.Run(tc.name, func(b *testing.B) {
			root := tc.make(b)
			s := spec(b, wsRoot("bench", root))
			e := Env{Now: time.Now}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d, err := Scan(context.Background(), s, e)
				if err != nil {
					b.Fatal(err)
				}
				// A benchmark that measures an empty walk is worse than none.
				if r := d.Roots[0]; !r.Success || r.Partial || r.WorkspacesFound != tc.workspaces || r.Bytes < tc.minBytes/8 {
					b.Fatalf("the benchmark tree must scan complete and full: found %d workspaces, %d bytes, %+v", r.WorkspacesFound, r.Bytes, r.Error)
				}
			}
		})
	}
}

// settledGoroutines waits briefly for goroutines that are already finishing, and
// returns how many exist.
func settledGoroutines(want int) int {
	n := runtime.NumGoroutine()
	for i := 0; i < 100 && n > want; i++ {
		time.Sleep(10 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}

// Budget: no goroutine is left running once Scan returns, whether it finished or
// was canceled, with one worker or several.
func TestScanLeavesNoGoroutinesRunning(t *testing.T) {
	root := standardTree(t)
	for _, workers := range []int{1, 4} {
		for _, cancel := range []bool{false, true} {
			s := spec(t, wsRoot("g", root))
			s.Workers = workers
			before := runtime.NumGoroutine()
			ctx, stop := context.WithCancel(context.Background())
			if cancel {
				go func() { time.Sleep(time.Millisecond); stop() }()
			}
			_, _ = Scan(ctx, s, Env{Now: time.Now})
			stop()
			if got := settledGoroutines(before); got > before {
				buf := make([]byte, 1<<16)
				buf = buf[:runtime.Stack(buf, true)]
				t.Errorf("workers=%d canceled=%v: %d goroutines before, %d after\n%s", workers, cancel, before, got, strings.TrimSpace(string(buf)))
			}
		}
	}
}
