package collect

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file pins docs/vigil.md. The mapping table there says which metric fills
// each field of the cubit{} block Vigil shows; the test renders a scan document
// through the collector, reads the exposition the way that table says, and
// compares the result with testdata/vigil/cubit-block.json.

// fieldMetric is the metric each block field is read from. The test uses it to
// build the block, and compares it with the Metric column of docs/vigil.md, so
// the two cannot drift.
var fieldMetric = map[string]string{
	"hosts[].host":                   "cubit_build_info",
	"hosts[].generated_at":           "cubit_scan_last_success_timestamp_seconds",
	"hosts[].roots[].alias":          "cubit_root_info",
	"hosts[].roots[].mode":           "cubit_root_info",
	"hosts[].roots[].bytes":          "cubit_root_bytes",
	"hosts[].roots[].success":        "cubit_scan_success",
	"hosts[].roots[].partial":        "cubit_scan_partial",
	"hosts[].roots[].top[].name":     "cubit_workspace_target_bytes",
	"hosts[].roots[].top[].bytes":    "cubit_workspace_target_bytes",
	"hosts[].roots[].caches[].kind":  "cubit_cache_bytes",
	"hosts[].roots[].caches[].bytes": "cubit_cache_bytes",
}

func metricFor(field string) string { return fieldMetric[field] }

const topN = 5

type block struct {
	Hosts []hostBlock `json:"hosts"`
}

type hostBlock struct {
	Host        string      `json:"host"`
	GeneratedAt string      `json:"generated_at,omitempty"`
	Roots       []rootBlock `json:"roots"`
}

type rootBlock struct {
	Alias   string       `json:"alias"`
	Mode    string       `json:"mode"`
	Bytes   *int64       `json:"bytes,omitempty"`
	Success bool         `json:"success"`
	Partial bool         `json:"partial"`
	Top     []topBlock   `json:"top"`
	Caches  []cacheBlock `json:"caches"`
}

type topBlock struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type cacheBlock struct {
	Kind  string `json:"kind"`
	Bytes int64  `json:"bytes"`
}

// scrape is what Prometheus would hold for one host: the exposition's samples,
// each with the instance label Prometheus adds.
func scrape(t *testing.T, instance string, exposition []byte) []sample {
	t.Helper()
	ss, _ := parse(t, string(exposition))
	for i := range ss {
		ss[i].labels["instance"] = instance
	}
	return ss
}

func asInt(t *testing.T, s sample) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s.value, 10, 64)
	if err != nil {
		t.Fatalf("%s%v: %q is not a whole number of bytes", s.name, s.labels, s.value)
	}
	return n
}

