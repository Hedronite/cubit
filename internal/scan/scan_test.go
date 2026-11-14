package scan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Hedronite/cubit/internal/config"
	"github.com/Hedronite/cubit/internal/contain"
	"github.com/Hedronite/cubit/internal/walk"
	"github.com/Hedronite/cubit/schemas"
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

// workspace creates a Cargo workspace with target bytes of build output.
func workspace(t *testing.T, base, rel string, n int) {
	t.Helper()
	put(t, base, rel+"/Cargo.toml", 10)
	put(t, base, rel+"/src/main.rs", 10)
	put(t, base, rel+"/target/debug/deps/lib.rlib", n)
	put(t, base, rel+"/target/release/bin", n/2)
	put(t, base, rel+"/target/debug/incremental/x/y.bin", n/4)
}

var fixed = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

func env() Env { return Env{Now: func() time.Time { return fixed }} }

func spec(t *testing.T, roots ...RootSpec) Spec {
	t.Helper()
	ref, err := contain.NewRefuser("/nonexistent-home", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return Spec{Roots: roots, Workers: 4, Refuse: ref}
}

func wsRoot(alias, path string) RootSpec {
	return RootSpec{Alias: alias, Mode: config.WorkspaceRoot, Path: path, MaxFiles: 2_000_000, MaxDuration: 30 * time.Second, DiscoveryDepth: 3, MaxWorkspaces: 64, Profiles: true}
}

func run(t *testing.T, s Spec, e Env) *Document {
	t.Helper()
	d, err := Scan(context.Background(), s, e)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

var (
	scanSchemaOnce sync.Once
	scanSchema     *jsonschema.Schema
)

func schema(t testing.TB) *jsonschema.Schema {
	scanSchemaOnce.Do(func() {
		var meta struct {
			ID string `json:"$id"`
		}
		json.Unmarshal(schemas.Scan, &meta)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemas.Scan))
		if err != nil {
			t.Fatal(err)
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource(meta.ID, doc); err != nil {
			t.Fatal(err)
		}
		if scanSchema, err = c.Compile(meta.ID); err != nil {
			t.Fatal(err)
		}
	})
	return scanSchema
}

// check holds a document to the schema and to the rules a schema cannot say.
func check(t *testing.T, d *Document, tmpDirs ...string) []byte {
	t.Helper()
	b, err := d.JSON()
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema(t).Validate(inst); err != nil {
		t.Fatalf("document violates cubit.scan/v0:\n%v\n%s", err, b)
	}
	for _, dir := range tmpDirs {
		if strings.Contains(string(b), dir) {
			t.Fatalf("the document holds a filesystem path (%s):\n%s", dir, b)
		}
	}
	for _, r := range d.Roots {
		if r.Success && r.Mode != config.CargoHome && r.Mode != config.Sccache {
			var sum int64
			for _, w := range r.Workspaces {
				sum += w.Bytes
				if w.Profiles != nil {
					p := w.Profiles
					if p.Debug+p.Release+p.Doc+p.Incremental+p.Other != w.Bytes {
						t.Errorf("%s/%s: profiles do not sum to the workspace bytes", r.Alias, w.Name)
					}
				}
			}
			if sum+r.WorkspacesUnlabeledBytes != r.Bytes {
				t.Errorf("%s: labelled %d + unlabeled %d != root bytes %d", r.Alias, sum, r.WorkspacesUnlabeledBytes, r.Bytes)
			}
			if len(r.Workspaces)+r.WorkspacesUnlabeled != r.WorkspacesFound {
				t.Errorf("%s: %d labelled + %d unlabeled != %d found", r.Alias, len(r.Workspaces), r.WorkspacesUnlabeled, r.WorkspacesFound)
			}
		}
		if r.Success && (r.Mode == config.CargoHome || r.Mode == config.Sccache) {
			var sum int64
			for _, c := range r.Caches {
				sum += c.Bytes
			}
			if sum != r.Bytes {
				t.Errorf("%s: caches %d != root bytes %d", r.Alias, sum, r.Bytes)
			}
		}
		if !sort.SliceIsSorted(r.Workspaces, func(i, j int) bool { return r.Workspaces[i].Name < r.Workspaces[j].Name }) {
			t.Errorf("%s: workspaces are not sorted by name", r.Alias)
		}
		if r.Partial && !r.Success {
			t.Errorf("%s: partial with success false", r.Alias)
		}
		if r.Partial && (len(r.Errors) == 0 || r.Error != r.Errors[0]) {
			t.Errorf("%s: partial but error %q, errors %v", r.Alias, r.Error, r.Errors)
		}
	}
	return b
}

// du is an independent figure for a directory: lstat blocks, hardlinks once.
func du(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	seen := map[[2]uint64]bool{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st, _ := walk.StatOf(fi)
		if st.Nlink > 1 && !fi.IsDir() {
			k := [2]uint64{st.Dev, st.Ino}
			if seen[k] {
				return nil
			}
			seen[k] = true
		}
		total += int64(st.Blocks) * 512
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

func rootOf(d *Document, alias string) *Root {
	for i := range d.Roots {
		if d.Roots[i].Alias == alias {
			return &d.Roots[i]
		}
	}
	return nil
}

func skipCount(r *Root, reason string) int {
	for _, s := range r.Skipped {
		if s.Reason == reason {
			return s.Count
		}
	}
	return 0
}

func TestWorkspaceRootEndToEnd(t *testing.T) {
	d := tmp(t)
	workspace(t, d, "app", 200_000)
	workspace(t, d, "libs/core", 80_000)
	put(t, d, "notes/readme", 100) // not a workspace
	doc := run(t, spec(t, wsRoot("projects", d)), env())
	check(t, doc, d)
	r := rootOf(doc, "projects")
	if !r.Success || r.Partial || r.Error != "" || r.WorkspacesFound != 2 {
		t.Fatalf("%+v", r)
	}
	if r.DiscoveryDepth == nil || *r.DiscoveryDepth != 3 || !r.ProfilesEmitted {
		t.Errorf("root fields: %+v", r)
	}
	names := []string{r.Workspaces[0].Name, r.Workspaces[1].Name}
	if !reflect.DeepEqual(names, []string{"app", "libs/core"}) {
		t.Errorf("names %v", names)
	}
	if got, want := r.Workspaces[0].Bytes, du(t, filepath.Join(d, "app", "target")); got != want {
		t.Errorf("app is %d, an independent du of its target is %d", got, want)
	}
	p := r.Workspaces[0].Profiles
	if p.Debug == 0 || p.Release == 0 || p.Incremental == 0 {
		t.Errorf("profiles: %+v", p)
	}
	if doc.GeneratedAt != "2026-01-15T12:00:00Z" || r.LastSuccessAt != doc.GeneratedAt {
		t.Errorf("timestamps: %s %s", doc.GeneratedAt, r.LastSuccessAt)
	}
	if len(doc.Warnings) != 0 || doc.ExitCode() != 0 {
		t.Errorf("warnings %v, exit %d", doc.Warnings, doc.ExitCode())
	}
}

func TestProfilesCanBeSwitchedOff(t *testing.T) {
	d := tmp(t)
	workspace(t, d, "app", 100_000)
	rs := wsRoot("p", d)
	rs.Profiles = false
	doc := run(t, spec(t, rs), env())
	check(t, doc, d)
	r := rootOf(doc, "p")
	if r.ProfilesEmitted || r.Workspaces[0].Profiles != nil {
		t.Errorf("profiles must be absent: %+v", r)
	}
}

func TestWorkerCountDoesNotChangeACompleteResult(t *testing.T) {
	d := tmp(t)
	for i := 0; i < 12; i++ {
		workspace(t, d, fmt.Sprintf("w/p%02d", i), 20_000+i*1000)
	}
	os.Link(filepath.Join(d, "w/p00/target/debug/deps/lib.rlib"), filepath.Join(d, "w/p01/target/debug/linked"))
	var first []byte
	for _, workers := range []int{1, 2, 4, 8, 16} {
		s := spec(t, wsRoot("p", d))
		s.Workers = workers
		doc := run(t, s, env())
		doc.Caps.MaxWorkers = 1 // the one field that is the worker count
		b := check(t, doc, d)
		if first == nil {
			first = b
		} else if !bytes.Equal(first, b) {
			t.Fatalf("workers=%d gave a different document", workers)
		}
	}
}

func TestLabelsDeclaredWinCollisionsGetASuffixAndTheCapMovesTheRest(t *testing.T) {
	d := tmp(t)
	workspace(t, d, "a b", 60_000) // "a_b"
	workspace(t, d, "a_b", 50_000) // also "a_b": the later one gets a suffix
	workspace(t, d, "Big", 300_000)
	workspace(t, d, "mid1", 90_000)
	workspace(t, d, "mid2", 90_000) // ties with mid1: broken by name
	workspace(t, d, "tiny", 1_000)
	rs := wsRoot("p", d)
	rs.Names = map[string]string{"Big": "huge", "ghost": "nobody", "tiny": "t"}
	doc := run(t, spec(t, rs), env())
	check(t, doc, d)
	r := rootOf(doc, "p")
	got := map[string]bool{}
	for _, w := range r.Workspaces {
		got[w.Name] = true
	}
	for _, want := range []string{"huge", "t", "a_b", "mid1", "mid2"} {
		if !got[want] {
			t.Errorf("missing label %q in %v", want, got)
		}
	}
	var suffixed int
	re := regexp.MustCompile(`^a_b_[0-9a-f]{8}$`)
	for n := range got {
		if re.MatchString(n) {
			suffixed++
		}
	}
	if suffixed != 1 {
		t.Errorf("exactly one of the colliding pair gets the suffix: %v", got)
	}
	if len(doc.Warnings) != 1 || doc.Warnings[0] != (Warning{Root: "p", Reason: "unmatched_name", Count: 1}) {
		t.Errorf("warnings: %+v", doc.Warnings)
	}

	// Cap at 3: the three biggest keep labels, the rest move to unlabeled.
	rs.MaxWorkspaces = 3
	doc = run(t, spec(t, rs), env())
	check(t, doc, d)
	r = rootOf(doc, "p")
	if len(r.Workspaces) != 3 || r.WorkspacesUnlabeled != 3 || r.WorkspacesFound != 6 {
		t.Fatalf("%+v", r)
	}
	for _, w := range r.Workspaces {
		if w.Name != "huge" && w.Name != "mid1" && w.Name != "mid2" {
			t.Errorf("the three largest keep labels, got %q", w.Name)
		}
	}
	if r.WorkspacesUnlabeledBytes == 0 {
		t.Error("the unlabeled bytes are reported, not dropped")
	}
	for _, w := range r.Workspaces {
		if w.Name == "other" {
			t.Error("there is no other bucket")
		}
	}
}

func TestOneTargetIsOwnedByTheFirstWorkspaceAndTheFirstRoot(t *testing.T) {
	d := tmp(t)
	put(t, d, "shared/debug/x", 100_000)
	for _, n := range []string{"one", "two"} {
		put(t, d, n+"/Cargo.toml", 1)
		os.Symlink("../shared", filepath.Join(d, n, "target"))
	}
	doc := run(t, spec(t, wsRoot("a", d)), env())
	check(t, doc, d)
	r := rootOf(doc, "a")
	if r.WorkspacesFound != 1 || r.Workspaces[0].Name != "one" || skipCount(r, "duplicate_target") != 1 {
		t.Fatalf("the first workspace in path order owns it: %+v", r)
	}
	if r.Partial {
		t.Error("a duplicate target does not make a root partial")
	}

	// Across roots: alias order decides. Scan runs roots sorted, whatever the spec order.
	other := RootSpec{Alias: "z-shared", Mode: config.TargetDir, Path: filepath.Join(d, "shared"), MaxFiles: 2_000_000, MaxDuration: 30 * time.Second, Name: "shared", Profiles: true}
	doc = run(t, spec(t, other, wsRoot("a", d)), env())
	check(t, doc, d)
	a, z := rootOf(doc, "a"), rootOf(doc, "z-shared")
	if a.WorkspacesFound != 1 || z.WorkspacesFound != 0 || skipCount(z, "duplicate_target") != 1 || z.Bytes != 0 {
		t.Errorf("a: %+v\nz: %+v", a, z)
	}
	if !z.Success {
		t.Error("a target owned by an earlier root is not a failure")
	}
}

func TestTargetDirMode(t *testing.T) {
	d := tmp(t)
	put(t, d, "debug/a", 100_000)
	put(t, d, "release/b", 60_000)
	put(t, d, "debug/incremental/c", 30_000)
	rs := RootSpec{Alias: "shared", Mode: config.TargetDir, Path: d, MaxFiles: 2_000_000, MaxDuration: time.Minute, Name: "ci-shared", Profiles: true}
	doc := run(t, spec(t, rs), env())
	check(t, doc, d)
	r := rootOf(doc, "shared")
	if r.WorkspacesFound != 1 || r.Workspaces[0].Name != "ci-shared" || r.DiscoveryDepth != nil {
		t.Fatalf("%+v", r)
	}
	if r.Bytes != du(t, d) {
		t.Errorf("%d vs du %d", r.Bytes, du(t, d))
	}
}

func TestCargoHomeAndSccache(t *testing.T) {
	d := tmp(t)
	put(t, d, "registry/cache/idx/a.crate", 50_000)
	put(t, d, "registry/src/idx/a/lib.rs", 20_000)
	put(t, d, "git/db/r/objects", 30_000)
	put(t, d, "bin/tool", 10_000)
	put(t, d, "env", 100) // not part of any area: counted nowhere
	rs := RootSpec{Alias: "ch", Mode: config.CargoHome, Path: d, MaxFiles: 2_000_000, MaxDuration: time.Minute}
	doc := run(t, spec(t, rs), env())
	check(t, doc, d)
	r := rootOf(doc, "ch")
	kinds := map[string]int64{}
	for _, c := range r.Caches {
		kinds[c.Kind] = c.Bytes
	}
	if len(kinds) != 6 || kinds["registry-cache"] == 0 || kinds["git-checkouts"] != 0 || kinds["registry-index"] != 0 {
		t.Errorf("kinds: %v", kinds)
	}
	if r.ProfilesEmitted || r.WorkspacesFound != 0 || r.DiscoveryDepth != nil {
		t.Errorf("%+v", r)
	}
	if want := du(t, filepath.Join(d, "registry", "cache")); kinds["registry-cache"] != want {
		t.Errorf("registry-cache %d vs du %d", kinds["registry-cache"], want)
	}

	s := tmp(t)
	put(t, s, "ab/cd/entry", 40_000)
	doc = run(t, spec(t, RootSpec{Alias: "sc", Mode: config.Sccache, Path: s, MaxFiles: 2_000_000, MaxDuration: time.Minute}), env())
	check(t, doc, s)
	r = rootOf(doc, "sc")
	if len(r.Caches) != 1 || r.Caches[0].Kind != "sccache" || r.Bytes != du(t, s) {
		t.Errorf("%+v vs du %d", r, du(t, s))
	}
}

// A symlinked area is never followed: it is reported, and measures zero.
func TestCargoHomeDoesNotFollowASymlinkedArea(t *testing.T) {
	d, out := tmp(t), tmp(t)
	put(t, out, "huge/crate", 900_000)
	os.MkdirAll(filepath.Join(d, "registry"), 0o755)
	os.Symlink(filepath.Join(out, "huge"), filepath.Join(d, "registry", "cache"))
	put(t, d, "registry/src/x", 10_000)
	rs := RootSpec{Alias: "ch", Mode: config.CargoHome, Path: d, MaxFiles: 2_000_000, MaxDuration: time.Minute}
	doc := run(t, spec(t, rs), env())
	check(t, doc, d, out)
	r := rootOf(doc, "ch")
	for _, c := range r.Caches {
		if c.Kind == "registry-cache" && c.Bytes != 0 {
			t.Errorf("a symlinked area leaked %d bytes", c.Bytes)
		}
	}
	if skipCount(r, "symlink_escape") != 1 || r.Partial {
		t.Errorf("%+v", r)
	}
}

func TestFailedRootsStillAppearAndTheOthersServe(t *testing.T) {
	good := tmp(t)
	workspace(t, good, "app", 50_000)
	file := filepath.Join(good, "app", "Cargo.toml")
	missing := filepath.Join(good, "nope")
	refused := tmp(t)
	put(t, refused, "x", 10)
	s := spec(t,
		wsRoot("good", good),
		RootSpec{Alias: "gone", Mode: config.Sccache, Path: missing, MaxFiles: 2_000_000, MaxDuration: time.Minute},
		RootSpec{Alias: "afile", Mode: config.TargetDir, Path: file, MaxFiles: 2_000_000, MaxDuration: time.Minute, Name: "x"},
		RootSpec{Alias: "refused", Mode: config.Sccache, Path: refused, MaxFiles: 2_000_000, MaxDuration: time.Minute},
	)
	s.Refuse, _ = contain.NewRefuser("/nonexistent-home", []string{refused}, "")
	doc := run(t, s, env())
	check(t, doc, good, refused)
	for alias, want := range map[string]string{"gone": "root_missing", "afile": "root_unreadable", "refused": "root_unreadable"} {
		r := rootOf(doc, alias)
		if r == nil || r.Success || r.Partial || r.Error != want || r.Bytes != 0 || r.LastSuccessAt != "" {
			t.Errorf("%s: %+v, want a failed root with %s", alias, r, want)
		}
	}
	if r := rootOf(doc, "good"); !r.Success || r.WorkspacesFound != 1 {
		t.Errorf("good: %+v", r)
	}
	if doc.ExitCode() != 1 {
		t.Errorf("exit %d, want 1 (mixed)", doc.ExitCode())
	}
}

func TestExitCodes(t *testing.T) {
	ok := Root{Success: true}
	partial := Root{Success: true, Partial: true}
	failed := Root{}
	for name, c := range map[string]struct {
		roots []Root
		want  int
	}{
		"all ok":           {[]Root{ok, ok}, 0},
		"one partial":      {[]Root{ok, partial}, 1},
		"all partial":      {[]Root{partial, partial}, 1},
		"one failed":       {[]Root{ok, failed}, 1},
		"partial and fail": {[]Root{partial, failed}, 1},
		"all failed":       {[]Root{failed, failed}, 2},
		"one root failed":  {[]Root{failed}, 2},
		"one root ok":      {[]Root{ok}, 0},
		"one root partial": {[]Root{partial}, 1},
	} {
		if got := (&Document{Roots: c.roots}).ExitCode(); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}

func TestNoWorkspacesWarns(t *testing.T) {
	d := tmp(t)
	put(t, d, "src/readme", 1)
	doc := run(t, spec(t, wsRoot("p", d)), env())
	check(t, doc, d)
	r := rootOf(doc, "p")
	if !r.Success || r.Partial || r.Bytes != 0 || r.WorkspacesFound != 0 {
		t.Errorf("%+v", r)
	}
	if len(doc.Warnings) != 1 || doc.Warnings[0] != (Warning{Root: "p", Reason: "no_workspaces", Count: 1}) {
		t.Errorf("warnings: %+v", doc.Warnings)
	}
	if doc.ExitCode() != 0 {
		t.Error("a warning is not an error")
	}
}

func TestPermissionDeniedMakesARootPartialAndPublishesALowerBound(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	d := tmp(t)
	workspace(t, d, "app", 100_000)
	put(t, d, "app/target/debug/locked/inner", 200_000)
	full := rootOf(run(t, spec(t, wsRoot("p", d)), env()), "p").Bytes
	lock := filepath.Join(d, "app", "target", "debug", "locked")
	os.Chmod(lock, 0)
	t.Cleanup(func() { os.Chmod(lock, 0o755) })
	doc := run(t, spec(t, wsRoot("p", d)), env())
	check(t, doc, d)
	r := rootOf(doc, "p")
	if !r.Success || !r.Partial || r.Error != "subtree_unreadable" || skipCount(r, "permission_denied") != 1 {
		t.Fatalf("%+v", r)
	}
	if r.Bytes >= full {
		t.Errorf("a partial root publishes a lower bound: %d vs full %d", r.Bytes, full)
	}
	if doc.ExitCode() != 1 {
		t.Errorf("exit %d", doc.ExitCode())
	}
}

// A refused subtree and a symlink are skipped by design: never partial.
func TestRefusedAndForeignAreNotPartial(t *testing.T) {
	d := tmp(t)
	workspace(t, d, "app", 100_000)
	put(t, d, "app/target/debug/.ssh/key", 50_000)
	put(t, d, "app/target/mnt/big", 50_000)
	e := env()
	e.Stat = func(fi fs.FileInfo) (walk.Stat, bool) {
		st, ok := walk.StatOf(fi)
		if fi.Name() == "mnt" {
			st.Dev++
		}
		return st, ok
	}
	doc := run(t, spec(t, wsRoot("p", d)), e)
	check(t, doc, d)
	r := rootOf(doc, "p")
	if r.Partial || skipCount(r, "refused") != 1 || skipCount(r, "foreign_device") != 1 {
		t.Errorf("%+v", r)
	}
}

// Caps give partial results, which are only ever lower bounds: tests assert
// invariants, never exact bytes, because which units finish before a cap trips
// depends on scheduling.
func TestCapsGiveLowerBoundsNotExactBytes(t *testing.T) {
	d := tmp(t)
	for i := 0; i < 20; i++ {
		workspace(t, d, fmt.Sprintf("w%02d", i), 30_000)
	}
	full := rootOf(run(t, spec(t, wsRoot("p", d)), env()), "p")

	rs := wsRoot("p", d)
	rs.MaxFiles = 1000 // honours the schema minimum, well under the tree's size
	// 20 workspaces x ~10 entries is under 1000, so make the tree bigger than the cap
	for i := 0; i < 20; i++ {
		for j := 0; j < 60; j++ {
			put(t, d, fmt.Sprintf("w%02d/target/debug/deps/f%03d", i, j), 100)
		}
	}
	doc := run(t, spec(t, rs), env())
	check(t, doc, d)
	r := rootOf(doc, "p")
	if !r.Success || !r.Partial || r.Error != "max_files_exceeded" {
		t.Fatalf("%+v", r)
	}
	if r.Bytes > full.Bytes+20*60*4096 {
		t.Errorf("a capped scan is no larger than the full one: %d vs %d", r.Bytes, full.Bytes)
	}
	if doc.ExitCode() != 1 {
		t.Errorf("exit %d", doc.ExitCode())
	}

	// A deadline, on a clock that advances a second per reading.
	var mu sync.Mutex
	now := fixed
	e := Env{Now: func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Second)
		return now
	}}
	rs = wsRoot("p", d)
	rs.MaxDuration = 3 * time.Second
	doc = run(t, spec(t, rs), e)
	check(t, doc, d)
	r = rootOf(doc, "p")
	if !r.Partial || r.Errors[0] != "deadline_exceeded" {
		t.Errorf("%+v", r)
	}
}

func TestErrorOrderingWhenSeveralApply(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	d := tmp(t)
	for i := 0; i < 30; i++ {
		workspace(t, d, fmt.Sprintf("w%02d", i), 10_000)
		for j := 0; j < 40; j++ {
			put(t, d, fmt.Sprintf("w%02d/target/debug/deps/f%03d", i, j), 10)
		}
	}
	put(t, d, "w00/target/debug/locked/x", 100)
	os.Chmod(filepath.Join(d, "w00/target/debug/locked"), 0)
	t.Cleanup(func() { os.Chmod(filepath.Join(d, "w00/target/debug/locked"), 0o755) })
	rs := wsRoot("p", d)
	rs.MaxFiles = 1000
	var mu sync.Mutex
	now := fixed
	e := Env{Now: func() time.Time { mu.Lock(); defer mu.Unlock(); now = now.Add(time.Second); return now }}
	rs.MaxDuration = 2 * time.Second
	doc := run(t, spec(t, rs), e)
	r := rootOf(doc, "p")
	want := []string{"deadline_exceeded", "max_files_exceeded"}
	var got []string
	for _, x := range r.Errors {
		if x != "subtree_unreadable" {
			got = append(got, x)
		}
	}
	if len(got) == 0 || got[0] != "deadline_exceeded" && got[0] != "max_files_exceeded" {
		t.Fatalf("errors %v", r.Errors)
	}
	order := map[string]int{"root_missing": 0, "root_unreadable": 1, "deadline_exceeded": 2, "max_files_exceeded": 3, "subtree_unreadable": 4}
	for i := 1; i < len(r.Errors); i++ {
		if order[r.Errors[i-1]] > order[r.Errors[i]] {
			t.Errorf("errors out of the schema's order: %v (want a subsequence of %v)", r.Errors, want)
		}
	}
}

func TestACanceledScanPublishesNothing(t *testing.T) {
	d := tmp(t)
	workspace(t, d, "app", 10_000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	doc, err := Scan(ctx, spec(t, wsRoot("p", d)), env())
	if err == nil || doc != nil {
		t.Errorf("canceled scan returned %v %v", doc, err)
	}
}

func TestHostAppearsOnlyWhenSet(t *testing.T) {
	d := tmp(t)
	workspace(t, d, "app", 10_000)
	s := spec(t, wsRoot("p", d))
	if b := check(t, run(t, s, env()), d); bytes.Contains(b, []byte(`"host"`)) {
		t.Error("host is omitted when unset")
	}
	s.Host = "build-example"
	if b := check(t, run(t, s, env()), d); !bytes.Contains(b, []byte(`"host": "build-example"`)) {
		t.Error("host is carried through")
	}
}

func TestFromConfig(t *testing.T) {
	d := tmp(t)
	cfg, fs := config.Parse([]byte("schema: cubit.config/v0\nhost: h1\nscan: {max_workers: 2}\nroots:\n  - {alias: a, path: " + d + ", mode: workspace-root, discovery_depth: 2, profiles: false}\n  - {alias: t, path: ~/x, mode: target-dir, name: shared}\n"))
	if cfg == nil {
		t.Fatalf("%+v", fs)
	}
	s, err := FromConfig(cfg, "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if s.Host != "h1" || s.Workers != 2 || len(s.Roots) != 2 {
		t.Fatalf("%+v", s)
	}
	a, tt := s.Roots[0], s.Roots[1]
	if a.DiscoveryDepth != 2 || a.Profiles || a.MaxFiles != 2_000_000 || a.MaxDuration != 30*time.Second || a.MaxWorkspaces != 64 {
		t.Errorf("%+v", a)
	}
	if tt.Path != "/home/u/x" || tt.Name != "shared" || !tt.Profiles {
		t.Errorf("%+v", tt)
	}
}

func TestManyWorkspacesUnderRace(t *testing.T) {
	d := tmp(t)
	for i := 0; i < 40; i++ {
		workspace(t, d, fmt.Sprintf("g%d/w%02d", i%4, i), 12_000)
	}
	s := spec(t, wsRoot("p", d))
	s.Workers = 8
	doc := run(t, s, env())
	check(t, doc, d)
	if r := rootOf(doc, "p"); r.WorkspacesFound != 40 || r.Partial {
		t.Errorf("%+v", r)
	}
}

func TestRelPathsNeverReachTheDocumentAsPaths(t *testing.T) {
	d := tmp(t)
	workspace(t, d, "Weird Name/ünïcode/p", 10_000)
	doc := run(t, spec(t, wsRoot("p", d)), env())
	b := check(t, doc, d)
	_ = b
	r := rootOf(doc, "p")
	if got := r.Workspaces[0].Name; got != "weird_name/__n__code/p" || !regexp.MustCompile(`^[a-z0-9._-]+(/[a-z0-9._-]+)*$`).MatchString(got) {
		t.Errorf("label %q", got)
	}
}
