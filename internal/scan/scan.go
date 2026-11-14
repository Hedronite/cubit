package scan

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Hedronite/cubit/internal/config"
	"github.com/Hedronite/cubit/internal/contain"
	"github.com/Hedronite/cubit/internal/reason"
	"github.com/Hedronite/cubit/internal/walk"
)

// Default bounds that are not per-root config.
const (
	DefaultDepthGuard = config.DepthGuard
	DefaultDedupeMax  = 1 << 20
)

// RootSpec is one root, ready to scan: its path expanded and every default applied.
type RootSpec struct {
	Alias          string
	Mode           string
	Path           string // absolute
	MaxFiles       int64
	MaxDuration    time.Duration
	DiscoveryDepth int
	MaxWorkspaces  int
	Names          map[string]string
	Name           string
	Profiles       bool
}

// Spec is everything Scan needs.
type Spec struct {
	Host       string
	Roots      []RootSpec
	Workers    int
	Refuse     *contain.Refuser
	DepthGuard int
	DedupeMax  int
	Log        *slog.Logger // nil is silent
}

// Env is what a test replaces: the clock and the stat seam.
type Env struct {
	Now  func() time.Time
	Stat walk.StatFunc
}

// FromConfig expands and defaults a validated config.
func FromConfig(c *config.Config, home string) (Spec, error) {
	ref, err := c.Refuser(home)
	if err != nil {
		return Spec{}, err
	}
	s := Spec{Host: c.Host, Workers: c.EffectiveWorkers(), Refuse: ref, DepthGuard: DefaultDepthGuard, DedupeMax: DefaultDedupeMax}
	for i := range c.Roots {
		r := &c.Roots[i]
		abs, err := config.ExpandPath(r.Path, home)
		if err != nil {
			return Spec{}, err
		}
		s.Roots = append(s.Roots, RootSpec{
			Alias: r.Alias, Mode: r.Mode, Path: abs,
			MaxFiles: r.EffectiveMaxFiles(), MaxDuration: r.EffectiveMaxDuration(),
			DiscoveryDepth: r.EffectiveDiscoveryDepth(), MaxWorkspaces: r.EffectiveMaxWorkspaces(),
			Names: r.Names, Name: r.WorkspaceName(), Profiles: r.EmitProfiles(),
		})
	}
	return s, nil
}

type inode struct{ dev, ino uint64 }

// Scan measures every root once, in alias order, and returns the document. A
// root that cannot be read is reported in the document, not as an error; the
// error is only ever ctx's.
func Scan(ctx context.Context, spec Spec, env Env) (*Document, error) {
	if env.Now == nil {
		env.Now = time.Now
	}
	if spec.DepthGuard == 0 {
		spec.DepthGuard = DefaultDepthGuard
	}
	if spec.DedupeMax == 0 {
		spec.DedupeMax = DefaultDedupeMax
	}
	if spec.Workers < 1 {
		spec.Workers = 1
	}
	roots := append([]RootSpec(nil), spec.Roots...)
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].Alias < roots[j].Alias })

	doc := &Document{
		APIVersion: APIVersion,
		Host:       spec.Host,
		Roots:      []Root{},
		Warnings:   []Warning{},
		Caps:       DocCaps{MaxWorkers: spec.Workers, DepthGuard: spec.DepthGuard},
	}
	claimed := map[inode]bool{}
	for _, rs := range roots {
		root, warns, err := scanRoot(ctx, spec, env, rs, claimed)
		if err != nil {
			return nil, err
		}
		doc.Roots = append(doc.Roots, root)
		doc.Warnings = append(doc.Warnings, warns...)
	}
	now := rfc3339(env.Now())
	doc.GeneratedAt = now
	for i := range doc.Roots {
		if doc.Roots[i].Success {
			doc.Roots[i].LastSuccessAt = now
		}
	}
	sort.SliceStable(doc.Warnings, func(i, j int) bool {
		a, b := doc.Warnings[i], doc.Warnings[j]
		if a.Root != b.Root {
			return a.Root < b.Root
		}
		return a.Reason < b.Reason
	})
	return doc, nil
}

// unit is one thing walked by one goroutine.
type unit struct {
	rel    string   // workspace rel path as on disk, or the cache kind
	open   []string // components below top to open; empty means top itself
	dev    uint64
	ino    uint64
	checkI bool // verify the identity after opening
}

