package collect

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hedronite/cubit/internal/scan"
	"github.com/Hedronite/cubit/internal/version"
)

var info = version.Info{Version: "v0.0.0-test", Revision: "abc123", GoVersion: "go1.test"}

var t0 = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

func mustUpdate(t *testing.T, c *Collector, d *scan.Document, at time.Time) {
	t.Helper()
	if err := c.Update(d, at); err != nil {
		t.Fatal(err)
	}
}

func exampleDoc(t *testing.T) *scan.Document {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "scan.json"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := scan.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	for i := range d.Roots {
		r := &d.Roots[i]
		if r.Error != "" {
			r.Errors = []string{r.Error}
		}
	}
	return d
}

type sample struct {
	name   string
	labels map[string]string
	value  string
}

var sampleRe = regexp.MustCompile(`^([a-z_]+)(?:\{(.*)\})? (\S+)$`)
var labelRe = regexp.MustCompile(`([a-z_]+)="((?:[^"\\]|\\.)*)"`)

func parse(t *testing.T, text string) (samples []sample, types map[string]string) {
	t.Helper()
	types = map[string]string{}
	helped := map[string]bool{}
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "# HELP "):
			helped[strings.Fields(l)[2]] = true
		case strings.HasPrefix(l, "# TYPE "):
			f := strings.Fields(l)
			types[f[2]] = f[3]
		default:
			m := sampleRe.FindStringSubmatch(l)
			if m == nil {
				t.Fatalf("not a sample line: %q", l)
			}
			s := sample{name: m[1], labels: map[string]string{}, value: m[3]}
			for _, lm := range labelRe.FindAllStringSubmatch(m[2], -1) {
				s.labels[lm[1]] = lm[2]
			}
			samples = append(samples, s)
			if _, ok := types[m[1]]; !ok || !helped[m[1]] {
				t.Fatalf("%s has no HELP and TYPE before its first sample", m[1])
			}
		}
	}
	return samples, types
}

func find(ss []sample, name string, kv ...string) []sample {
	var out []sample
next:
	for _, s := range ss {
		if s.name != name {
			continue
		}
		for i := 0; i+1 < len(kv); i += 2 {
			if s.labels[kv[i]] != kv[i+1] {
				continue next
			}
		}
		out = append(out, s)
	}
	return out
}

func one(t *testing.T, ss []sample, name string, kv ...string) string {
	t.Helper()
	got := find(ss, name, kv...)
	if len(got) != 1 {
		t.Fatalf("%s%v: %d samples, want 1", name, kv, len(got))
	}
	return got[0].value
}

// docMetrics reads the table in docs/metrics.md: name -> (type, label names).
func docMetrics(t *testing.T) map[string]struct {
	typ    string
	labels []string
} {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "metrics.md"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]struct {
		typ    string
		labels []string
	}{}
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(l, "| `cubit_") {
			continue
		}
		cells := strings.Split(l, "|")
		name := strings.Trim(strings.TrimSpace(cells[1]), "`")
		var labels []string
		for _, x := range regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(cells[3], -1) {
			labels = append(labels, x[1])
		}
		sort.Strings(labels)
		out[name] = struct {
			typ    string
			labels []string
		}{strings.TrimSpace(cells[2]), labels}
	}
	if len(out) != 15 {
		t.Fatalf("parsed %d documented metrics, want 15", len(out))
	}
	return out
}