// buildBlock applies the mapping table of docs/vigil.md to the samples of every
// host. It is the reference for what Vigil's named queries compute.
func buildBlock(t *testing.T, samples []sample) block {
	t.Helper()
	hosts := map[string]bool{}
	for _, s := range find(samples, metricFor("hosts[].host")) {
		hosts[s.labels["instance"]] = true
	}
	var names []string
	for h := range hosts {
		names = append(names, h)
	}
	sort.Strings(names)

	out := block{Hosts: []hostBlock{}}
	for _, host := range names {
		mine := func(name string, kv ...string) []sample {
			return find(samples, name, append([]string{"instance", host}, kv...)...)
		}
		hb := hostBlock{Host: host, Roots: []rootBlock{}}

		// generated_at: the newest last-success time across the host's roots.
		var newest float64
		for _, s := range mine(metricFor("hosts[].generated_at")) {
			f, err := strconv.ParseFloat(s.value, 64)
			if err != nil {
				t.Fatalf("%s: %v", s.name, err)
			}
			if f > newest {
				newest = f
			}
		}
		if newest > 0 {
			hb.GeneratedAt = time.Unix(int64(newest), 0).UTC().Format(time.RFC3339)
		}

		infos := mine(metricFor("hosts[].roots[].alias"))
		sort.Slice(infos, func(i, j int) bool { return infos[i].labels["root"] < infos[j].labels["root"] })
		for _, info := range infos {
			alias := info.labels["root"]
			rb := rootBlock{Alias: alias, Mode: info.labels["mode"], Top: []topBlock{}, Caches: []cacheBlock{}}
			if got := mine(metricFor("hosts[].roots[].bytes"), "root", alias); len(got) == 1 {
				n := asInt(t, got[0])
				rb.Bytes = &n
			}
			rb.Success = one(t, samples, metricFor("hosts[].roots[].success"), "instance", host, "root", alias) == "1"
			rb.Partial = one(t, samples, metricFor("hosts[].roots[].partial"), "instance", host, "root", alias) == "1"

			for _, s := range mine(metricFor("hosts[].roots[].top[].name"), "root", alias) {
				rb.Top = append(rb.Top, topBlock{Name: s.labels["workspace"], Bytes: asInt(t, s)})
			}
			sort.Slice(rb.Top, func(i, j int) bool {
				if rb.Top[i].Bytes != rb.Top[j].Bytes {
					return rb.Top[i].Bytes > rb.Top[j].Bytes
				}
				return rb.Top[i].Name < rb.Top[j].Name
			})
			if len(rb.Top) > topN {
				rb.Top = rb.Top[:topN]
			}

			for _, s := range mine(metricFor("hosts[].roots[].caches[].kind"), "root", alias) {
				rb.Caches = append(rb.Caches, cacheBlock{Kind: s.labels["kind"], Bytes: asInt(t, s)})
			}
			sort.Slice(rb.Caches, func(i, j int) bool { return rb.Caches[i].Kind < rb.Caches[j].Kind })
			hb.Roots = append(hb.Roots, rb)
		}
		out.Hosts = append(out.Hosts, hb)
	}
	return out
}

func render(t *testing.T, b block) []byte {
	t.Helper()
	j, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(j, '\n')
}

// fixtureBlock renders examples/scan.json for two hosts the way a deployment
// would: lab serves (the full exposition), edge runs one-shot scans from cron
// (gauges only), and edge's last scan is five minutes newer.
func fixtureBlock(t *testing.T) block {
	t.Helper()
	aliases := []string{"example-cargo-home", "example-projects", "example-shared-target"}

	lab := New(info, aliases)
	mustUpdate(t, lab, exampleDoc(t), t0)
	edge := New(info, aliases)
	mustUpdate(t, edge, exampleDoc(t), t0.Add(5*time.Minute))

	samples := append(scrape(t, "lab", lab.Metrics()), scrape(t, "edge", edge.Gauges())...)
	return buildBlock(t, samples)
}

const fixturePath = "../../testdata/vigil/cubit-block.json"

// The block built from examples/scan.json is the fixture. Regenerate it with
// UPDATE_VIGIL_FIXTURE=1 go test ./internal/collect -run TestVigilBlockMatchesTheFixture
// and read the diff: it is the contract Vigil codes against.
func TestVigilBlockMatchesTheFixture(t *testing.T) {
	got := render(t, fixtureBlock(t))
	if os.Getenv("UPDATE_VIGIL_FIXTURE") == "1" {
		if err := os.MkdirAll(filepath.Dir(fixturePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixturePath, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the block built from examples/scan.json differs from %s:\n--- got\n%s--- want\n%s", fixturePath, got, want)
	}
}

// fieldPaths lists every leaf field of a JSON value as a path with [] for arrays.
func fieldPaths(v any, prefix string, into map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			fieldPaths(c, p, into)
		}
	case []any:
		for _, c := range x {
			fieldPaths(c, prefix+"[]", into)
		}
	default:
		into[prefix] = true
	}
}