type unitResult struct {
	unit   unit
	tally  walk.Tally
	absent bool  // the directory is not there: zero bytes, and not a workspace
	err    error // the unit's top directory could not be listed
}

func scanRoot(ctx context.Context, spec Spec, env Env, rs RootSpec, claimed map[inode]bool) (Root, []Warning, error) {
	start := env.Now()
	budget := walk.NewBudgetAt(start, rs.MaxFiles, rs.MaxDuration, env.Now)
	wc := walk.Config{Refuse: spec.Refuse, Stat: env.Stat, Budget: budget, DepthGuard: spec.DepthGuard, DedupeMax: spec.DedupeMax}
	if spec.Log != nil {
		wc.Log = &walk.Logger{L: spec.Log.With("alias", rs.Alias)}
		defer wc.Log.Done()
	}

	out := Root{
		Alias: rs.Alias, Mode: rs.Mode,
		Caps:       RootCaps{MaxFiles: rs.MaxFiles, MaxDurationSeconds: rs.MaxDuration.Seconds()},
		Workspaces: []Workspace{}, Caches: []Cache{}, Skipped: []Skip{},
	}
	if rs.Mode == config.WorkspaceRoot {
		d := rs.DiscoveryDepth
		out.DiscoveryDepth = &d
	}
	fail := func(reason string) (Root, []Warning, error) {
		out.Success, out.Partial, out.Error, out.Errors = false, false, reason, []string{reason}
		out.DurationSeconds = round3(env.Now().Sub(start).Seconds())
		return out, nil, nil
	}

	top, real, err := contain.OpenRoot(rs.Path, spec.Refuse)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fail(reason.RootMissing)
		}
		return fail(reason.RootUnreadable)
	}
	defer top.Close()
	comps := contain.Components(real)

	skips := map[string]int{}
	var warns []Warning
	var units []unit

	switch rs.Mode {
	case config.WorkspaceRoot:
		var dt walk.Tally
		ws, err := wc.Discover(ctx, top, comps, rs.DiscoveryDepth, &dt)
		if err != nil {
			if ctx.Err() != nil {
				return Root{}, nil, ctx.Err()
			}
			return fail(reason.RootUnreadable)
		}
		mergeSkips(skips, dt.Skips)
		matched := map[string]bool{}
		for _, w := range ws {
			matched[w.Rel] = true
			id := inode{w.Dev, w.Ino}
			if claimed[id] {
				skips[reason.DuplicateTarget]++
				continue
			}
			claimed[id] = true
			units = append(units, unit{rel: w.Rel, open: strings.Split(w.Target, "/"), dev: w.Dev, ino: w.Ino, checkI: true})
		}
		var unmatched int
		for k := range rs.Names {
			if !matched[k] {
				unmatched++
			}
		}
		if unmatched > 0 {
			warns = append(warns, Warning{Root: rs.Alias, Reason: reason.UnmatchedName, Count: unmatched})
		}
		if len(ws) == 0 && budget.Alive() {
			warns = append(warns, Warning{Root: rs.Alias, Reason: reason.NoWorkspaces, Count: 1})
		}
	case config.TargetDir:
		fi, err := top.Stat(".")
		st, ok := walkStat(env, fi)
		if err != nil || !ok {
			return fail(reason.RootUnreadable)
		}
		id := inode{st.Dev, st.Ino}
		if claimed[id] {
			skips[reason.DuplicateTarget]++
		} else {
			claimed[id] = true
			units = append(units, unit{rel: rs.Name})
		}
	case config.CargoHome:
		for _, a := range cargoAreas {
			units = append(units, unit{rel: a.kind, open: a.path})
		}
	case config.Sccache:
		units = append(units, unit{rel: "sccache"})
	}

	results, err := measure(ctx, spec, wc, top, comps, units)
	if err != nil {
		return Root{}, nil, err
	}
	// A target-dir root that cannot even be listed has nothing to show.
	if rs.Mode == config.TargetDir && len(results) == 1 && results[0].err != nil {
		return fail(reason.RootUnreadable)
	}
	for _, r := range results {
		mergeSkips(skips, r.tally.Skips)
		if r.err != nil {
			skips[contain.SkipReason(r.err)]++
		}
	}

	switch rs.Mode {
	case config.WorkspaceRoot, config.TargetDir:
		labelWorkspaces(&out, rs, results)
	case config.CargoHome, config.Sccache:
		for _, r := range results {
			out.Caches = append(out.Caches, Cache{Kind: r.unit.rel, Bytes: r.tally.Bytes})
			out.Bytes += r.tally.Bytes
		}
		sort.Slice(out.Caches, func(i, j int) bool { return out.Caches[i].Kind < out.Caches[j].Kind })
	}

	out.Skipped = skipList(skips)
	out.Success = true
	if budget.DeadlineExceeded() {
		out.Errors = append(out.Errors, reason.DeadlineExceeded)
	}
	if budget.FilesExceeded() {
		out.Errors = append(out.Errors, reason.MaxFilesExceeded)
	}
	for k, n := range skips {
		if n > 0 && reason.MakesPartial(k) {
			out.Errors = append(out.Errors, reason.SubtreeUnreadable)
			break
		}
	}
	if len(out.Errors) > 0 {
		out.Partial = true
		out.Error = out.Errors[0]
	}
	out.DurationSeconds = round3(env.Now().Sub(start).Seconds())
	return out, warns, nil
}

