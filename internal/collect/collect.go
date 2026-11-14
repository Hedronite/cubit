package collect

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hedronite/cubit/internal/config"
	"github.com/Hedronite/cubit/internal/reason"
	"github.com/Hedronite/cubit/internal/scan"
	"github.com/Hedronite/cubit/internal/version"
)

// The scan results of cubit_scans_total.
const (
	ResultOK      = "ok"
	ResultPartial = "partial"
	ResultFailed  = "failed"
)

var results = []string{ResultOK, ResultPartial, ResultFailed}

type rootState struct {
	good        *scan.Root // the last scan with success true; nil before the first
	lastSuccess time.Time
	seen        bool // a scan of this root has finished
	duration    float64
	success     bool
	partial     bool
	errs        map[string]uint64
}

// Collector holds what a scrape can see. It is safe for one writer (the scan
// loop) and any number of readers.
type Collector struct {
	info version.Info

	mu    sync.Mutex
	roots map[string]*rootState
	scans map[string]uint64
	snap  atomic.Pointer[[]byte]
}

// New makes a Collector for the given root aliases, so the counters exist at 0
// from the first scrape. The snapshot is valid at once: build info and the
// zero counters.
func New(info version.Info, aliases []string) *Collector {
	c := &Collector{info: info, roots: map[string]*rootState{}, scans: map[string]uint64{}}
	for _, a := range aliases {
		c.state(a)
	}
	if err := c.publish(); err != nil {
		panic("collect: the empty snapshot does not render: " + err.Error())
	}
	return c
}

func (c *Collector) state(alias string) *rootState {
	st := c.roots[alias]
	if st == nil {
		st = &rootState{errs: map[string]uint64{}}
		for _, r := range reason.Error {
			st.errs[r] = 0
		}
		c.roots[alias] = st
	}
	return st
}

// Update folds one finished scan in and publishes a new snapshot.
//
//   - A root with success true replaces its last good values, partial or not: a
//     partial scan publishes the lower bound it measured and never falls back.
//   - A root that failed keeps its last good values; only its health changes.
//   - Every error reason that applied increments its counter.
//
// If the result cannot be rendered (a duplicate label set, which Prometheus
// would reject whole), Update returns the error and the previous snapshot
// stays: one bad document never costs the scrape every series.
func (c *Collector) Update(doc *scan.Document, at time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.snapshotState()
	for i := range doc.Roots {
		r := doc.Roots[i]
		st := c.state(r.Alias)
		st.seen = true
		st.duration = r.DurationSeconds
		st.success = r.Success
		st.partial = r.Success && r.Partial
		for _, e := range r.Errors {
			st.errs[e]++
		}
		if r.Success {
			cp := r
			st.good = &cp
			st.lastSuccess = at
		}
	}
	switch doc.ExitCode() {
	case 0:
		c.scans[ResultOK]++
	case 1:
		c.scans[ResultPartial]++
	default:
		c.scans[ResultFailed]++
	}
	if err := c.publish(); err != nil {
		c.restore(prev)
		return err
	}
	return nil
}

// state copy/restore, so a render failure leaves nothing half applied.
type saved struct {
	roots map[string]rootState
	scans map[string]uint64
}

func (c *Collector) snapshotState() saved {
	sv := saved{roots: map[string]rootState{}, scans: map[string]uint64{}}
	for k, v := range c.roots {
		cp := *v
		cp.errs = map[string]uint64{}
		for a, b := range v.errs {
			cp.errs[a] = b
		}
		sv.roots[k] = cp
	}
	for k, v := range c.scans {
		sv.scans[k] = v
	}
	return sv
}

func (c *Collector) restore(sv saved) {
	c.roots = map[string]*rootState{}
	for k, v := range sv.roots {
		cp := v
		c.roots[k] = &cp
	}
	c.scans = sv.scans
}

// Gauges renders the current state without the two counter families. Counters
// are process-scoped: a one-shot `scan` writes a file from a process that
// lives for one scan, so its counters would restart at 0 or 1 every run and
// read as a reset on every scrape. A textfile from a one-shot scan carries
// gauges only.
func (c *Collector) Gauges() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	text, err := c.render(false)
	if err != nil {
		return nil
	}
	return []byte(text)
}

// Metrics returns the current snapshot. The slice is never modified after it is
// published, so a caller may hold it, but must not change it.
func (c *Collector) Metrics() []byte { return *c.snap.Load() }

func (c *Collector) publish() error {
	text, err := c.render(true)
	if err != nil {
		return err
	}
	b := []byte(text)
	c.snap.Store(&b)
	return nil
}

