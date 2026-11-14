package walk

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hedronite/cubit/internal/contain"
)

func tmp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func put(t *testing.T, base, rel string, n int) {
	t.Helper()
	p := filepath.Join(base, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixedClock() func() time.Time {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time { return t0 }
}

func cfg(refuse *contain.Refuser) Config {
	return Config{
		Refuse:     refuse,
		Budget:     NewBudget(0, 0, fixedClock()),
		DepthGuard: 64,
		DedupeMax:  1 << 20,
	}
}

func noRefuse(t *testing.T) *contain.Refuser {
	t.Helper()
	r, err := contain.NewRefuser("/nonexistent-home", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func measure(t *testing.T, c Config, dir string) Tally {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	tl, err := c.Measure(context.Background(), root, contain.Components(dir))
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

// oracle is an independent du: filepath.WalkDir with lstat, hardlinks counted
// once, and bytes split by ClassifyProfile over the whole relative path.
func oracle(t *testing.T, dir string, skip func(rel string) bool) (total int64, prof [NumProfiles]int64) {
	t.Helper()
	seen := map[[2]uint64]bool{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			rel = ""
		}
		if rel != "" && skip != nil && skip(filepath.ToSlash(rel)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st, _ := StatOf(fi)
		if st.Nlink > 1 && !fi.IsDir() {
			k := [2]uint64{st.Dev, st.Ino}
			if seen[k] {
				return nil
			}
			seen[k] = true
		}
		n := int64(st.Blocks) * 512
		total += n
		var comps []string
		if rel != "" {
			comps = strings.Split(filepath.ToSlash(rel), "/")
		}
		prof[ClassifyProfile(comps)] += n
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total, prof
}

func buildTarget(t *testing.T, base string) {
	t.Helper()
	for rel, n := range map[string]int{
		"debug/a.rlib":                        70000,
		"debug/deps/b.rlib":                   130000,
		"debug/incremental/crate/q.bin":       90000,
		"debug/build/x/out/gen.rs":            3000,
		"release/c.rlib":                      50000,
		"release/deps/d":                      20000,
		"doc/cargo/index.html":                40000,
		"x86_64-unknown-linux-musl/debug/e":   60000,
		"x86_64-unknown-linux-musl/release/f": 10000,
		"incremental/top.bin":                 8000,
		"tmp/scratch":                         5000,
		".rustc_info.json":                    4000,
		"CACHEDIR.TAG":                        100,
	} {
		put(t, base, rel, n)
	}
}

func TestMeasureMatchesAnIndependentDu(t *testing.T) {
	d := tmp(t)
	buildTarget(t, d)
	got := measure(t, cfg(noRefuse(t)), d)
	want, wantProf := oracle(t, d, nil)
	if got.Bytes != want {
		t.Errorf("bytes %d, oracle %d", got.Bytes, want)
	}
	if got.Profiles != wantProf {
		t.Errorf("profiles %v, oracle %v", got.Profiles, wantProf)
	}
	var sum int64
	for _, v := range got.Profiles {
		sum += v
	}
	if sum != got.Bytes {
		t.Errorf("profiles sum %d != total %d", sum, got.Bytes)
	}
	if len(got.Skips) != 0 {
		t.Errorf("unexpected skips: %v", got.Skips)
	}
	for _, p := range []Profile{Debug, Release, Doc, Incremental, Other} {
		if got.Profiles[p] == 0 {
			t.Errorf("profile %s got no bytes; the tree has some in every profile", p)
		}
	}
}

func TestHardlinkIsCountedOnce(t *testing.T) {
	d := tmp(t)
	put(t, d, "debug/deps/lib.rlib", 100000)
	before := measure(t, cfg(noRefuse(t)), d).Bytes
	if err := os.Link(filepath.Join(d, "debug/deps/lib.rlib"), filepath.Join(d, "debug/lib.rlib")); err != nil {
		t.Fatal(err)
	}
	after := measure(t, cfg(noRefuse(t)), d)
	if after.Bytes != before {
		t.Errorf("adding a hardlink changed the total: %d -> %d", before, after.Bytes)
	}
	if want, _ := oracle(t, d, nil); after.Bytes != want {
		t.Errorf("bytes %d, oracle %d", after.Bytes, want)
	}
}

func TestDedupeOverflowIsReportedAndStillCounts(t *testing.T) {
	d := tmp(t)
	for i := 0; i < 4; i++ {
		name := string(rune('a' + i))
		put(t, d, "x/"+name, 20000)
		if err := os.Link(filepath.Join(d, "x", name), filepath.Join(d, "y-"+name)); err != nil {
			t.Fatal(err)
		}
	}
	c := cfg(noRefuse(t))
	c.DedupeMax = 1
	got := measure(t, c, d)
	if got.Skips["dedupe_overflow"] == 0 {
		t.Errorf("a full hardlink set must be reported: %v", got.Skips)
	}
	full := measure(t, cfg(noRefuse(t)), d)
	if got.Bytes <= full.Bytes {
		t.Errorf("an overflowing set double counts, so bytes should rise: %d vs %d", got.Bytes, full.Bytes)
	}
}

func TestSymlinksAreMeasuredAsLinksNotFollowed(t *testing.T) {
	d, out := tmp(t), tmp(t)
	put(t, out, "huge", 1_000_000)
	put(t, d, "t/f", 10000)
	if err := os.Symlink(out, filepath.Join(d, "t", "outlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("f", filepath.Join(d, "t", "samedir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("l2", filepath.Join(d, "t", "l1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("l1", filepath.Join(d, "t", "l2")); err != nil {
		t.Fatal(err)
	}
	got := measure(t, cfg(noRefuse(t)), d)
	want, _ := oracle(t, d, nil) // WalkDir does not follow symlinks either
	if got.Bytes != want {
		t.Errorf("bytes %d, oracle %d; a symlink must count as link bytes only", got.Bytes, want)
	}
	if len(got.Skips) != 0 {
		t.Errorf("in-tree symlinks are never classified or skipped: %v", got.Skips)
	}
	if got.Bytes >= 1_000_000 {
		t.Error("the symlink target's bytes leaked into the total")
	}
}

type recorder struct {
	mu    sync.Mutex
	names []string
	hook  func(name string)
}

func (r *recorder) stat(fi fs.FileInfo) (Stat, bool) {
	r.mu.Lock()
	r.names = append(r.names, fi.Name())
	r.mu.Unlock()
	if r.hook != nil {
		r.hook(fi.Name())
	}
	return StatOf(fi)
}

func TestRefusedPathsAreNeverStattedOrCounted(t *testing.T) {
	d := tmp(t)
	put(t, d, "ok/f", 30000)
	put(t, d, ".ssh/id_secret", 50000)
	put(t, d, "sub/.AWS/credentials", 50000) // case-insensitive, any depth
	put(t, d, "sub/fine", 10000)
	put(t, d, "own-secret.txt", 9000)

	ref, err := contain.NewRefuser("/nonexistent-home", []string{filepath.Join(d, "own-secret.txt")}, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	c := cfg(ref)
	c.Stat = rec.stat
	got := measure(t, c, d)

	if got.Skips["refused"] != 3 {
		t.Errorf("refused = %d, want 3 (.ssh, .AWS, own-secret.txt): %v", got.Skips["refused"], got.Skips)
	}
	for _, n := range rec.names {
		switch n {
		case ".ssh", "id_secret", ".AWS", "credentials", "own-secret.txt":
			t.Errorf("refused name %q was statted", n)
		}
	}
	want, _ := oracle(t, d, func(rel string) bool {
		return rel == ".ssh" || rel == "sub/.AWS" || rel == "own-secret.txt"
	})
	if got.Bytes != want {
		t.Errorf("bytes %d, want %d (refused subtrees add nothing)", got.Bytes, want)
	}
}

func TestForeignDeviceIsSkippedWithItsContents(t *testing.T) {
	d := tmp(t)
	put(t, d, "a/f", 20000)
	put(t, d, "mnt/big", 500000)
	c := cfg(noRefuse(t))
	c.Stat = func(fi fs.FileInfo) (Stat, bool) {
		st, ok := StatOf(fi)
		if fi.Name() == "mnt" {
			st.Dev++ // pretend it is a mount point
		}
		return st, ok
	}
	got := measure(t, c, d)
	if got.Skips["foreign_device"] != 1 {
		t.Fatalf("skips: %v", got.Skips)
	}
	want, _ := oracle(t, d, func(rel string) bool { return rel == "mnt" })
	if got.Bytes != want {
		t.Errorf("bytes %d, want %d: the other device adds nothing, not even its mount point", got.Bytes, want)
	}
}

func TestPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	d := tmp(t)
	put(t, d, "ok/f", 20000)
	put(t, d, "locked/inner", 90000)
	if err := os.Chmod(filepath.Join(d, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(d, "locked"), 0o755) })
	got := measure(t, cfg(noRefuse(t)), d)
	if got.Skips["permission_denied"] != 1 {
		t.Fatalf("skips: %v", got.Skips)
	}
	os.Chmod(filepath.Join(d, "locked"), 0o755)
	full := measure(t, cfg(noRefuse(t)), d)
	if got.Bytes >= full.Bytes {
		t.Errorf("a locked subtree must add nothing: %d vs %d", got.Bytes, full.Bytes)
	}
}

func TestVanishedDuringTheWalk(t *testing.T) {
	d := tmp(t)
	put(t, d, "a", 10000)
	put(t, d, "z", 10000)
	c := cfg(noRefuse(t))
	c.Stat = func(fi fs.FileInfo) (Stat, bool) {
		if fi.Name() == "a" {
			os.Remove(filepath.Join(d, "z")) // z is read later, and is gone by then
		}
		return StatOf(fi)
	}
	got := measure(t, c, d)
	if got.Skips["vanished"] != 1 {
		t.Errorf("skips: %v", got.Skips)
	}
}

func TestDepthGuard(t *testing.T) {
	d := tmp(t)
	put(t, d, "1/2/3/4/5/6/deep", 20000)
	put(t, d, "1/shallow", 10000)
	c := cfg(noRefuse(t))
	c.DepthGuard = 3
	got := measure(t, c, d)
	if got.Skips["depth_exceeded"] != 1 {
		t.Errorf("skips: %v", got.Skips)
	}
	if full := measure(t, cfg(noRefuse(t)), d); got.Bytes >= full.Bytes {
		t.Errorf("the too-deep subtree must add nothing: %d vs %d", got.Bytes, full.Bytes)
	}
}

func TestMaxFilesCap(t *testing.T) {
	d := tmp(t)
	for i := 0; i < 60; i++ {
		put(t, d, filepath.Join("d", string(rune('a'+i%26)), string(rune('A'+i/26)), "f"), 1000)
	}
	c := cfg(noRefuse(t))
	c.Budget = NewBudget(10, 0, fixedClock())
	got := measure(t, c, d)
	if !c.Budget.FilesExceeded() || c.Budget.DeadlineExceeded() {
		t.Fatalf("files cut %v, deadline cut %v", c.Budget.FilesExceeded(), c.Budget.DeadlineExceeded())
	}
	if full := measure(t, cfg(noRefuse(t)), d); got.Bytes >= full.Bytes {
		t.Errorf("a capped walk is a lower bound: %d vs %d", got.Bytes, full.Bytes)
	}
}

func TestDeadlineUsesTheInjectedClock(t *testing.T) {
	d := tmp(t)
	for i := 0; i < 40; i++ {
		put(t, d, filepath.Join("d", string(rune('a'+i%26)), string(rune('A'+i/26)), "f"), 1000)
	}
	var mu sync.Mutex
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Second) // every read of the clock costs one second
		return now
	}
	c := cfg(noRefuse(t))
	c.Budget = NewBudget(0, 5*time.Second, clock)
	measure(t, c, d)
	if !c.Budget.DeadlineExceeded() || c.Budget.FilesExceeded() {
		t.Fatalf("deadline cut %v, files cut %v", c.Budget.DeadlineExceeded(), c.Budget.FilesExceeded())
	}
}

func TestAWalkIsDeterministic(t *testing.T) {
	d := tmp(t)
	buildTarget(t, d)
	put(t, d, "x/y", 7000)
	os.Link(filepath.Join(d, "x", "y"), filepath.Join(d, "debug", "y-link"))
	os.Link(filepath.Join(d, "x", "y"), filepath.Join(d, "release", "y-link"))
	first := measure(t, cfg(noRefuse(t)), d)
	for i := 0; i < 5; i++ {
		again := measure(t, cfg(noRefuse(t)), d)
		if again.Bytes != first.Bytes || again.Profiles != first.Profiles {
			t.Fatalf("run %d differs: %+v vs %+v", i, again, first)
		}
	}
}

func TestContextCancelStopsTheWalk(t *testing.T) {
	d := tmp(t)
	for i := 0; i < 600; i++ {
		put(t, d, filepath.Join("d", "f"+string(rune('0'+i%10))+string(rune('a'+i%26))+string(rune('A'+i/26))), 100)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root, _ := os.OpenRoot(d)
	defer root.Close()
	if _, err := cfg(noRefuse(t)).Measure(ctx, root, contain.Components(d)); err == nil {
		t.Error("a canceled context must surface as an error")
	}
}

// The walk must be safe to run on many units at once.
func TestConcurrentWalksOfAGeneratedTree(t *testing.T) {
	d := tmp(t)
	for w := 0; w < 8; w++ {
		for i := 0; i < 60; i++ {
			put(t, d, filepath.Join("w", string(rune('a'+w)), "target", "debug", "deps", "f"+string(rune('a'+i%26))+string(rune('A'+i/26))), 2000)
		}
	}
	var wg sync.WaitGroup
	results := make([]Tally, 8)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			root, err := os.OpenRoot(filepath.Join(d, "w", string(rune('a'+w)), "target"))
			if err != nil {
				t.Error(err)
				return
			}
			defer root.Close()
			c := cfg(noRefuse(t))
			results[w], _ = c.Measure(context.Background(), root, contain.Components(d))
		}()
	}
	wg.Wait()
	for w := 1; w < 8; w++ {
		if results[w].Bytes != results[0].Bytes {
			t.Errorf("identical trees measured differently: %d vs %d", results[w].Bytes, results[0].Bytes)
		}
	}
}

func TestClassifyProfile(t *testing.T) {
	cases := []struct {
		rel  string
		want Profile
	}{
		{"", Other},
		{"debug", Debug},
		{"debug/deps/x", Debug},
		{"release/build/y", Release},
		{"doc/cargo", Doc},
		{"debug/incremental/c", Incremental},
		{"release/incremental", Incremental},
		{"incremental", Incremental},
		{"x86_64-apple-darwin/debug/z", Other},
		{"x86_64-apple-darwin/debug/incremental/z", Incremental},
		{"tmp/a", Other},
		{"package/a", Other},
		{".rustc_info.json", Other},
		{"Debug/x", Other}, // exact, case-sensitive
		{"debug/Incremental/x", Debug},
	}
	for _, c := range cases {
		var comps []string
		if c.rel != "" {
			comps = strings.Split(c.rel, "/")
		}
		if got := ClassifyProfile(comps); got != c.want {
			t.Errorf("%q: %s, want %s", c.rel, got, c.want)
		}
	}
}

// foldProfile is how the walk classifies: one step per component.
func foldProfile(comps []string) Profile {
	p := Other
	for depth, name := range comps {
		p = childProfile(p, depth, name)
	}
	return p
}

func FuzzClassifyProfile(f *testing.F) {
	for _, s := range []string{"debug/deps/x", "release", "incremental/a", "a/incremental/b", "doc", "x86_64-unknown/debug", "", "debug//x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		var comps []string
		for _, c := range strings.Split(rel, "/") {
			if c != "" {
				comps = append(comps, c)
			}
		}
		p := ClassifyProfile(comps)
		if p < 0 || p >= NumProfiles {
			t.Fatalf("out of range: %d", p)
		}
		if got := foldProfile(comps); got != p {
			t.Fatalf("%q: whole-path rule says %s, the walk's fold says %s", rel, p, got)
		}
		for _, c := range comps {
			if c == "incremental" && p != Incremental {
				t.Fatalf("%q holds an incremental component but classified %s", rel, p)
			}
		}
		for i := range comps {
			if ClassifyProfile(comps[:i+1]) == Incremental && p != Incremental {
				t.Fatalf("%q: a prefix is incremental but the whole is %s", rel, p)
			}
		}
	})
}

func sortedKeys(m map[string]int) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Refusal comes before any open or stat: a chmod-000 refused directory is
// reported as refused, not as permission_denied.
func TestRefusalPrecedesTheOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	d := tmp(t)
	put(t, d, "ok/f", 10000)
	put(t, d, ".ssh/key", 10000)
	if err := os.Chmod(filepath.Join(d, ".ssh"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(d, ".ssh"), 0o755) })
	got := measure(t, cfg(noRefuse(t)), d)
	if got.Skips["refused"] != 1 || got.Skips["permission_denied"] != 0 {
		t.Errorf("skips: %v", got.Skips)
	}
}

// du is the reference: one unit with hardlinks, a sparse file, directories and
// symlinks must match `du -sk` exactly.
func TestParityWithDu(t *testing.T) {
	du, err := exec.LookPath("du")
	if err != nil {
		t.Skip("no du")
	}
	d := tmp(t)
	buildTarget(t, d)
	put(t, d, "debug/deps/lib.rlib", 120000)
	os.Link(filepath.Join(d, "debug/deps/lib.rlib"), filepath.Join(d, "debug/lib.rlib"))
	os.Symlink("deps/lib.rlib", filepath.Join(d, "debug", "ln"))
	sp, err := os.Create(filepath.Join(d, "sparse"))
	if err != nil {
		t.Fatal(err)
	}
	sp.Truncate(1 << 30) // a 1 GiB hole that allocates (almost) nothing
	sp.Close()
	out, err := exec.Command(du, "-sk", d).Output()
	if err != nil {
		t.Skipf("du failed: %v", err)
	}
	kb, err := strconv.ParseInt(strings.Fields(string(out))[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	got := measure(t, cfg(noRefuse(t)), d)
	if got.Bytes != kb*1024 {
		t.Errorf("walk %d bytes, du -sk %d KiB (%d bytes)", got.Bytes, kb, kb*1024)
	}
	if got.Bytes > 1<<24 {
		t.Errorf("the sparse hole was counted as allocated: %d", got.Bytes)
	}
}

// A flat directory far over max_files must stop within one read chunk of the cap.
func TestMaxFilesDoesNotOvershootAFlatDirectory(t *testing.T) {
	d := tmp(t)
	for i := 0; i < 3000; i++ {
		os.WriteFile(filepath.Join(d, "f"+strconv.Itoa(i)), nil, 0o644)
	}
	c := cfg(noRefuse(t))
	c.Budget = NewBudget(100, 0, fixedClock())
	got := measure(t, c, d)
	if !c.Budget.FilesExceeded() {
		t.Fatal("the cap should trip")
	}
	if n := c.Budget.Entered(); n > 100+readChunk {
		t.Errorf("read %d entries against a cap of 100; the overshoot must stay within one chunk (%d)", n, readChunk)
	}
	_ = got
}