// vigilDoc reads the tables of docs/vigil.md: the mapping rows by field, and the
// PromQL of every row (mapping and freshness).
func vigilDoc(t *testing.T) (metric map[string]string, promql []string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "vigil.md"))
	if err != nil {
		t.Fatal(err)
	}
	cell := func(s string) string { return strings.Trim(strings.TrimSpace(s), "`") }
	metric = map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		cells := strings.Split(l, "|")
		switch {
		case strings.HasPrefix(l, "| `hosts[]"):
			if len(cells) != 5 {
				t.Fatalf("mapping row %q: want field, metric, PromQL", l)
			}
			field := cell(cells[1])
			if _, dup := metric[field]; dup {
				t.Errorf("docs/vigil.md maps %s twice", field)
			}
			metric[field] = cell(cells[2])
			promql = append(promql, cell(cells[3]))
		case strings.HasPrefix(l, "| Age of"):
			if len(cells) != 5 {
				t.Fatalf("freshness row %q: want input, PromQL, says", l)
			}
			promql = append(promql, cell(cells[2]))
		}
	}
	return metric, promql
}

// Every field of the fixture has a mapping row, every row maps a field the
// fixture shows, and the metric in the row is the one the mapping reads.
func TestEveryFixtureFieldHasAMappingRow(t *testing.T) {
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	fieldPaths(v, "", fields)
	metric, _ := vigilDoc(t)

	for f := range fields {
		if _, ok := metric[f]; !ok {
			t.Errorf("fixture field %s has no row in the docs/vigil.md mapping table", f)
		}
	}
	for f, m := range metric {
		if !fields[f] {
			t.Errorf("docs/vigil.md maps %s, which the fixture never shows", f)
		}
		if fieldMetric[f] != m {
			t.Errorf("%s: docs/vigil.md says %q, the mapping reads %q", f, m, fieldMetric[f])
		}
	}
	for f := range fieldMetric {
		if _, ok := metric[f]; !ok {
			t.Errorf("the mapping reads %s, which docs/vigil.md does not document", f)
		}
	}
	docs := docMetrics(t)
	for f, m := range metric {
		if _, ok := docs[m]; !ok {
			t.Errorf("%s: %s is not in docs/metrics.md", f, m)
		}
	}
}