func (c *Collector) aliases() []string {
	out := make([]string, 0, len(c.roots))
	for a := range c.roots {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

type family struct {
	name, help, typ string
	lines           []string
}

func (f *family) add(labels, value string) {
	if labels != "" {
		labels = "{" + labels + "}"
	}
	f.lines = append(f.lines, f.name+labels+" "+value)
}

func label(k, v string) string { return k + `="` + escape(v) + `"` }

func escape(s string) string {
	if !strings.ContainsAny(s, "\\\"\n") {
		return s
	}
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func join(ls ...string) string { return strings.Join(ls, ",") }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func ftoa(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func b01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// render writes every family in the order docs/metrics.md lists them. Samples
// inside a family are sorted by root, then by their other labels.
func (c *Collector) render(withCounters bool) (string, error) {
	fam := []*family{
		{name: "cubit_workspace_target_bytes", typ: "gauge", help: "Allocated bytes under one workspace's target/."},
		{name: "cubit_workspace_profile_bytes", typ: "gauge", help: "Workspace bytes split by build profile."},
		{name: "cubit_root_bytes", typ: "gauge", help: "All measured bytes in the root."},
		{name: "cubit_cache_bytes", typ: "gauge", help: "Allocated bytes of one fixed cache area."},
		{name: "cubit_workspaces", typ: "gauge", help: "Workspaces found, labelled or not."},
		{name: "cubit_workspaces_unlabeled", typ: "gauge", help: "Workspaces beyond max_workspaces that got no label."},
		{name: "cubit_workspaces_unlabeled_bytes", typ: "gauge", help: "Bytes of the workspaces that got no label."},
		{name: "cubit_skipped_entries", typ: "gauge", help: "Entries or subtrees skipped in the last scan, by reason."},
		{name: "cubit_scan_duration_seconds", typ: "gauge", help: "Wall time of the last scan of this root."},
		{name: "cubit_scan_success", typ: "gauge", help: "1 when the last scan gave a usable result, complete or partial."},
		{name: "cubit_scan_partial", typ: "gauge", help: "1 when some bytes were not measured; read every size as a lower bound."},
		{name: "cubit_scan_last_success_timestamp_seconds", typ: "gauge", help: "Unix time of the last scan with cubit_scan_success 1."},
		{name: "cubit_scan_errors_total", typ: "counter", help: "Scans of this root that failed or were partial, by reason."},
		{name: "cubit_scans_total", typ: "counter", help: "Scan rounds finished, by result."},
		{name: "cubit_build_info", typ: "gauge", help: "Build identity; always 1."},
	}
	by := map[string]*family{}
	for _, f := range fam {
		by[f.name] = f
	}

	for _, alias := range c.aliases() {
		st := c.roots[alias]
		root := label("root", alias)
		if g := st.good; g != nil {
			by["cubit_root_bytes"].add(root, itoa(g.Bytes))
			switch g.Mode {
			case config.WorkspaceRoot, config.TargetDir:
				for _, w := range g.Workspaces {
					ws := join(root, label("workspace", w.Name))
					by["cubit_workspace_target_bytes"].add(ws, itoa(w.Bytes))
					if w.Profiles != nil {
						p := w.Profiles
						for _, kv := range []struct {
							n string
							v int64
						}{{"debug", p.Debug}, {"release", p.Release}, {"doc", p.Doc}, {"incremental", p.Incremental}, {"other", p.Other}} {
							by["cubit_workspace_profile_bytes"].add(join(ws, label("profile", kv.n)), itoa(kv.v))
						}
					}
				}
				by["cubit_workspaces"].add(root, itoa(int64(g.WorkspacesFound)))
				by["cubit_workspaces_unlabeled"].add(root, itoa(int64(g.WorkspacesUnlabeled)))
				by["cubit_workspaces_unlabeled_bytes"].add(root, itoa(g.WorkspacesUnlabeledBytes))
			case config.CargoHome, config.Sccache:
				for _, k := range g.Caches {
					by["cubit_cache_bytes"].add(join(root, label("kind", k.Kind)), itoa(k.Bytes))
				}
			}
			for _, s := range g.Skipped {
				if s.Count > 0 {
					by["cubit_skipped_entries"].add(join(root, label("reason", s.Reason)), itoa(int64(s.Count)))
				}
			}
		}
		if st.seen {
			by["cubit_scan_duration_seconds"].add(root, ftoa(st.duration))
			by["cubit_scan_success"].add(root, b01(st.success))
			by["cubit_scan_partial"].add(root, b01(st.partial))
		}
		if st.good != nil {
			by["cubit_scan_last_success_timestamp_seconds"].add(root, ftoa(float64(st.lastSuccess.UnixMilli())/1000))
		}
		if withCounters {
			for _, r := range reason.Error {
				by["cubit_scan_errors_total"].add(join(root, label("reason", r)), strconv.FormatUint(st.errs[r], 10))
			}
		}
	}
	if withCounters {
		for _, r := range results {
			by["cubit_scans_total"].add(label("result", r), strconv.FormatUint(c.scans[r], 10))
		}
	}
	by["cubit_build_info"].add(join(label("version", c.info.Version), label("revision", c.info.Revision), label("go_version", c.info.GoVersion)), "1")

	var b strings.Builder
	for _, f := range fam {
		if len(f.lines) == 0 {
			continue
		}
		seen := make(map[string]bool, len(f.lines))
		for _, l := range f.lines {
			series := l[:strings.LastIndexByte(l, ' ')]
			if seen[series] {
				return "", fmt.Errorf("duplicate sample %s", series)
			}
			seen[series] = true
		}
		b.WriteString("# HELP " + f.name + " " + escapeHelp(f.help) + "\n# TYPE " + f.name + " " + f.typ + "\n")
		for _, l := range f.lines {
			b.WriteString(l + "\n")
		}
	}
	return b.String(), nil
}

// escapeHelp escapes a HELP text: backslash and newline.
func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}
