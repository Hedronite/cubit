package walk

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/Hedronite/cubit/internal/contain"
)

func ws(t *testing.T, base, rel string) {
	t.Helper()
	put(t, base, rel+"/Cargo.toml", 10)
	put(t, base, rel+"/target/debug/x", 20000)
	put(t, base, rel+"/src/main.rs", 10)
}

func discover(t *testing.T, c Config, dir string, depth int) ([]Workspace, Tally) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var tl Tally
	got, err := c.Discover(context.Background(), root, contain.Components(dir), depth, &tl)
	if err != nil {
		t.Fatal(err)
	}
	return got, tl
}

func rels(w []Workspace) []string {
	var out []string
	for _, x := range w {
		out = append(out, x.Rel)
	}
	return out
}

func TestDiscoverFindsWorkspacesToTheDepth(t *testing.T) {
	d := tmp(t)
	ws(t, d, "app")
	ws(t, d, "libs/core")
	ws(t, d, "a/b/c")     // depth 3
	ws(t, d, "a/b/c/d/e") // depth 5
	put(t, d, "norust/readme", 1)
	put(t, d, "onlytarget/target/x", 1)  // target without Cargo.toml
	put(t, d, "onlycargo/Cargo.toml", 1) // Cargo.toml without target
	if err := os.MkdirAll(filepath.Join(d, "dircargo", "Cargo.toml"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(d, "dircargo", "target"), 0o755)

	got, _ := discover(t, cfg(noRefuse(t)), d, 3)
	want := []string{"a/b/c", "app", "libs/core"}
	if !reflect.DeepEqual(rels(got), want) {
		t.Errorf("depth 3: %v, want %v", rels(got), want)
	}
	got, _ = discover(t, cfg(noRefuse(t)), d, 1)
	if !reflect.DeepEqual(rels(got), []string{"app"}) {
		t.Errorf("depth 1: %v", rels(got))
	}
	got, _ = discover(t, cfg(noRefuse(t)), d, 6)
	if !reflect.DeepEqual(rels(got), []string{"a/b/c", "a/b/c/d/e", "app", "libs/core"}) {
		t.Errorf("depth 6: %v", rels(got))
	}
}

func TestDiscoverNeverEntersTargetGitOrNodeModules(t *testing.T) {
	d := tmp(t)
	ws(t, d, "app")
	// workspaces buried where discovery must not look
	ws(t, d, "app/target/nested")
	ws(t, d, "app/.git/hidden")
	ws(t, d, "node_modules/pkg")
	got, _ := discover(t, cfg(noRefuse(t)), d, 6)
	if !reflect.DeepEqual(rels(got), []string{"app"}) {
		t.Errorf("got %v", rels(got))
	}
}

func TestDiscoverTargetSymlinks(t *testing.T) {
	d, out := tmp(t), tmp(t)
	put(t, d, "shared/debug/x", 1000)
	put(t, out, "elsewhere/debug/x", 1000)
	for _, n := range []string{"inroot", "escape", "abs", "loop", "self", "up"} {
		put(t, d, n+"/Cargo.toml", 1)
	}
	for n, link := range map[string]string{
		"inroot": "../shared",
		"escape": "../../" + filepath.Base(out) + "/elsewhere",
		"abs":    filepath.Join(out, "elsewhere"),
		"loop":   "target",
		"self":   ".",
		"up":     "..",
	} {
		if err := os.Symlink(link, filepath.Join(d, n, "target")); err != nil {
			t.Fatal(err)
		}
	}
	got, tl := discover(t, cfg(noRefuse(t)), d, 3)
	if !reflect.DeepEqual(rels(got), []string{"inroot"}) {
		t.Errorf("only the in-root link is admitted, got %v", rels(got))
	}
	if got[0].Target != "shared" {
		t.Errorf("target resolved to %q", got[0].Target)
	}
	for reason, n := range map[string]int{"symlink_escape": 2, "symlink_loop": 3} {
		if tl.Skips[reason] != n {
			t.Errorf("%s = %d, want %d (all: %v)", reason, tl.Skips[reason], n, tl.Skips)
		}
	}
}

func TestDiscoverTwoWorkspacesOneTargetShareAnIdentity(t *testing.T) {
	d := tmp(t)
	put(t, d, "shared/debug/x", 1000)
	for _, n := range []string{"one", "two"} {
		put(t, d, n+"/Cargo.toml", 1)
		os.Symlink("../shared", filepath.Join(d, n, "target"))
	}
	got, _ := discover(t, cfg(noRefuse(t)), d, 2)
	if len(got) != 2 || got[0].Ino != got[1].Ino || got[0].Dev != got[1].Dev {
		t.Fatalf("expected two workspaces with one identity, got %+v", got)
	}
}

func TestDiscoverRefusesAndNeverReadsRefused(t *testing.T) {
	d := tmp(t)
	ws(t, d, "app")
	ws(t, d, ".ssh/sneaky") // a refused directory
	put(t, d, "linkholder/Cargo.toml", 1)
	os.MkdirAll(filepath.Join(d, ".docker"), 0o755)
	os.Symlink("../.docker", filepath.Join(d, "linkholder", "target")) // a link into a refused dir
	rec := &recorder{}
	c := cfg(noRefuse(t))
	c.Stat = rec.stat
	got, tl := discover(t, c, d, 4)
	if !reflect.DeepEqual(rels(got), []string{"app"}) {
		t.Errorf("got %v", rels(got))
	}
	// .ssh and .docker as directories, and the link that points into .docker.
	if tl.Skips["refused"] != 3 {
		t.Errorf("skips: %v", tl.Skips)
	}
	for _, n := range rec.names {
		if n == ".ssh" || n == "sneaky" || n == ".docker" {
			t.Errorf("refused name %q was statted", n)
		}
	}
}

func TestDiscoverSkipsAForeignDevice(t *testing.T) {
	d := tmp(t)
	ws(t, d, "app")
	ws(t, d, "mnt/app2")
	c := cfg(noRefuse(t))
	c.Stat = func(fi fs.FileInfo) (Stat, bool) {
		st, ok := StatOf(fi)
		if fi.Name() == "mnt" {
			st.Dev++
		}
		return st, ok
	}
	got, tl := discover(t, c, d, 3)
	if !reflect.DeepEqual(rels(got), []string{"app"}) || tl.Skips["foreign_device"] != 1 {
		t.Errorf("got %v, skips %v", rels(got), tl.Skips)
	}
}

func TestDiscoverCountsEntriesAgainstTheBudget(t *testing.T) {
	d := tmp(t)
	for i := 0; i < 30; i++ {
		ws(t, d, filepath.Join("p", string(rune('a'+i%26))+string(rune('A'+i/26))))
	}
	c := cfg(noRefuse(t))
	c.Budget = NewBudget(20, 0, fixedClock())
	got, _ := discover(t, c, d, 3)
	if !c.Budget.FilesExceeded() {
		t.Error("the cap should have tripped")
	}
	if len(got) >= 30 {
		t.Errorf("a capped discovery is a partial list, got %d", len(got))
	}
}

func TestSortedKeysHelperIsStable(t *testing.T) {
	if !reflect.DeepEqual(sortedKeys(map[string]int{"b": 1, "a": 2}), []string{"a", "b"}) {
		t.Fatal("sortedKeys")
	}
}

// A chain of links must not smuggle a refused directory past the check, and a
// legitimate chain of more than os.Root's eight hops is not a loop.
func TestDiscoverResolvesChainsHopByHop(t *testing.T) {
	d := tmp(t)
	put(t, d, "shared/debug/x", 1000)
	put(t, d, ".kube/config", 1000)
	os.Symlink(".kube", filepath.Join(d, "hop2"))
	os.Symlink("hop2", filepath.Join(d, "hop1"))
	for _, n := range []string{"viachain", "long"} {
		put(t, d, n+"/Cargo.toml", 1)
	}
	os.Symlink("../hop1", filepath.Join(d, "viachain", "target"))
	for i := 1; i <= 10; i++ {
		next := "l" + strconv.Itoa(i+1)
		if i == 10 {
			next = "shared"
		}
		os.Symlink(next, filepath.Join(d, "l"+strconv.Itoa(i)))
	}
	os.Symlink("../l1", filepath.Join(d, "long", "target"))
	got, tl := discover(t, cfg(noRefuse(t)), d, 2)
	if !reflect.DeepEqual(rels(got), []string{"long"}) || got[0].Target != "shared" {
		t.Errorf("got %+v", got)
	}
	if tl.Skips["refused"] != 2 { // .kube listed as a dir, and the chain that ends in it
		t.Errorf("skips: %v", tl.Skips)
	}
}
