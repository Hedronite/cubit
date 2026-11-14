package walk

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"

	"github.com/Hedronite/cubit/internal/contain"
	"github.com/Hedronite/cubit/internal/reason"
)

// Stat is what the walk needs from a file's metadata. Blocks are 512-byte
// units, as st_blocks reports on linux and darwin alike.
type Stat struct {
	Blocks, Dev, Ino, Nlink uint64
}

// StatFunc reads a Stat from a FileInfo. ok is false when the FileInfo is not
// from the OS.
type StatFunc func(fs.FileInfo) (Stat, bool)

// Config is what every walk of one root shares.
type Config struct {
	Refuse     *contain.Refuser
	Stat       StatFunc // StatOf when nil
	Budget     *Budget
	DepthGuard int // directories deeper than this are skipped as depth_exceeded
	DedupeMax  int // hardlink set bound per unit
}

// Tally is the result of walking one unit.
type Tally struct {
	Bytes    int64
	Profiles [NumProfiles]int64
	Skips    map[string]int
}

func (t *Tally) skip(reason string) {
	if t.Skips == nil {
		t.Skips = map[string]int{}
	}
	t.Skips[reason]++
}

type inode struct{ dev, ino uint64 }

type walker struct {
	ctx   context.Context
	cfg   Config
	dev   uint64
	comps []string // absolute components of the directory being read
	t     *Tally
	seen  map[inode]struct{}
}

func (c Config) stat(fi fs.FileInfo) (Stat, bool) {
	if c.Stat != nil {
		return c.Stat(fi)
	}
	return StatOf(fi)
}

// Measure walks the whole tree under root and returns its allocated bytes, the
// bytes split by profile (every byte lands in exactly one), and skip counts.
// rootComps are the absolute components of root, for the refuse check. The
// root directory's own inode is counted. An error means the root itself could
// not be read (classify it with contain.SkipReason) or ctx was canceled; skips
// below the root never fail the walk.
func (c Config) Measure(ctx context.Context, root *os.Root, rootComps []string) (Tally, error) {
	var t Tally
	fi, err := root.Stat(".")
	if err != nil {
		return t, err
	}
	st, ok := c.stat(fi)
	if !ok {
		return t, errors.New("no stat data from this filesystem")
	}
	w := &walker{
		ctx:   ctx,
		cfg:   c,
		dev:   st.Dev,
		comps: append(make([]string, 0, len(rootComps)+16), rootComps...),
		t:     &t,
		seen:  map[inode]struct{}{},
	}
	err = w.tree(root, st, 0, Other, true)
	return t, err
}

func (w *walker) add(st Stat, p Profile) {
	n := int64(st.Blocks) * 512 //nolint:gosec // blocks come from stat, far below 2^53
	w.t.Bytes += n
	w.t.Profiles[p] += n
}

// listing reads the names in dir in sorted order. It counts every entry
// against the budget as it is read, so a huge directory stops at the cap.
func (w *walker) listing(dir *os.Root) ([]string, error) {
	f, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var names []string
	for {
		chunk, err := f.Readdirnames(readChunk)
		if len(chunk) > 0 {
			names = append(names, chunk...)
			if !w.cfg.Budget.Entries(len(chunk)) {
				return nil, nil
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			sort.Strings(names)
			return names, err
		}
	}
	sort.Strings(names)
	return names, nil
}

// tree reads dir, adds its own inode once the listing worked, and recurses. top
// marks the unit's root, whose listing failure is the caller's to report.
func (w *walker) tree(dir *os.Root, own Stat, depth int, prof Profile, top bool) error {
	names, err := w.listing(dir)
	if err != nil && len(names) == 0 {
		if top {
			return err
		}
		w.t.skip(contain.SkipReason(err))
		return nil
	}
	if err != nil {
		w.t.skip(contain.SkipReason(err))
	}
	if !w.cfg.Budget.Alive() {
		return nil
	}
	w.add(own, prof)
	for i, name := range names {
		if i&255 == 255 {
			if err := w.ctx.Err(); err != nil {
				return err
			}
			if !w.cfg.Budget.Alive() {
				return nil
			}
		}
		if err := w.entry(dir, name, depth, prof); err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) entry(dir *os.Root, name string, depth int, prof Profile) error {
	w.comps = append(w.comps, name)
	defer func() { w.comps = w.comps[:len(w.comps)-1] }()
	if w.cfg.Refuse.Refused(w.comps) {
		w.t.skip(reason.Refused)
		return nil
	}
	fi, err := dir.Lstat(name)
	if err != nil {
		w.t.skip(contain.SkipReason(err))
		return nil
	}
	st, ok := w.cfg.stat(fi)
	if !ok {
		w.t.skip(reason.IOError)
		return nil
	}
	p := childProfile(prof, depth, name)
	if fi.IsDir() {
		if st.Dev != w.dev {
			w.t.skip(reason.ForeignDevice)
			return nil
		}
		if w.cfg.DepthGuard > 0 && depth+1 > w.cfg.DepthGuard {
			w.t.skip(reason.DepthExceeded)
			return nil
		}
		child, err := contain.ChildWith(dir, name, fi)
		if err != nil {
			w.t.skip(contain.SkipReason(err))
			return nil
		}
		defer child.Close()
		return w.tree(child, st, depth+1, p, false)
	}
	if st.Nlink > 1 {
		k := inode{st.Dev, st.Ino}
		if _, dup := w.seen[k]; dup {
			return nil
		}
		if len(w.seen) < w.cfg.DedupeMax {
			w.seen[k] = struct{}{}
		} else {
			w.t.skip(reason.DedupeOverflow)
		}
	}
	w.add(st, p)
	return nil
}