// The renderer emits exactly the documented metrics, with the documented type
// and label names, for a document that covers every mode.
func TestEmitsExactlyTheDocumentedMetrics(t *testing.T) {
	doc := exampleDoc(t)
	c := New(info, []string{"example-cargo-home", "example-projects", "example-shared-target"})
	mustUpdate(t, c, doc, t0)
	samples, types := parse(t, string(c.Metrics()))
	docs := docMetrics(t)

	seen := map[string]bool{}
	for _, s := range samples {
		seen[s.name] = true
		d, ok := docs[s.name]
		if !ok {
			t.Errorf("emitted %s, which docs/metrics.md does not define", s.name)
			continue
		}
		if types[s.name] != d.typ {
			t.Errorf("%s: type %s, documented %s", s.name, types[s.name], d.typ)
		}
		var got []string
		for k := range s.labels {
			got = append(got, k)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(d.labels, ",") {
			t.Errorf("%s: labels %v, documented %v", s.name, got, d.labels)
		}
	}
	for name := range docs {
		if !seen[name] {
			t.Errorf("documented %s is never emitted for a document covering every mode", name)
		}
	}
}

func TestSnapshotBeforeTheFirstScan(t *testing.T) {
	c := New(info, []string{"a", "b"})
	ss, _ := parse(t, string(c.Metrics()))
	for _, s := range ss {
		switch s.name {
		case "cubit_build_info", "cubit_scans_total", "cubit_scan_errors_total":
		default:
			t.Errorf("%s emitted before any scan finished", s.name)
		}
	}
	if got := find(ss, "cubit_scan_errors_total"); len(got) != 10 {
		t.Errorf("errors_total: %d series, want 5 reasons x 2 roots", len(got))
	}
	for _, s := range find(ss, "cubit_scans_total") {
		if s.value != "0" {
			t.Errorf("%+v", s)
		}
	}
	if one(t, ss, "cubit_build_info", "version", "v0.0.0-test") != "1" {
		t.Error("build info is always 1")
	}
}

func root(alias, mode string, ok, partial bool, bytesN int64, ws ...scan.Workspace) scan.Root {
	r := scan.Root{Alias: alias, Mode: mode, Success: ok, Partial: partial, Bytes: bytesN, DurationSeconds: 1.5,
		Workspaces: ws, WorkspacesFound: len(ws), Caches: []scan.Cache{}, Skipped: []scan.Skip{}}
	if partial {
		r.Errors = []string{"deadline_exceeded"}
		r.Error = "deadline_exceeded"
	}
	if !ok {
		r.Errors = []string{"root_missing"}
		r.Error = "root_missing"
	}
	return r
}

func ws(name string, b int64) scan.Workspace {
	return scan.Workspace{Name: name, Bytes: b, Profiles: &scan.Profiles{Debug: b}}
}

func doc(roots ...scan.Root) *scan.Document { return &scan.Document{Roots: roots} }

// The lifecycle: a root that fails first has no size series; its first success
// brings them; a later failure keeps them; a partial scan replaces them with the
// lower bound; a vanished workspace takes its series with it.
func TestLastGoodAndFirstSuccessRules(t *testing.T) {
	c := New(info, []string{"p"})

	mustUpdate(t, c, doc(root("p", "workspace-root", false, false, 0)), t0)
	ss, _ := parse(t, string(c.Metrics()))
	if len(find(ss, "cubit_root_bytes")) != 0 || len(find(ss, "cubit_workspace_target_bytes")) != 0 || len(find(ss, "cubit_scan_last_success_timestamp_seconds")) != 0 {
		t.Error("a never-measured root must publish no size series and no success time")
	}
	if one(t, ss, "cubit_scan_success", "root", "p") != "0" || one(t, ss, "cubit_scan_partial", "root", "p") != "0" {
		t.Error("failed: success 0, partial 0")
	}
	if one(t, ss, "cubit_scan_errors_total", "root", "p", "reason", "root_missing") != "1" {
		t.Error("error counter")
	}

	t1 := t0.Add(time.Minute)
	mustUpdate(t, c, doc(root("p", "workspace-root", true, false, 300, ws("a", 100), ws("b", 200))), t1)
	ss, _ = parse(t, string(c.Metrics()))
	if one(t, ss, "cubit_root_bytes", "root", "p") != "300" || one(t, ss, "cubit_workspace_target_bytes", "workspace", "b") != "200" {
		t.Error("first success publishes")
	}
	if one(t, ss, "cubit_scan_last_success_timestamp_seconds", "root", "p") != ftoa(float64(t1.UnixMilli())/1000) {
		t.Error("success time")
	}
	if len(find(ss, "cubit_workspace_profile_bytes", "workspace", "a")) != 5 {
		t.Error("five profiles per workspace")
	}

	mustUpdate(t, c, doc(root("p", "workspace-root", false, false, 0)), t1.Add(time.Minute))
	ss, _ = parse(t, string(c.Metrics()))
	if one(t, ss, "cubit_root_bytes", "root", "p") != "300" || one(t, ss, "cubit_workspace_target_bytes", "workspace", "a") != "100" {
		t.Error("a failed scan keeps the last good values")
	}
	if one(t, ss, "cubit_scan_success", "root", "p") != "0" || one(t, ss, "cubit_scan_partial", "root", "p") != "0" {
		t.Error("failed: success 0, partial 0 even with last good values held")
	}
	if one(t, ss, "cubit_scan_last_success_timestamp_seconds", "root", "p") != ftoa(float64(t1.UnixMilli())/1000) {
		t.Error("the success time stays at the last success")
	}

	mustUpdate(t, c, doc(root("p", "workspace-root", true, true, 120, ws("a", 120))), t1.Add(2*time.Minute))
	ss, _ = parse(t, string(c.Metrics()))
	if one(t, ss, "cubit_root_bytes", "root", "p") != "120" || one(t, ss, "cubit_scan_partial", "root", "p") != "1" || one(t, ss, "cubit_scan_success", "root", "p") != "1" {
		t.Error("a partial scan publishes its lower bound; it never falls back to last good")
	}
	if len(find(ss, "cubit_workspace_target_bytes", "workspace", "b")) != 0 {
		t.Error("a workspace that is gone takes its series with it")
	}
	if one(t, ss, "cubit_scan_errors_total", "root", "p", "reason", "deadline_exceeded") != "1" || one(t, ss, "cubit_scan_errors_total", "root", "p", "reason", "root_missing") != "2" {
		t.Error("counters accumulate across scans")
	}
	for _, r := range []string{"ok", "partial", "failed"} {
		want := map[string]string{"ok": "1", "partial": "1", "failed": "2"}[r]
		if got := one(t, ss, "cubit_scans_total", "result", r); got != want {
			t.Errorf("scans_total{%s} = %s, want %s", r, got, want)
		}
	}
}

func TestSkippedEntriesShowOnlyNonZeroReasonsFromTheSameScanAsTheSizes(t *testing.T) {
	c := New(info, []string{"p"})
	r := root("p", "workspace-root", true, false, 10, ws("a", 10))
	r.Skipped = []scan.Skip{{Reason: "refused", Count: 2}, {Reason: "vanished", Count: 1}}
	mustUpdate(t, c, doc(r), t0)
	ss, _ := parse(t, string(c.Metrics()))
	if len(find(ss, "cubit_skipped_entries")) != 2 || one(t, ss, "cubit_skipped_entries", "reason", "refused") != "2" {
		t.Errorf("%v", find(ss, "cubit_skipped_entries"))
	}
	// A failed scan holds the last good skips with the last good sizes.
	mustUpdate(t, c, doc(root("p", "workspace-root", false, false, 0)), t0.Add(time.Minute))
	ss, _ = parse(t, string(c.Metrics()))
	if len(find(ss, "cubit_skipped_entries")) != 2 {
		t.Error("failed scan: skips stay with the last good values")
	}
	// A later good scan with none clears them.
	mustUpdate(t, c, doc(root("p", "workspace-root", true, false, 10, ws("a", 10))), t0.Add(2*time.Minute))
	ss, _ = parse(t, string(c.Metrics()))
	if len(find(ss, "cubit_skipped_entries")) != 0 {
		t.Error("a scan with no skips publishes no skip series")
	}
}

func TestCacheAndModeSpecificFamilies(t *testing.T) {
	c := New(info, []string{"ch", "t"})
	ch := root("ch", "cargo-home", true, false, 9)
	ch.Caches = []scan.Cache{{Kind: "git-db", Bytes: 4}, {Kind: "cargo-bin", Bytes: 5}}
	tg := root("t", "target-dir", true, false, 7, ws("shared", 7))
	mustUpdate(t, c, doc(ch, tg), t0)
	ss, _ := parse(t, string(c.Metrics()))
	if one(t, ss, "cubit_cache_bytes", "root", "ch", "kind", "git-db") != "4" {
		t.Error("cache bytes")
	}
	if len(find(ss, "cubit_workspaces", "root", "ch")) != 0 || len(find(ss, "cubit_workspace_target_bytes", "root", "ch")) != 0 {
		t.Error("cache modes emit no workspace series")
	}
	if len(find(ss, "cubit_cache_bytes", "root", "t")) != 0 || one(t, ss, "cubit_workspaces", "root", "t") != "1" {
		t.Error("target-dir emits one workspace and no cache series")
	}
}

func TestProfilesOffEmitsNoProfileSeries(t *testing.T) {
	c := New(info, []string{"p"})
	w := scan.Workspace{Name: "a", Bytes: 5}
	r := root("p", "workspace-root", true, false, 5, w)
	mustUpdate(t, c, doc(r), t0)
	ss, _ := parse(t, string(c.Metrics()))
	if len(find(ss, "cubit_workspace_profile_bytes")) != 0 || one(t, ss, "cubit_workspace_target_bytes", "workspace", "a") != "5" {
		t.Error("profiles: false")
	}
}

func TestRenderingIsDeterministicAndSorted(t *testing.T) {
	d := exampleDoc(t)
	a := New(info, []string{"example-projects", "example-cargo-home", "example-shared-target"})
	mustUpdate(t, a, d, t0)
	b := New(info, []string{"example-shared-target", "example-cargo-home", "example-projects"})
	mustUpdate(t, b, d, t0)
	if !bytes.Equal(a.Metrics(), b.Metrics()) {
		t.Error("registration order changed the output")
	}
	ss, _ := parse(t, string(a.Metrics()))
	var roots []string
	for _, s := range find(ss, "cubit_root_bytes") {
		roots = append(roots, s.labels["root"])
	}
	if !sort.StringsAreSorted(roots) {
		t.Errorf("roots not sorted: %v", roots)
	}
}

func TestLabelValuesAreEscaped(t *testing.T) {
	if got := escape("a\"b\\c\nd"); got != `a\"b\\c\nd` {
		t.Errorf("%q", got)
	}
	if escape("plain") != "plain" {
		t.Error("plain values pass through")
	}
}

// A scrape in the middle of an update sees either the old or the new snapshot
// whole, never a mixture. Run under -race.
func TestSnapshotsAreAtomicUnderRace(t *testing.T) {
	c := New(info, []string{"p"})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				m := string(c.Metrics())
				// Within one snapshot, root_bytes and the workspace total agree.
				ss, _ := parse(t, m)
				rb := find(ss, "cubit_root_bytes")
				wb := find(ss, "cubit_workspace_target_bytes")
				if len(rb) == 1 && len(wb) == 1 && rb[0].value != wb[0].value {
					t.Errorf("torn snapshot: root %s workspace %s", rb[0].value, wb[0].value)
					return
				}
			}
		}()
	}
	for i := int64(1); i <= 200; i++ {
		mustUpdate(t, c, doc(root("p", "workspace-root", true, false, i*10, ws("a", i*10))), t0)
	}
	close(stop)
	wg.Wait()
}

