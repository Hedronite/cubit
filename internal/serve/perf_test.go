package serve

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// scrapeBudget is the most a /metrics request may take on the generated tree
// (docs/performance.md). A scrape is served from the last finished scan's bytes,
// so this is a ceiling that leaves room for a slow CI runner, not a tight goal.
const scrapeBudget = 50 * time.Millisecond

// Budget: a scrape is served from the snapshot and never walks. The tree is
// 10,000 files in 1,000 directories; after the first scan it is deleted, and
// every scrape still answers the same bytes, inside the budget.
func TestAScrapeIsServedFromTheSnapshotInsideTheBudget(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for w := 0; w < 10; w++ {
		ws := filepath.Join(d, fmt.Sprintf("ws%02d", w))
		if err := os.MkdirAll(ws, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, "Cargo.toml"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		for dir := 0; dir < 100; dir++ {
			p := filepath.Join(ws, "target", "debug", fmt.Sprintf("d%03d", dir))
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			for f := 0; f < 10; f++ {
				if err := os.WriteFile(filepath.Join(p, fmt.Sprintf("f%d", f)), []byte{1}, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	r := start(t, Options{Spec: spec(t, wsRoot("p", d)), Wait: func(time.Duration) time.Duration { return time.Hour }}, slog.LevelError)
	doc := r.waitScan(t)
	if got := doc.Roots[0]; !got.Success || got.Partial || got.WorkspacesFound != 10 {
		t.Fatalf("the budget tree must scan complete: %+v", got)
	}
	_, first := r.get(t, "GET", "/metrics")

	// Nothing to walk any more: a scrape that walked would show it.
	if err := os.RemoveAll(d); err != nil {
		t.Fatal(err)
	}
	var times []time.Duration
	for i := 0; i < 200; i++ {
		start := time.Now()
		_, body := r.get(t, "GET", "/metrics")
		times = append(times, time.Since(start))
		if body != first {
			t.Fatalf("scrape %d differs from the first: the handler must serve the snapshot", i)
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	p99, worst := times[len(times)*99/100], times[len(times)-1]
	t.Logf("200 scrapes of %d bytes: median %v, p99 %v, worst %v", len(first), times[len(times)/2], p99, worst)
	if p99 > scrapeBudget {
		t.Errorf("p99 scrape %v is over the %v budget", p99, scrapeBudget)
	}
}
