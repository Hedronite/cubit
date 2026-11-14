package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The release pipeline is code that runs once, on a tag, with write permission and
// signing identity. These tests hold its shape in the repo, so a change to it is
// a reviewed diff against a stated rule and not a surprise at tag time.

type workflow struct {
	On          any               `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Env         map[string]string `yaml:"env"`
	Jobs        map[string]wfJob  `yaml:"jobs"`
	raw         string
	file        string
}

type wfJob struct {
	Needs       any               `yaml:"needs"`
	Permissions map[string]string `yaml:"permissions"`
	Steps       []wfStep          `yaml:"steps"`
}

type wfStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	With map[string]any    `yaml:"with"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}

func loadWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	raw := read(t, ".github/workflows/"+name)
	var w workflow
	if err := yaml.Unmarshal(raw, &w); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	w.raw, w.file = string(raw), name
	return w
}

func workflowFiles(t *testing.T) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil || len(m) == 0 {
		t.Fatalf("no workflows found: %v", err)
	}
	var out []string
	for _, f := range m {
		out = append(out, filepath.Base(f))
	}
	return out
}

var pinned = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._/-]+@[0-9a-f]{40}$`)

// Every action is pinned to a full commit SHA, and the page that lists the pins
// lists it, with the same SHA.
func TestEveryActionIsPinnedAndListedInVersions(t *testing.T) {
	versions := string(read(t, "docs/versions.md"))
	count := 0
	for _, f := range workflowFiles(t) {
		w := loadWorkflow(t, f)
		for job, j := range w.Jobs {
			for _, s := range j.Steps {
				if s.Uses == "" {
					continue
				}
				count++
				if !pinned.MatchString(s.Uses) {
					t.Errorf("%s job %s: %q is not pinned to a full commit SHA", f, job, s.Uses)
					continue
				}
				name, sha, _ := strings.Cut(s.Uses, "@")
				if !strings.Contains(versions, "`"+name+"`") || !strings.Contains(versions, sha) {
					t.Errorf("%s: docs/versions.md does not list %s at %s", f, name, sha)
				}
			}
		}
	}
	if count == 0 {
		t.Fatal("found no actions")
	}
}

// The release workflow runs on a version tag and nothing else, and its write
// permissions are the minimum the job needs.
func TestReleaseWorkflowRunsOnlyOnTagsWithMinimumPermissions(t *testing.T) {
	w := loadWorkflow(t, "release.yml")

	on, ok := w.On.(map[string]any)
	if !ok || len(on) != 1 {
		t.Fatalf("release.yml must have exactly one trigger, got %#v", w.On)
	}
	push, ok := on["push"].(map[string]any)
	if !ok || len(push) != 1 {
		t.Fatalf("the trigger must be push with tags only, got %#v", on)
	}
	tags, ok := push["tags"].([]any)
	if !ok || len(tags) != 1 || tags[0] != "v*" {
		t.Errorf("push must filter on tags [v*], got %#v", push["tags"])
	}

	if len(w.Permissions) != 1 || w.Permissions["contents"] != "read" {
		t.Errorf("workflow permissions must be contents: read, got %v", w.Permissions)
	}
	rel, ok := w.Jobs["release"]
	if !ok {
		t.Fatal("no release job")
	}
	want := map[string]string{"contents": "write", "id-token": "write", "attestations": "write"}
	if len(rel.Permissions) != len(want) {
		t.Errorf("release job permissions %v, want exactly %v", rel.Permissions, want)
	}
	for k, v := range want {
		if rel.Permissions[k] != v {
			t.Errorf("release job permission %s: %q, want %q", k, rel.Permissions[k], v)
		}
	}
	if len(w.Jobs["test"].Permissions) != 0 {
		t.Errorf("the test job must take the read-only default, got %v", w.Jobs["test"].Permissions)
	}
	if needs := strings.Join(toStrings(rel.Needs), ","); needs != "test" {
		t.Errorf("release must need the test job, got %q", needs)
	}
	if w.Env["GOTOOLCHAIN"] != "local" {
		t.Error("GOTOOLCHAIN must be local, so the pinned Go is the one that builds")
	}
	// Credentials are not left in the checkout, and the release is published, not a draft.
	for _, s := range rel.Steps {
		if strings.HasPrefix(s.Uses, "actions/checkout@") && s.With["persist-credentials"] != false {
			t.Error("checkout must set persist-credentials: false")
		}
	}
	if !strings.Contains(w.raw, "gh release create") || strings.Contains(w.raw, "--draft") {
		t.Error("the release step must create a published release")
	}
	for _, need := range []string{"actions/attest-build-provenance@", "actions/attest-sbom@", "scripts/release.sh", "scripts/sbom.sh", "scripts/changelog-notes.sh", "--verify-tag"} {
		if !strings.Contains(w.raw, need) {
			t.Errorf("release.yml does not use %s", need)
		}
	}
	// No other workflow publishes.
	for _, f := range workflowFiles(t) {
		if f == "release.yml" {
			continue
		}
		o := loadWorkflow(t, f)
		if strings.Contains(o.raw, "gh release") || strings.Contains(o.raw, "attestations: write") || strings.Contains(o.raw, "contents: write") {
			t.Errorf("%s must not publish or take write permissions", f)
		}
	}
}

func toStrings(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, e.(string))
		}
		return out
	}
	return nil
}

// The reproducibility job checks out the commit under review, not the merge ref.
func TestReproducibleJobChecksOutThePullRequestHead(t *testing.T) {
	w := loadWorkflow(t, "ci.yml")
	j, ok := w.Jobs["reproducible"]
	if !ok {
		t.Fatal("ci.yml has no reproducible job")
	}
	var ref any
	for _, s := range j.Steps {
		if strings.HasPrefix(s.Uses, "actions/checkout@") {
			ref = s.With["ref"]
		}
	}
	if r, _ := ref.(string); !strings.Contains(r, "github.event.pull_request.head.sha") {
		t.Errorf("the reproducible job must check out the PR head SHA, got ref %v", ref)
	}
	if !strings.Contains(w.raw, "scripts/reproduce.sh") {
		t.Error("the reproducible job must run scripts/reproduce.sh")
	}
}

var goVer = regexp.MustCompile(`(?m)^GO_VERSION=(\S+)$`)

// One Go release builds the archives, and every place that names it agrees.
func TestTheReleaseGoVersionAgreesEverywhere(t *testing.T) {
	m := goVer.FindSubmatch(read(t, "scripts/release.env"))
	if m == nil {
		t.Fatal("scripts/release.env has no GO_VERSION")
	}
	want := string(m[1])
	for _, f := range []string{"release.yml", "ci.yml"} {
		w := loadWorkflow(t, f)
		found := 0
		for job, j := range w.Jobs {
			for _, s := range j.Steps {
				if !strings.HasPrefix(s.Uses, "actions/setup-go@") {
					continue
				}
				v, _ := s.With["go-version"].(string)
				if f == "ci.yml" && job != "reproducible" {
					continue // the test matrix runs stable and oldstable on purpose
				}
				found++
				if v != want {
					t.Errorf("%s job %s: go-version %q, release.env says %q", f, job, v, want)
				}
			}
		}
		if found == 0 {
			t.Errorf("%s: no pinned setup-go step", f)
		}
	}
	if !strings.Contains(string(read(t, "docs/versions.md")), "`go"+want+"`") {
		t.Errorf("docs/versions.md does not list go%s as the release toolchain", want)
	}
}

// Tool versions in the workflows and on the versions page are the same.
func TestToolVersionsMatchThePage(t *testing.T) {
	versions := string(read(t, "docs/versions.md"))
	ci := loadWorkflow(t, "ci.yml")
	rel := loadWorkflow(t, "release.yml")
	checks := []struct{ what, in, re, page string }{
		{"promtool", ci.raw, `PROM_VERSION: (\S+)`, "`%s`"},
		{"gitleaks", ci.raw, `GITLEAKS_VERSION: (\S+)`, "`%s`"},
		{"govulncheck", ci.raw, `govulncheck@(v\S+)`, "`%s`"},
		{"golangci-lint", ci.raw, `version: (v2\.\S+)`, "`%s`"},
		{"cyclonedx-gomod", rel.raw, `CYCLONEDX_GOMOD_VERSION: (\S+)`, "`%s`"},
	}
	for _, c := range checks {
		m := regexp.MustCompile(c.re).FindStringSubmatch(c.in)
		if m == nil {
			t.Errorf("%s: no version found in the workflow", c.what)
			continue
		}
		if !strings.Contains(versions, strings.Replace(c.page, "%s", m[1], 1)) {
			t.Errorf("%s: docs/versions.md does not list %s", c.what, m[1])
		}
	}
}

// The changelog has the first release's section, and the script that cuts it out
// finds it, refuses a tag without one, and does not run past the next heading.
func TestChangelogNotesForTheTag(t *testing.T) {
	if !strings.Contains(string(read(t, "CHANGELOG.md")), "\n## v0.1.0") {
		t.Fatal("CHANGELOG.md has no v0.1.0 section")
	}
	script := filepath.Join("..", "..", "scripts", "changelog-notes.sh")
	out, err := exec.Command("bash", script, "v0.1.0").CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	notes := string(out)
	if !strings.Contains(notes, "### Added") || strings.Contains(notes, "## v0.1.0") || strings.HasPrefix(notes, "\n") {
		t.Errorf("unexpected notes:\n%s", notes)
	}
	if out, err := exec.Command("bash", script, "v9.9.9").CombinedOutput(); err == nil {
		t.Errorf("a tag with no section must fail, got:\n%s", out)
	}
	// Only the tag's own section comes out, never the next one.
	dir := t.TempDir()
	cl := filepath.Join(dir, "CHANGELOG.md")
	if err := os.WriteFile(cl, []byte("# Changelog\n\n## v2.0.0\n\ntwo\n\n### Fixed\n\n- x\n\n## v1.0.0\n\none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, "v2.0.0")
	cmd.Env = append(os.Environ(), "CHANGELOG_FILE="+cl)
	got, err := cmd.Output()
	if err != nil || string(got) != "two\n\n### Fixed\n\n- x\n" {
		t.Errorf("%v: %q", err, got)
	}
}