// promtool is the reference linter; the CI rules job runs it over a live
// /metrics too. Here it runs when it is installed.
func TestPromtoolCheckMetrics(t *testing.T) {
	pt, err := exec.LookPath("promtool")
	if err != nil {
		t.Skip("promtool is not installed")
	}
	c := New(info, []string{"example-cargo-home", "example-projects", "example-shared-target"})
	mustUpdate(t, c, exampleDoc(t), t0)
	cmd := exec.Command(pt, "check", "metrics")
	cmd.Stdin = bytes.NewReader(c.Metrics())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("promtool check metrics: %v\n%s", err, out)
	}
}

func TestADuplicateSampleKeepsThePreviousSnapshotWhole(t *testing.T) {
	c := New(info, []string{"p"})
	mustUpdate(t, c, doc(root("p", "workspace-root", true, false, 10, ws("a", 10))), t0)
	before := string(c.Metrics())
	bad := root("p", "workspace-root", true, false, 99, ws("dup", 1), ws("dup", 2)) // a hand-fed document
	if err := c.Update(doc(bad), t0.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "duplicate sample") {
		t.Fatalf("err = %v", err)
	}
	if string(c.Metrics()) != before {
		t.Error("the snapshot changed")
	}
	// Nothing was half applied: the counters did not move either.
	mustUpdate(t, c, doc(root("p", "workspace-root", true, false, 11, ws("a", 11))), t0.Add(2*time.Minute))
	ss, _ := parse(t, string(c.Metrics()))
	if one(t, ss, "cubit_scans_total", "result", "ok") != "2" {
		t.Error("the rejected scan was counted")
	}
}

