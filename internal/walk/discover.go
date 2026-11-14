package walk

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/Hedronite/cubit/internal/contain"
	"github.com/Hedronite/cubit/internal/reason"
)

// Workspace is one discovered Cargo workspace: a directory holding Cargo.toml
// and a target directory.
type Workspace struct {
	// Rel is the workspace directory below the discovery root, slash separated,
	// exactly as on disk. It is never used to open anything.
	Rel string
	// Target is the target directory below the discovery root, slash separated.
	// It differs from Rel+"/target" when target is a symlink that stays inside
	// the root.
	Target string
	// Dev and Ino identify the target directory, for duplicate detection.
	Dev, Ino uint64
}

// skipWalk are directories discovery never enters.
// readChunk is how many entries one directory read asks for, so a caller's caps
// are checked often enough that a huge directory cannot overshoot them far.
const readChunk = 256

var skipWalk = map[string]bool{"target": true, ".git": true, "node_modules": true}

type discoverer struct {
	ctx      context.Context
	cfg      Config
	top      *os.Root
	topComps []string
	dev      uint64
	comps    []string // absolute components of the directory being read
	max      int
	t        *Tally
	found    []Workspace
}

// Discover finds the workspaces up to maxDepth directories below top (children
// of top are depth 1). The result is sorted by Rel. A symlinked target is
// resolved once and admitted only if it stays inside top. Entries read count
// against the budget, and every directory entry is checked against the refuse
// list before anything is opened. An error means top itself could not be read.
func (c Config) Discover(ctx context.Context, top *os.Root, topComps []string, maxDepth int, t *Tally) ([]Workspace, error) {
	fi, err := top.Stat(".")
	if err != nil {
		return nil, err
	}
	st, ok := c.stat(fi)
	if !ok {
		return nil, fs.ErrInvalid
	}
	d := &discoverer{
		ctx: ctx, cfg: c, top: top, dev: st.Dev, max: maxDepth, t: t,
		topComps: append([]string(nil), topComps...),
		comps:    append(make([]string, 0, len(topComps)+16), topComps...),
	}
	if err := d.dir(top, nil, 0, true); err != nil {
		return nil, err
	}
	sort.Slice(d.found, func(i, j int) bool { return d.found[i].Rel < d.found[j].Rel })
	return d.found, nil
}

func (d *discoverer) read(dir *os.Root) ([]fs.DirEntry, error) {
	f, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var ents []fs.DirEntry
	for {
		chunk, err := f.ReadDir(readChunk)
		if len(chunk) > 0 {
			ents = append(ents, chunk...)
			if !d.cfg.Budget.Entries(len(chunk)) {
				return nil, nil
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
			return ents, err
		}
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	return ents, nil
}

func (d *discoverer) dir(dir *os.Root, rel []string, depth int, top bool) error {
	ents, err := d.read(dir)
	if err != nil && len(ents) == 0 {
		if top {
			return err
		}
		d.t.skip(contain.SkipReason(err))
		return nil
	}
	if err != nil {
		d.t.skip(contain.SkipReason(err))
	}
	if !d.cfg.Budget.Alive() {
		return nil
	}
	if depth >= 1 {
		d.workspace(dir, rel, ents)
	}
	if depth >= d.max {
		return nil
	}
	for i, ent := range ents {
		if i&255 == 255 {
			if err := d.ctx.Err(); err != nil {
				return err
			}
			if !d.cfg.Budget.Alive() {
				return nil
			}
		}
		name := ent.Name()
		if !ent.IsDir() || skipWalk[name] {
			continue
		}
		if err := d.child(dir, rel, depth, name); err != nil {
			return err
		}
	}
	return nil
}

func (d *discoverer) child(dir *os.Root, rel []string, depth int, name string) error {
	d.comps = append(d.comps, name)
	defer func() { d.comps = d.comps[:len(d.comps)-1] }()
	if d.cfg.Refuse.Refused(d.comps) {
		d.t.skip(reason.Refused)
		return nil
	}
	fi, err := dir.Lstat(name)
	if err != nil {
		d.t.skip(contain.SkipReason(err))
		return nil
	}
	st, ok := d.cfg.stat(fi)
	if !ok {
		d.t.skip(reason.IOError)
		return nil
	}
	if st.Dev != d.dev {
		d.t.skip(reason.ForeignDevice)
		return nil
	}
	if d.cfg.DepthGuard > 0 && depth+1 > d.cfg.DepthGuard {
		d.t.skip(reason.DepthExceeded)
		return nil
	}
	sub, err := contain.ChildWith(dir, name, fi)
	if err != nil {
		d.t.skip(contain.SkipReason(err))
		return nil
	}
	defer sub.Close()
	return d.dir(sub, append(rel[:len(rel):len(rel)], name), depth+1, false)
}

// workspace looks at one directory's own listing: Cargo.toml present and not a
// directory, and target either a directory or a symlink to one inside top.
func (d *discoverer) workspace(dir *os.Root, rel []string, ents []fs.DirEntry) {
	var cargo, target fs.DirEntry
	for _, e := range ents {
		switch e.Name() {
		case "Cargo.toml":
			cargo = e
		case "target":
			target = e
		}
	}
	if cargo == nil || target == nil || cargo.IsDir() {
		return
	}
	d.comps = append(d.comps, "target")
	defer func() { d.comps = d.comps[:len(d.comps)-1] }()
	if d.cfg.Refuse.Refused(d.comps) {
		d.t.skip(reason.Refused)
		return
	}
	relDir := strings.Join(rel, "/")
	switch {
	case target.IsDir():
		d.admit(relDir, path.Join(relDir, "target"))
	case target.Type()&fs.ModeSymlink != 0:
		d.symlinked(relDir)
	}
}

// symlinked resolves a symlinked target through contain.Resolve, which follows
// every hop inside top and runs the refuse list on each one. A link that is
// absolute, leaves top, loops, or lands on a refused path is not admitted. A
// link that points back at the workspace or above it is a loop.
func (d *discoverer) symlinked(relDir string) {
	final, err := contain.Resolve(d.top, d.topComps, relDir+"/target", d.cfg.Refuse)
	if err != nil {
		d.t.skip(contain.SkipReason(err))
		return
	}
	if final == "" || final == relDir || strings.HasPrefix(relDir+"/", final+"/") {
		d.t.skip(reason.SymlinkLoop)
		return
	}
	d.admit(relDir, final)
}

// admit opens the target through top to record its identity, then closes it.
func (d *discoverer) admit(relDir, target string) {
	fi, err := d.top.Stat(target)
	if err != nil {
		d.t.skip(contain.SkipReason(err))
		return
	}
	if !fi.IsDir() {
		return
	}
	sub, err := d.top.OpenRoot(target)
	if err != nil {
		d.t.skip(contain.SkipReason(err))
		return
	}
	defer sub.Close()
	now, err := sub.Stat(".")
	if err != nil || !os.SameFile(fi, now) {
		d.t.skip(reason.Vanished)
		return
	}
	st, ok := d.cfg.stat(now)
	if !ok {
		d.t.skip(reason.IOError)
		return
	}
	if st.Dev != d.dev {
		d.t.skip(reason.ForeignDevice)
		return
	}
	d.found = append(d.found, Workspace{Rel: relDir, Target: target, Dev: st.Dev, Ino: st.Ino})
}
