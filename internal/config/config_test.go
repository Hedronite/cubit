package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func readExample(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "cubit.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestExampleParses(t *testing.T) {
	cfg, fs := Parse(readExample(t))
	if cfg == nil || HasErrors(fs) {
		t.Fatalf("examples/cubit.yaml: %+v", fs)
	}
	if cfg.Host != "build-example" || len(cfg.Roots) != 4 {
		t.Errorf("decoded %+v", cfg)
	}
	r := cfg.Roots[0]
	if r.Mode != WorkspaceRoot || r.EffectiveDiscoveryDepth() != 3 || r.Names["app/cli"] != "app-cli" {
		t.Errorf("root 0: %+v", r)
	}
	if time.Duration(r.MaxDuration) != 30*time.Second || r.EffectiveMaxFiles() != 2_000_000 {
		t.Errorf("caps: %v %d", r.MaxDuration, r.MaxFiles)
	}
	if cfg.Line("roots/0/alias") == 0 {
		t.Error("lines were not recorded")
	}
}

func TestDefaults(t *testing.T) {
	cfg, fs := Parse([]byte("apiVersion: cubit.config/v0\nroots:\n  - {alias: a, path: /srv/a, mode: workspace-root}\n  - {alias: b, path: /srv/b, mode: target-dir}\n  - {alias: c, path: /srv/c, mode: cargo-home}\n"))
	if cfg == nil {
		t.Fatalf("%+v", fs)
	}
	if cfg.EffectiveInterval() != 5*time.Minute || cfg.EffectiveWorkers() < 1 || cfg.EffectiveWorkers() > 4 {
		t.Error("scan defaults")
	}
	a, b, c := cfg.Roots[0], cfg.Roots[1], cfg.Roots[2]
	if a.EffectiveMaxWorkspaces() != 64 || a.EffectiveMaxDuration() != 30*time.Second || !a.EmitProfiles() {
		t.Error("workspace-root defaults")
	}
	if b.WorkspaceName() != "b" || !b.EmitProfiles() {
		t.Error("target-dir defaults: name is the alias")
	}
	if c.EmitProfiles() {
		t.Error("cache modes never emit profiles")
	}
}

func TestRejects(t *testing.T) {
	good := "apiVersion: cubit.config/v0\nroots:\n  - alias: a\n    path: /srv/a\n    mode: workspace-root\n"
	cases := map[string]struct{ doc, msg string }{
		"unknown top-level key": {"apiVersion: cubit.config/v0\nlisten: x\nroots:\n  - {alias: a, path: /a, mode: sccache}\n", "listen"},
		"wrong schema":          {strings.Replace(good, "v0", "v1", 1), "cubit.config/v0"},
		"no roots":              {"apiVersion: cubit.config/v0\nroots: []\n", "roots"},
		"missing mode":          {"apiVersion: cubit.config/v0\nroots:\n  - {alias: a, path: /a}\n", "mode"},
		"bad alias":             {strings.Replace(good, "alias: a", "alias: A_b", 1), "alias"},
		"relative path":         {strings.Replace(good, "/srv/a", "srv/a", 1), "path"},
		"key from another mode": {"apiVersion: cubit.config/v0\nroots:\n  - {alias: a, path: /a, mode: cargo-home, discovery_depth: 2}\n", "discovery_depth"},
		"depth over 6":          {strings.Replace(good, "mode: workspace-root", "mode: workspace-root\n    discovery_depth: 9", 1), "discovery_depth"},
		"bad duration":          {strings.Replace(good, "mode: workspace-root", "mode: workspace-root\n    max_duration: 1.5h", 1), "max_duration"},
		"names key with ..":     {strings.Replace(good, "mode: workspace-root", "mode: workspace-root\n    names:\n      a/../b: x", 1), "names"},
		"anchor":                {"apiVersion: cubit.config/v0\nroots:\n  - &r {alias: a, path: /a, mode: sccache}\n", "anchors"},
		"two documents":         {good + "---\n" + good, "more than one"},
		"empty":                 {"", "empty"},
		"not a mapping":         {"- a\n- b\n", ""},
		"duplicate key":         {"apiVersion: cubit.config/v0\napiVersion: cubit.config/v0\nroots: []\n", ""},
		"non-string key":        {"apiVersion: cubit.config/v0\n1: x\nroots:\n  - {alias: a, path: /a, mode: sccache}\n", ""},
		"tab indent":            {"apiVersion: cubit.config/v0\nroots:\n\t- x\n", ""},
	}
	for name, c := range cases {
		cfg, fs := Parse([]byte(c.doc))
		if cfg != nil || !HasErrors(fs) {
			t.Errorf("%s: accepted", name)
			continue
		}
		if c.msg != "" {
			found := false
			for _, f := range fs {
				if strings.Contains(f.Message+" "+f.Field, c.msg) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: no finding mentions %q: %+v", name, c.msg, fs)
			}
		}
	}
}