// A scan counts once, however many roots, and a root counts one error per
// reason however many subtrees caused it.
func TestCountersCountOnce(t *testing.T) {
	c := New(info, []string{"a", "b"})
	ra := root("a", "workspace-root", true, true, 1, ws("x", 1))
	ra.Errors = []string{"deadline_exceeded", "subtree_unreadable"}
	ra.Skipped = []scan.Skip{{Reason: "permission_denied", Count: 3}}
	mustUpdate(t, c, doc(ra, root("b", "workspace-root", true, false, 1, ws("y", 1))), t0)
	ss, _ := parse(t, string(c.Metrics()))
	total := 0
	for _, s := range find(ss, "cubit_scans_total") {
		if s.value == "1" {
			total++
		}
	}
	if total != 1 {
		t.Errorf("one scan is one count in one result: %v", find(ss, "cubit_scans_total"))
	}
	if one(t, ss, "cubit_scan_errors_total", "root", "a", "reason", "subtree_unreadable") != "1" || one(t, ss, "cubit_scan_errors_total", "root", "b", "reason", "subtree_unreadable") != "0" {
		t.Error("errors_total")
	}
}

// The duration of a failed scan is that attempt's wall time, not the last good one's.
func TestDurationOfAFailedScanIsTheAttempt(t *testing.T) {
	c := New(info, []string{"p"})
	good := root("p", "workspace-root", true, false, 1, ws("a", 1))
	good.DurationSeconds = 2
	mustUpdate(t, c, doc(good), t0)
	bad := root("p", "workspace-root", false, false, 0)
	bad.DurationSeconds = 41.5
	mustUpdate(t, c, doc(bad), t0.Add(time.Minute))
	ss, _ := parse(t, string(c.Metrics()))
	if one(t, ss, "cubit_scan_duration_seconds", "root", "p") != "41.5" {
		t.Error("the failed attempt's wall time")
	}
}

func TestHelpTextIsEscaped(t *testing.T) {
	if got := escapeHelp("a\\b\nc"); got != `a\\b\nc` {
		t.Errorf("%q", got)
	}
}
