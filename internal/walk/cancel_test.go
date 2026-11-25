package walk

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hedronite/cubit/internal/contain"
	"github.com/Hedronite/cubit/internal/reason"
)

// cancelAt cancels the context on the n-th stat the walk makes and counts them
// all, so the test sees exactly how far the walk went on.
func cancelAt(t *testing.T, n int) (Config, context.Context, *int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	calls := new(int)
	c := cfg(noRefuse(t))
	c.Stat = func(fi fs.FileInfo) (Stat, bool) {
		*calls++
		if *calls == n {
			cancel()
		}
		return StatOf(fi)
	}
	return c, ctx, calls
}

func measureCtx(t *testing.T, c Config, ctx context.Context, dir string) error {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	_, err = c.Measure(ctx, root, contain.Components(dir))
	return err
}

// A cancel is honored at the next entry, far below 256 entries.
func TestCancelStopsAtTheNextEntry(t *testing.T) {
	d := tmp(t)
	for i := 0; i < 50; i++ {
		put(t, d, fmt.Sprintf("f%02d", i), 1)
	}
	c, ctx, calls := cancelAt(t, 4) // the root, then three files
	if err := measureCtx(t, c, ctx, d); !errors.Is(err, context.Canceled) || *calls != 4 {
		t.Fatalf("err=%v stats=%d, want canceled after 4", err, *calls)
	}
}

// A cancel inside one small directory stops the walk before the next one.
func TestCancelStopsAtADirectoryBoundary(t *testing.T) {
	d := tmp(t)
	for i := 0; i < 50; i++ {
		put(t, d, fmt.Sprintf("d%02d/only", i), 1)
	}
	c, ctx, calls := cancelAt(t, 3) // the root, d00, d00/only
	if err := measureCtx(t, c, ctx, d); !errors.Is(err, context.Canceled) || *calls != 3 {
		t.Fatalf("err=%v stats=%d, want canceled after 3", err, *calls)
	}
}

func TestCancelledContextOnAnEmptyDirectory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := measureCtx(t, cfg(noRefuse(t)), ctx, tmp(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestCancelStopsDiscovery(t *testing.T) {
	d := tmp(t)
	for i := 0; i < 50; i++ {
		put(t, d, fmt.Sprintf("w%02d/Cargo.toml", i), 1)
		put(t, d, fmt.Sprintf("w%02d/target/f", i), 1)
	}
	c, ctx, calls := cancelAt(t, 2) // the top, then the first child
	root, err := os.OpenRoot(d)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var tl Tally
	found, err := c.Discover(ctx, root, contain.Components(d), 3, &tl)
	if !errors.Is(err, context.Canceled) || *calls != 2 || len(found) != 0 || len(tl.Skips) != 0 {
		t.Fatalf("err=%v stats=%d found=%d skips=%v", err, *calls, len(found), tl.Skips)
	}
}

// The walk never measures a refused subtree, however the entry spells it.
func TestRefusedThroughALinkIsNotMeasured(t *testing.T) {
	base := tmp(t)
	put(t, base, "real/tgt/debug/build.bin", 1<<20)
	put(t, base, "real/tgt/debug/secret/blob", 4<<20)
	if err := os.Symlink("real", filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "real", "tgt")
	want := measure(t, cfg(noRefuse(t)), filepath.Join(dir)).Bytes
	for _, spelling := range []string{"real", "link"} {
		ref, err := contain.NewRefuser("/nonexistent-home", []string{filepath.Join(base, spelling, "tgt", "debug", "secret")}, "")
		if err != nil {
			t.Fatal(err)
		}
		got := measure(t, cfg(ref), dir)
		if got.Skips[reason.Refused] != 1 || got.Bytes > want-(4<<20) {
			t.Errorf("entry via %s: bytes %d of %d, skips %v", spelling, got.Bytes, want, got.Skips)
		}
	}
}

// Children that discovery does not enter, here because they sit on another
// device, still have a cancel check between them.
func TestCancelStopsDiscoveryBetweenSkippedChildren(t *testing.T) {
	d := tmp(t)
	for i := 0; i < 50; i++ {
		put(t, d, fmt.Sprintf("w%02d/f", i), 1)
	}
	c, ctx, calls := cancelAt(t, 2) // the top, then the first child
	inner := c.Stat
	c.Stat = func(fi fs.FileInfo) (Stat, bool) {
		st, ok := inner(fi)
		if *calls > 1 {
			st.Dev++
		}
		return st, ok
	}
	root, err := os.OpenRoot(d)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var tl Tally
	_, err = c.Discover(ctx, root, contain.Components(d), 3, &tl)
	if !errors.Is(err, context.Canceled) || *calls != 2 || tl.Skips[reason.ForeignDevice] != 1 {
		t.Fatalf("err=%v stats=%d skips=%v", err, *calls, tl.Skips)
	}
}