// The PromQL on the page is the PromQL promtool ran against sample data.
func TestVigilQueriesAreRunByPromtoolTest(t *testing.T) {
	_, promql := vigilDoc(t)
	if len(promql) == 0 {
		t.Fatal("no PromQL found in docs/vigil.md")
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vigil", "queries.test.yml"))
	if err != nil {
		t.Fatal(err)
	}
	tested := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*- expr: (.+)$`).FindAllStringSubmatch(string(b), -1) {
		tested[strings.TrimSpace(m[1])] = true
	}
	for _, q := range promql {
		if !tested[q] {
			t.Errorf("docs/vigil.md query %q has no promql_expr_test in testdata/vigil/queries.test.yml", q)
		}
	}
}

// The fixture holds the shapes Vigil must handle: a failed root that was never
// measured, a partial root, both families of root, and no severity or path.
func TestFixtureShape(t *testing.T) {
	var b block
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Hosts) != 2 || b.Hosts[0].Host != "edge" || b.Hosts[1].Host != "lab" {
		t.Fatalf("hosts: %+v", b.Hosts)
	}
	if b.Hosts[0].GeneratedAt != "2026-01-15T12:05:00Z" || b.Hosts[1].GeneratedAt != "2026-01-15T12:00:00Z" {
		t.Errorf("generated_at: %q, %q", b.Hosts[0].GeneratedAt, b.Hosts[1].GeneratedAt)
	}
	byAlias := map[string]rootBlock{}
	for _, r := range b.Hosts[1].Roots {
		byAlias[r.Alias] = r
	}
	if r := byAlias["example-shared-target"]; r.Mode != "target-dir" || r.Bytes != nil || r.Success || r.Partial || len(r.Top) != 0 {
		t.Errorf("a failed, never-measured root has no bytes and no members: %+v", r)
	}
	if r := byAlias["example-projects"]; !r.Success || !r.Partial || r.Bytes == nil || len(r.Top) != 2 || r.Top[1].Name != "libs/core" || len(r.Caches) != 0 {
		t.Errorf("a partial workspace root: %+v", r)
	}
	if r := byAlias["example-cargo-home"]; r.Mode != "cargo-home" || len(r.Caches) != 6 || len(r.Top) != 0 {
		t.Errorf("a cargo-home root: %+v", r)
	}
	lower := strings.ToLower(string(raw))
	for _, banned := range []string{"severity", "warning", "critical", "/home", "/users", "/var"} {
		if strings.Contains(lower, banned) {
			t.Errorf("the fixture must carry no severity and no path, but contains %q", banned)
		}
	}
	// Every workspace name in the fixture fits the contract's pattern.
	name := regexp.MustCompile(`^[a-z0-9._-]+(/[a-z0-9._-]+)*$`)
	for _, h := range b.Hosts {
		for _, r := range h.Roots {
			for _, w := range r.Top {
				if !name.MatchString(w.Name) || len(w.Name) > 64 {
					t.Errorf("workspace name %q breaks the label pattern", w.Name)
				}
			}
		}
	}
}

// top[] is the five largest, biggest first, ties by name; generated_at is the
// newest success on the host even when that root has since failed.
func TestTopIsTheFiveLargestAndGeneratedAtTheNewestSuccess(t *testing.T) {
	c := New(info, []string{"p", "h"})
	mustUpdate(t, c, doc(
		root("p", "workspace-root", true, false, 0,
			ws("b", 50), ws("a", 50), ws("c", 10), ws("libs/d", 90), ws("e", 20), ws("f", 70), ws("g", 5)),
		root("h", "cargo-home", true, false, 9),
	), t0)
	mustUpdate(t, c, doc(
		root("p", "workspace-root", true, false, 0,
			ws("b", 50), ws("a", 50), ws("c", 10), ws("libs/d", 90), ws("e", 20), ws("f", 70), ws("g", 5)),
		root("h", "cargo-home", false, false, 0),
	), t0.Add(10*time.Minute))

	got := buildBlock(t, scrape(t, "lab", c.Metrics()))
	if len(got.Hosts) != 1 {
		t.Fatalf("%+v", got)
	}
	h := got.Hosts[0]
	if h.GeneratedAt != "2026-01-15T12:10:00Z" {
		t.Errorf("generated_at %q, want the newest success on the host", h.GeneratedAt)
	}
	var p rootBlock
	var hm rootBlock
	for _, r := range h.Roots {
		if r.Alias == "p" {
			p = r
		} else {
			hm = r
		}
	}
	want := []topBlock{{"libs/d", 90}, {"f", 70}, {"a", 50}, {"b", 50}, {"e", 20}}
	if !reflect.DeepEqual(p.Top, want) {
		t.Errorf("top = %+v, want %+v", p.Top, want)
	}
	if hm.Success || hm.Bytes == nil {
		t.Errorf("a root that failed after a success keeps its bytes and reads success false: %+v", hm)
	}
}

// Before any scan has finished a host is listed with no roots and no time.
func TestAHostBeforeItsFirstScanHasNoRootsAndNoTime(t *testing.T) {
	c := New(info, []string{"p"})
	got := buildBlock(t, scrape(t, "lab", c.Metrics()))
	want := block{Hosts: []hostBlock{{Host: "lab", Roots: []rootBlock{}}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%+v", got)
	}
	// A root that failed on its first scan is listed, with its mode and no bytes.
	mustUpdate(t, c, doc(root("p", "sccache", false, false, 0)), t0)
	got = buildBlock(t, scrape(t, "lab", c.Metrics()))
	r := got.Hosts[0].Roots
	if len(r) != 1 || r[0].Mode != "sccache" || r[0].Bytes != nil || got.Hosts[0].GeneratedAt != "" {
		t.Errorf("%+v", got)
	}
}