func walkStat(env Env, fi fs.FileInfo) (walk.Stat, bool) {
	if fi == nil {
		return walk.Stat{}, false
	}
	if env.Stat != nil {
		return env.Stat(fi)
	}
	return walk.StatOf(fi)
}

func mergeSkips(into, from map[string]int) {
	for k, v := range from {
		into[k] += v
	}
}

func skipList(m map[string]int) []Skip {
	out := []Skip{}
	for k, v := range m {
		if v > 0 {
			out = append(out, Skip{Reason: k, Count: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reason < out[j].Reason })
	return out
}

// cargoAreas are the fixed parts of CARGO_HOME, by kind.
var cargoAreas = []struct {
	kind string
	path []string
}{
	{"cargo-bin", []string{"bin"}},
	{"git-checkouts", []string{"git", "checkouts"}},
	{"git-db", []string{"git", "db"}},
	{"registry-cache", []string{"registry", "cache"}},
	{"registry-index", []string{"registry", "index"}},
	{"registry-src", []string{"registry", "src"}},
}

// measure walks every unit on a pool, one goroutine per unit, and returns the
// results in unit order.
func measure(ctx context.Context, spec Spec, wc walk.Config, top *os.Root, comps []string, units []unit) ([]unitResult, error) {
	results := make([]unitResult, len(units))
	sem := make(chan struct{}, spec.Workers)
	var wg sync.WaitGroup
	for i := range units {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = measureUnit(ctx, spec, wc, top, comps, units[i])
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func measureUnit(ctx context.Context, spec Spec, wc walk.Config, top *os.Root, comps []string, u unit) unitResult {
	res := unitResult{unit: u}
	skip := func(reason string) { res.tally.Skips = map[string]int{reason: 1} }
	abs := append(append(make([]string, 0, len(comps)+len(u.open)+4), comps...), u.open...)
	if _, bad := spec.Refuse.Inside(abs); len(u.open) > 0 && bad {
		skip(reason.Refused)
		res.absent = u.checkI
		return res
	}
	root, err := openUnit(top, u)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			res.absent = true // an absent cache area is zero bytes, not a skip
			if u.checkI {
				skip(reason.Vanished)
			}
			return res
		}
		res.absent = u.checkI
		skip(contain.SkipReason(err))
		return res
	}
	defer root.Close()
	if u.checkI {
		fi, err := root.Stat(".")
		st, ok := walkStat(Env{Stat: wc.Stat}, fi)
		if err != nil || !ok || st.Dev != u.dev || st.Ino != u.ino {
			// Discovery admitted this directory, and now it is a different one:
			// a swap, not a plain removal. Its bytes are unmeasured, so fail closed.
			res.absent = true
			if wc.Log != nil && ok {
				wc.Log.Swap(len(u.open))
			}
			skip(reason.IOError)
			return res
		}
	}
	res.tally, res.err = wc.Measure(ctx, root, abs)
	return res
}

// openUnit opens the unit's directory. A target found by discovery is opened
// through top in one step (os.Root resolves a symlinked target inside the
// root). A cache area is opened one real directory at a time, so a symlink
// anywhere on the way is never followed.
func openUnit(top *os.Root, u unit) (*os.Root, error) {
	if len(u.open) == 0 {
		return top.OpenRoot(".")
	}
	if u.checkI {
		return top.OpenRoot(strings.Join(u.open, "/"))
	}
	cur := top
	for _, c := range u.open {
		next, _, err := contain.Child(cur, c)
		if cur != top {
			_ = cur.Close()
		}
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}
