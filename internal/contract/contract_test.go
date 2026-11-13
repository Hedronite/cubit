package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

// repoRoot finds the directory holding go.mod, whatever the test's cwd.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func read(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func compile(t *testing.T, file string) *jsonschema.Schema {
	t.Helper()
	raw := read(t, "schemas/"+file)
	var meta struct {
		ID string `json:"$id"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil || meta.ID == "" {
		t.Fatalf("%s: no $id (%v)", file, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(meta.ID, doc); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	s, err := c.Compile(meta.ID)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return s
}

// asJSON turns any decoded value into the JSON data model the validator wants.
func asJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func decodeYAML(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func decodeJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// clone deep-copies a decoded document so a case can mutate it freely.
func clone(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return decodeJSON(t, b)
}

func firstRoot(t *testing.T, doc map[string]any, mode string) map[string]any {
	t.Helper()
	for _, r := range doc["roots"].([]any) {
		rm := r.(map[string]any)
		if rm["mode"] == mode {
			return rm
		}
	}
	t.Fatalf("example has no %s root", mode)
	return nil
}

func TestExampleConfigIsValid(t *testing.T) {
	s := compile(t, "cubit.config.v0.schema.json")
	if err := s.Validate(asJSON(t, decodeYAML(t, read(t, "examples/cubit.yaml")))); err != nil {
		t.Fatalf("examples/cubit.yaml does not satisfy cubit.config/v0:\n%v", err)
	}
}

func TestConfigSchemaRejects(t *testing.T) {
	s := compile(t, "cubit.config.v0.schema.json")
	base := decodeYAML(t, read(t, "examples/cubit.yaml"))

	cases := map[string]func(m map[string]any){
		"unknown top-level key":     func(m map[string]any) { m["listen"] = "0.0.0.0:9101" },
		"wrong schema value":        func(m map[string]any) { m["schema"] = "cubit.config/v1" },
		"no roots":                  func(m map[string]any) { m["roots"] = []any{} },
		"host with uppercase":       func(m map[string]any) { m["host"] = "Build-Example" },
		"interval not a duration":   func(m map[string]any) { m["scan"] = map[string]any{"interval": "5 minutes"} },
		"too many workers":          func(m map[string]any) { m["scan"] = map[string]any{"max_workers": 64} },
		"unknown refuse key":        func(m map[string]any) { m["refuse"] = map[string]any{"allow": []any{"/srv/x"}} },
		"refuse entry not absolute": func(m map[string]any) { m["refuse"] = map[string]any{"extra": []any{"relative/dir"}} },
		"refuse entries duplicated": func(m map[string]any) { m["refuse"] = map[string]any{"extra": []any{"/srv/x", "/srv/x"}} },
		"alias with uppercase":      func(m map[string]any) { firstRoot(t, m, "workspace-root")["alias"] = "Example" },
		"alias too long":            func(m map[string]any) { firstRoot(t, m, "workspace-root")["alias"] = strings.Repeat("a", 33) },
		"path relative":             func(m map[string]any) { firstRoot(t, m, "workspace-root")["path"] = "projects/x" },
		"unknown mode":              func(m map[string]any) { firstRoot(t, m, "workspace-root")["mode"] = "recursive" },
		"missing mode":              func(m map[string]any) { delete(firstRoot(t, m, "workspace-root"), "mode") },
		"max_files below floor":     func(m map[string]any) { firstRoot(t, m, "workspace-root")["max_files"] = 10 },
		"discovery_depth over cap":  func(m map[string]any) { firstRoot(t, m, "workspace-root")["discovery_depth"] = 7 },
		"max_workspaces over ceil":  func(m map[string]any) { firstRoot(t, m, "workspace-root")["max_workspaces"] = 300 },
		"name is not a slug": func(m map[string]any) {
			firstRoot(t, m, "workspace-root")["names"] = map[string]any{"app/cli": "App/CLI"}
		},
		"name key is absolute":      func(m map[string]any) { firstRoot(t, m, "workspace-root")["names"] = map[string]any{"/abs": "x"} },
		"name on a workspace-root":  func(m map[string]any) { firstRoot(t, m, "workspace-root")["name"] = "x" },
		"names on a target-dir":     func(m map[string]any) { firstRoot(t, m, "target-dir")["names"] = map[string]any{"a": "b"} },
		"discovery on a target-dir": func(m map[string]any) { firstRoot(t, m, "target-dir")["discovery_depth"] = 2 },
		"profiles on a cargo-home":  func(m map[string]any) { firstRoot(t, m, "cargo-home")["profiles"] = true },
		"max_workspaces on sccache": func(m map[string]any) { firstRoot(t, m, "sccache")["max_workspaces"] = 4 },
		"unknown root key":          func(m map[string]any) { firstRoot(t, m, "sccache")["label"] = "x" },
		"max_duration not a string": func(m map[string]any) { firstRoot(t, m, "sccache")["max_duration"] = 30 },
		"max_duration fractional":   func(m map[string]any) { firstRoot(t, m, "sccache")["max_duration"] = "1.5h" },
		"host with leading dash":    func(m map[string]any) { m["host"] = "-build" },
		"names key with dot-dot": func(m map[string]any) {
			firstRoot(t, m, "workspace-root")["names"] = map[string]any{"a/../b": "x"}
		},
		"names key with trailing slash": func(m map[string]any) {
			firstRoot(t, m, "workspace-root")["names"] = map[string]any{"app/": "x"}
		},
		"names key with backslash": func(m map[string]any) {
			firstRoot(t, m, "workspace-root")["names"] = map[string]any{`app\cli`: "x"}
		},
		"names key with NUL": func(m map[string]any) {
			firstRoot(t, m, "workspace-root")["names"] = map[string]any{"app\x00cli": "x"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			doc := clone(t, base)
			mutate(doc)
			if err := s.Validate(asJSON(t, doc)); err == nil {
				t.Fatal("schema accepted a document it must reject")
			}
		})
	}
}

func TestExampleScanIsValid(t *testing.T) {
	s := compile(t, "cubit.scan.v0.schema.json")
	if err := s.Validate(asJSON(t, decodeJSON(t, read(t, "examples/scan.json")))); err != nil {
		t.Fatalf("examples/scan.json does not satisfy cubit.scan/v0:\n%v", err)
	}
}

func TestScanSchemaRejects(t *testing.T) {
	s := compile(t, "cubit.scan.v0.schema.json")
	base := decodeJSON(t, read(t, "examples/scan.json"))

	proj := func(m map[string]any) map[string]any { return firstRoot(t, m, "workspace-root") }
	ws0 := func(m map[string]any) map[string]any {
		return proj(m)["workspaces"].([]any)[0].(map[string]any)
	}

	cases := map[string]func(m map[string]any){
		"unknown top-level key":    func(m map[string]any) { m["path"] = "/srv/x" },
		"wrong schema value":       func(m map[string]any) { m["schema"] = "cubit.scan/v1" },
		"timestamp not UTC":        func(m map[string]any) { m["generated_at"] = "2026-01-15T12:00:00+02:00" },
		"host with uppercase":      func(m map[string]any) { m["host"] = "Build" },
		"a path key on a root":     func(m map[string]any) { proj(m)["path"] = "/srv/example/projects" },
		"negative bytes":           func(m map[string]any) { proj(m)["bytes"] = -1 },
		"fractional bytes":         func(m map[string]any) { proj(m)["bytes"] = 1.5 },
		"alias with a slash":       func(m map[string]any) { proj(m)["alias"] = "a/b" },
		"unknown mode":             func(m map[string]any) { proj(m)["mode"] = "recursive" },
		"failed without an error":  func(m map[string]any) { r := firstRoot(t, m, "target-dir"); delete(r, "error") },
		"partial without an error": func(m map[string]any) { delete(proj(m), "error") },
		"unknown error reason":     func(m map[string]any) { proj(m)["error"] = "boom" },
		"unknown skip reason":      func(m map[string]any) { proj(m)["skipped"].([]any)[0].(map[string]any)["reason"] = "boom" },
		"skip count of zero":       func(m map[string]any) { proj(m)["skipped"].([]any)[0].(map[string]any)["count"] = 0 },
		"unknown cache kind": func(m map[string]any) {
			firstRoot(t, m, "cargo-home")["caches"].([]any)[0].(map[string]any)["kind"] = "target"
		},
		"absolute workspace name":    func(m map[string]any) { ws0(m)["name"] = "/srv/example/app" },
		"dot-dot workspace name":     func(m map[string]any) { ws0(m)["name"] = "../app" },
		"dot workspace component":    func(m map[string]any) { ws0(m)["name"] = "app/./cli" },
		"uppercase workspace name":   func(m map[string]any) { ws0(m)["name"] = "App" },
		"workspace name too long":    func(m map[string]any) { ws0(m)["name"] = strings.Repeat("a", 65) },
		"profile missing":            func(m map[string]any) { delete(ws0(m)["profiles"].(map[string]any), "doc") },
		"profile unknown":            func(m map[string]any) { ws0(m)["profiles"].(map[string]any)["bench"] = 1 },
		"warning without a root":     func(m map[string]any) { delete(m["warnings"].([]any)[0].(map[string]any), "root") },
		"warning with a path":        func(m map[string]any) { m["warnings"].([]any)[0].(map[string]any)["path"] = "/etc" },
		"missing caps":               func(m map[string]any) { delete(m, "caps") },
		"per-root caps missing":      func(m map[string]any) { delete(proj(m), "caps") },
		"workspace with extra field": func(m map[string]any) { ws0(m)["root"] = "x" },
		"host with leading dash":     func(m map[string]any) { m["host"] = "-build" },
		"partial with no success": func(m map[string]any) {
			r := firstRoot(t, m, "target-dir")
			r["partial"], r["error"] = true, "deadline_exceeded"
		},
		"success without last_success_at": func(m map[string]any) { delete(proj(m), "last_success_at") },
		"never-succeeded root with bytes": func(m map[string]any) {
			firstRoot(t, m, "target-dir")["bytes"] = 5
		},
		"never-succeeded root with workspaces": func(m map[string]any) {
			r := firstRoot(t, m, "target-dir")
			r["workspaces"] = []any{map[string]any{"name": "shared", "bytes": 0}}
		},
		"old workspaces_total field":    func(m map[string]any) { proj(m)["workspaces_total"] = 3 },
		"missing profiles_emitted":      func(m map[string]any) { delete(proj(m), "profiles_emitted") },
		"profiles_emitted without data": func(m map[string]any) { delete(ws0(m), "profiles") },
		"profiles when not emitted": func(m map[string]any) {
			proj(m)["profiles_emitted"] = false
		},
		"discovery_depth on a cargo-home": func(m map[string]any) {
			firstRoot(t, m, "cargo-home")["discovery_depth"] = 3
		},
		"workspace-root without discovery_depth": func(m map[string]any) { delete(proj(m), "discovery_depth") },
		"removed error reason canceled":          func(m map[string]any) { proj(m)["error"] = "canceled" },
		"removed skip reason mount_skipped": func(m map[string]any) {
			proj(m)["skipped"].([]any)[0].(map[string]any)["reason"] = "mount_skipped"
		},
		"skip reason in a warning": func(m map[string]any) {
			m["warnings"].([]any)[0].(map[string]any)["reason"] = "permission_denied"
		},
		"bad last_success_at": func(m map[string]any) { proj(m)["last_success_at"] = "yesterday" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			doc := clone(t, base)
			mutate(doc)
			if err := s.Validate(asJSON(t, doc)); err == nil {
				t.Fatal("schema accepted a document it must reject")
			}
		})
	}
}

// TestExampleScanInvariants checks the rules the schema cannot express, on the
// example document. C1a re-checks them on real scan results.
func TestExampleScanInvariants(t *testing.T) {
	var doc struct {
		Roots []struct {
			Alias      string `json:"alias"`
			Mode       string `json:"mode"`
			Bytes      int64  `json:"bytes"`
			Success    bool   `json:"success"`
			Workspaces []struct {
				Name     string           `json:"name"`
				Bytes    int64            `json:"bytes"`
				Profiles map[string]int64 `json:"profiles"`
			} `json:"workspaces"`
			Unlabeled      int64 `json:"workspaces_unlabeled"`
			UnlabeledBytes int64 `json:"workspaces_unlabeled_bytes"`
			Total          int   `json:"workspaces_found"`
			Caches         []struct {
				Kind  string `json:"kind"`
				Bytes int64  `json:"bytes"`
			} `json:"caches"`
			Skipped []struct {
				Reason string `json:"reason"`
			} `json:"skipped"`
		} `json:"roots"`
		Warnings []struct {
			Root   string `json:"root"`
			Reason string `json:"reason"`
		} `json:"warnings"`
	}
	if err := json.Unmarshal(read(t, "examples/scan.json"), &doc); err != nil {
		t.Fatal(err)
	}

	if !sort.SliceIsSorted(doc.Roots, func(i, j int) bool { return doc.Roots[i].Alias < doc.Roots[j].Alias }) {
		t.Error("roots are not sorted by alias")
	}
	if !sort.SliceIsSorted(doc.Warnings, func(i, j int) bool {
		a, b := doc.Warnings[i], doc.Warnings[j]
		return a.Root < b.Root || (a.Root == b.Root && a.Reason < b.Reason)
	}) {
		t.Error("warnings are not sorted by root, then reason")
	}

	for _, r := range doc.Roots {
		if !r.Success {
			continue // a failed root carries its last good values, or none
		}
		switch r.Mode {
		case "workspace-root", "target-dir":
			sum := r.UnlabeledBytes
			for _, w := range r.Workspaces {
				sum += w.Bytes
				if w.Profiles != nil {
					var p int64
					for _, b := range w.Profiles {
						p += b
					}
					if p != w.Bytes {
						t.Errorf("%s/%s: profiles sum to %d, workspace is %d", r.Alias, w.Name, p, w.Bytes)
					}
				}
			}
			if sum != r.Bytes {
				t.Errorf("%s: labelled + unlabeled = %d, root bytes are %d", r.Alias, sum, r.Bytes)
			}
			if int(r.Unlabeled)+len(r.Workspaces) != r.Total {
				t.Errorf("%s: %d labelled + %d unlabeled != %d total", r.Alias, len(r.Workspaces), r.Unlabeled, r.Total)
			}
			if !sort.SliceIsSorted(r.Workspaces, func(i, j int) bool { return r.Workspaces[i].Name < r.Workspaces[j].Name }) {
				t.Errorf("%s: workspaces are not sorted by name", r.Alias)
			}
		default:
			var sum int64
			for _, c := range r.Caches {
				sum += c.Bytes
			}
			if sum != r.Bytes {
				t.Errorf("%s: caches sum to %d, root bytes are %d", r.Alias, sum, r.Bytes)
			}
			if !sort.SliceIsSorted(r.Caches, func(i, j int) bool { return r.Caches[i].Kind < r.Caches[j].Kind }) {
				t.Errorf("%s: caches are not sorted by kind", r.Alias)
			}
		}
		if !sort.SliceIsSorted(r.Skipped, func(i, j int) bool { return r.Skipped[i].Reason < r.Skipped[j].Reason }) {
			t.Errorf("%s: skipped is not sorted by reason", r.Alias)
		}
	}
}

// TestExampleScanHasNoPaths: no string value in a scan document looks like a
// filesystem path.
func TestExampleScanHasNoPaths(t *testing.T) {
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case string:
			if strings.HasPrefix(x, "/") || strings.HasPrefix(x, "~") {
				t.Errorf("a scan document carries a path-like value: %q", x)
			}
		}
	}
	walk(decodeJSON(t, read(t, "examples/scan.json")))
}

// ---- metric table ----------------------------------------------------------

type metric struct {
	name, typ string
	labels    []string
}

var (
	backticked = regexp.MustCompile("`([^`]+)`")
	metricName = regexp.MustCompile(`^cubit_[a-z0-9_]+$`)
)

func parseMetrics(t *testing.T) []metric {
	t.Helper()
	var out []metric
	for _, line := range strings.Split(string(read(t, "docs/metrics.md")), "\n") {
		if !strings.HasPrefix(line, "| `cubit_") {
			continue
		}
		cols := strings.Split(line, "|")
		if len(cols) < 5 {
			t.Fatalf("metric row has too few columns: %q", line)
		}
		m := metric{
			name: strings.Trim(strings.TrimSpace(cols[1]), "`"),
			typ:  strings.TrimSpace(cols[2]),
		}
		for _, l := range backticked.FindAllStringSubmatch(cols[3], -1) {
			m.labels = append(m.labels, l[1])
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		t.Fatal("no metric rows found in docs/metrics.md")
	}
	return out
}

func TestMetricTable(t *testing.T) {
	// Count and flag names that carry no unit suffix, on purpose.
	unitless := map[string]bool{
		"cubit_workspaces":           true,
		"cubit_workspaces_unlabeled": true,
		"cubit_skipped_entries":      true,
		"cubit_scan_success":         true,
		"cubit_scan_partial":         true,
		"cubit_build_info":           true,
	}
	bannedSuffix := []string{"_kb", "_mb", "_gb", "_mib", "_gib", "_kib", "_ms", "_millis", "_percent", "_pct", "_ratio"}
	allowedLabels := map[string]bool{
		"root": true, "workspace": true, "profile": true, "kind": true,
		"reason": true, "result": true, "version": true, "revision": true, "go_version": true,
	}

	seen := map[string]bool{}
	for _, m := range parseMetrics(t) {
		if !metricName.MatchString(m.name) {
			t.Errorf("%s: not a valid cubit_ metric name", m.name)
		}
		if seen[m.name] {
			t.Errorf("%s: listed twice", m.name)
		}
		seen[m.name] = true

		switch m.typ {
		case "gauge":
			if strings.HasSuffix(m.name, "_total") {
				t.Errorf("%s: a gauge must not end in _total", m.name)
			}
		case "counter":
			if !strings.HasSuffix(m.name, "_total") {
				t.Errorf("%s: a counter must end in _total", m.name)
			}
		default:
			t.Errorf("%s: type %q is neither gauge nor counter", m.name, m.typ)
		}
		for _, s := range bannedSuffix {
			if strings.HasSuffix(m.name, s) {
				t.Errorf("%s: base units only, not %s", m.name, s)
			}
		}
		if !unitless[m.name] && m.typ == "gauge" &&
			!strings.HasSuffix(m.name, "_bytes") && !strings.HasSuffix(m.name, "_seconds") {
			t.Errorf("%s: a gauge needs _bytes or _seconds, or an entry in the unitless list", m.name)
		}
		for _, l := range m.labels {
			if !allowedLabels[l] {
				t.Errorf("%s: label %q is not allowed (no host, path or instance labels)", m.name, l)
			}
		}
		if strings.HasPrefix(m.name, "cubit_workspace_") && !contains(m.labels, "workspace") {
			t.Errorf("%s: per-workspace metric without a workspace label", m.name)
		}
		if m.name != "cubit_build_info" && m.name != "cubit_scans_total" && !contains(m.labels, "root") {
			t.Errorf("%s: every metric except build info and scans_total carries a root label", m.name)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// docEnum reads a "- `name`: `a`, `b`" line from docs/metrics.md.
func docEnum(t *testing.T, name string) []string {
	t.Helper()
	prefix := "- `" + name + "`:"
	for _, line := range strings.Split(string(read(t, "docs/metrics.md")), "\n") {
		if strings.HasPrefix(line, prefix) {
			var vals []string
			for _, m := range backticked.FindAllStringSubmatch(strings.TrimPrefix(line, prefix), -1) {
				vals = append(vals, m[1])
			}
			sort.Strings(vals)
			return vals
		}
	}
	t.Fatalf("docs/metrics.md has no %q enum line", name)
	return nil
}

func sorted(in []any) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, v.(string))
	}
	sort.Strings(out)
	return out
}

func same(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

// TestEnumsMatchSchema: the label values in the metric table and the closed
// enums in the scan schema are the same sets.
func TestEnumsMatchSchema(t *testing.T) {
	var sch struct {
		Defs struct {
			SkipReason struct {
				Enum []any `json:"enum"`
			} `json:"skipReason"`
			ErrorReason struct {
				Enum []any `json:"enum"`
			} `json:"errorReason"`
			WarningReason struct {
				Enum []any `json:"enum"`
			} `json:"warningReason"`
			Profiles struct {
				Properties map[string]any `json:"properties"`
			} `json:"profiles"`
			Cache struct {
				Properties struct {
					Kind struct {
						Enum []any `json:"enum"`
					} `json:"kind"`
				} `json:"properties"`
			} `json:"cache"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(read(t, "schemas/cubit.scan.v0.schema.json"), &sch); err != nil {
		t.Fatal(err)
	}
	profiles := make([]string, 0, len(sch.Defs.Profiles.Properties))
	for k := range sch.Defs.Profiles.Properties {
		profiles = append(profiles, k)
	}
	sort.Strings(profiles)

	for name, want := range map[string][]string{
		"profile":        profiles,
		"kind":           sorted(sch.Defs.Cache.Properties.Kind.Enum),
		"skip_reason":    sorted(sch.Defs.SkipReason.Enum),
		"error_reason":   sorted(sch.Defs.ErrorReason.Enum),
		"warning_reason": sorted(sch.Defs.WarningReason.Enum),
	} {
		if got := docEnum(t, name); !same(got, want) {
			t.Errorf("%s: docs/metrics.md has %v, the scan schema has %v", name, got, want)
		}
	}
	if got := docEnum(t, "result"); !same(got, []string{"failed", "ok", "partial"}) {
		t.Errorf("result: unexpected values %v", got)
	}
}

// ---- rules -----------------------------------------------------------------

func TestRulesUseOnlyDocumentedMetrics(t *testing.T) {
	known := map[string]bool{}
	for _, m := range parseMetrics(t) {
		known[m.name] = true
	}
	raw := read(t, "examples/rules/cubit.rules.yml")
	for _, name := range regexp.MustCompile(`cubit_[a-z0-9_]+`).FindAllString(string(raw), -1) {
		if !known[name] {
			t.Errorf("rules use %s, which docs/metrics.md does not define", name)
		}
	}
}

// TestRulesAreYellowOnly: every example alert carries severity "warning" and
// nothing that reads as critical.
func TestRulesAreYellowOnly(t *testing.T) {
	var f struct {
		Groups []struct {
			Rules []struct {
				Alert  string            `yaml:"alert"`
				Labels map[string]string `yaml:"labels"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(read(t, "examples/rules/cubit.rules.yml"), &f); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, g := range f.Groups {
		for _, r := range g.Rules {
			n++
			if r.Alert == "" {
				t.Errorf("a rule has no alert name")
			}
			if sev := r.Labels["severity"]; sev != "warning" {
				t.Errorf("%s: severity is %q; Cubit-derived alerts are warning only", r.Alert, sev)
			}
		}
	}
	if n == 0 {
		t.Fatal("no rules found")
	}
}

// ---- refuse list -----------------------------------------------------------

func TestRefuseList(t *testing.T) {
	// The entries spec §3 requires. Removing one is a spec change.
	want := []string{
		"~/.ssh", "~/.gnupg", "~/.aws", "~/.kube", "~/.config/gh", "~/.netrc",
		"~/.cargo/credentials*", "~/Library/Keychains", "~/Library/CloudStorage",
		"~/Library/Mobile Documents", "/etc/shadow", "/etc/ssh", "/proc", "/sys", "/dev",
	}
	doc := string(read(t, "docs/refuse-list.md"))
	_, rest, ok := strings.Cut(doc, "```refuse\n")
	if !ok {
		t.Fatal("docs/refuse-list.md has no ```refuse block")
	}
	block, _, ok := strings.Cut(rest, "```")
	if !ok {
		t.Fatal("the refuse block is not closed")
	}
	var got []string
	seen := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(block), "\n") {
		if !strings.HasPrefix(l, "~/") && !strings.HasPrefix(l, "/") && !strings.HasPrefix(l, "**/") {
			t.Errorf("refuse entry %q is not home-relative (~/), absolute (/) or any-depth (**/)", l)
		}
		if strings.Contains(l, "..") || strings.HasSuffix(l, "/") {
			t.Errorf("refuse entry %q has a .. component or a trailing slash", l)
		}
		// Matching is case-insensitive, so a duplicate differing only in case is a duplicate.
		if k := strings.ToLower(l); seen[k] {
			t.Errorf("refuse entry %q is listed twice", l)
		} else {
			seen[k] = true
		}
		got = append(got, l)
	}
	// Entries may be added after C0, never removed: the spec's list is a floor.
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("the spec's refuse entry %q is missing from docs/refuse-list.md", w)
		}
	}
}