func TestFindingsCarryLinesAndExcerpts(t *testing.T) {
	doc := "apiVersion: cubit.config/v0\nroots:\n  - alias: ok\n    path: /srv/ok\n    mode: sccache\n  - alias: Bad_Alias\n    path: /srv/b\n    mode: sccache\n  - alias: third\n    path: relative\n    mode: sccache\n"
	_, fs := Parse([]byte(doc))
	if len(fs) != 2 {
		t.Fatalf("both problems are reported at once: %+v", fs)
	}
	if fs[0].Line != 6 || fs[0].Field != "roots[1].alias" || !strings.Contains(fs[0].Excerpt, "Bad_Alias") || fs[0].Fix == "" {
		t.Errorf("first: %+v", fs[0])
	}
	if fs[1].Line != 10 || fs[1].Field != "roots[2].path" {
		t.Errorf("second: %+v", fs[1])
	}
}

func TestOversizedFileIsRejected(t *testing.T) {
	if cfg, fs := Parse(make([]byte, MaxBytes+1)); cfg != nil || !HasErrors(fs) {
		t.Error("a file over the limit must be rejected before it is parsed")
	}
}

func TestParseDuration(t *testing.T) {
	good := map[string]time.Duration{"30s": 30 * time.Second, "5m": 5 * time.Minute, "1h30m": 90 * time.Minute, "100ms": 100 * time.Millisecond, "2h": 2 * time.Hour}
	for s, want := range good {
		if got, err := ParseDuration(s); err != nil || got != want {
			t.Errorf("%q: %v %v", s, got, err)
		}
	}
	for _, s := range []string{"", "5", "1.5h", "5us", "5ns", "-5s", "1h 30m", "30 s", "s", "5S", "1d", "99999999999999999999h"} {
		if _, err := ParseDuration(s); err == nil {
			t.Errorf("%q should be rejected", s)
		}
	}
}

func FuzzConfigDecode(f *testing.F) {
	f.Add(readExampleF(f))
	for _, s := range []string{
		"", "{}", "apiVersion: cubit.config/v0\nroots: []\n", "- a\n", "a: &x [*x]\n", "apiVersion: cubit.config/v0\nroots:\n  - {alias: a, path: /a, mode: sccache}\n",
		"apiVersion: cubit.config/v0\nroots:\n  - alias: a\n    path: /a\n    mode: workspace-root\n    names: {x/y: z}\n    max_duration: 1h30m\n",
		"\x00\xff", strings.Repeat("a: [", 400),
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, fs := Parse(data) // must never panic
		if (cfg == nil) != HasErrors(fs) {
			t.Fatalf("cfg nil %v but findings %+v", cfg == nil, fs)
		}
		if cfg == nil {
			for _, x := range fs {
				if x.Message == "" {
					t.Fatalf("a finding without a message: %+v", x)
				}
			}
			return
		}
		// An accepted config satisfies the properties the rest of Cubit relies on.
		alias := regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
		if len(cfg.Roots) < 1 || len(cfg.Roots) > 32 {
			t.Fatalf("%d roots", len(cfg.Roots))
		}
		for _, r := range cfg.Roots {
			if !alias.MatchString(r.Alias) {
				t.Fatalf("alias %q", r.Alias)
			}
			if !strings.HasPrefix(r.Path, "/") && !strings.HasPrefix(r.Path, "~/") {
				t.Fatalf("path %q", r.Path)
			}
			if r.EffectiveMaxFiles() < 1000 || r.EffectiveDiscoveryDepth() < 1 || r.EffectiveDiscoveryDepth() > 6 {
				t.Fatalf("caps out of range: %+v", r)
			}
		}
		_ = Check(cfg, "/home/user") // must never panic
	})
}

func readExampleF(f *testing.F) []byte {
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "cubit.yaml"))
	if err != nil {
		f.Fatal(err)
	}
	return b
}
