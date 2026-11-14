package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Hedronite/cubit/internal/contain"
)

// ExpandPath turns a config path into a cleaned absolute one. `~/x` is under
// home. A path holding a "." or ".." component is rejected, not quietly cleaned.
func ExpandPath(p, home string) (string, error) {
	for _, c := range strings.Split(p, "/") {
		if c == "." || c == ".." {
			return "", fmt.Errorf("path holds a %q component", c)
		}
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if !filepath.IsAbs(home) {
			return "", fmt.Errorf("path starts with ~/ but the home directory is unknown")
		}
		p = filepath.Join(home, rest)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path is not absolute")
	}
	return filepath.Clean(p), nil
}

// Refuser builds the refuse list for this config: the built-in entries plus
// refuse.vault and refuse.extra.
func (c *Config) Refuser(home string) (*contain.Refuser, error) {
	return contain.NewRefuser(home, c.Refuse.Extra, c.Refuse.Vault)
}

// ResolvedRoot is one root with its path expanded.
type ResolvedRoot struct {
	Root
	Abs string // cleaned absolute path as configured
}

// Check runs the checks a schema cannot, all at once. It reads the filesystem
// only to resolve symlinks and to see whether a root exists; it never reads a
// refused path. home is the invoking user's home directory. No message holds a
// refused location, only its class.
func Check(c *Config, home string) []Finding {
	var fs []Finding
	add := func(sev, code, field, msg, fix string) {
		line := c.Line(ptr(field))
		f := Finding{Code: code, Severity: sev, Field: field, Line: line, Message: msg, Fix: fix}
		if sev != Info {
			f.Excerpt = c.excerpt(line)
		}
		fs = append(fs, f)
	}
	ref, err := c.Refuser(home)
	if err != nil {
		add(Error, CodeHomeUnknown, "refuse", err.Error(), "set HOME, or write absolute paths in refuse.vault and refuse.extra.")
		return finish(fs)
	}

	if iv := c.EffectiveInterval(); iv < MinInterval {
		add(Error, CodeIntervalFloor, "scan.interval", fmt.Sprintf("scan.interval '%s' is below the %s floor", iv, MinInterval), "use 30s or more; a faster interval only burns I/O, and serve runs one scan at a time.")
	}

	type resolved struct {
		abs, real string
		comps     []string
		realComps []string
	}
	res := make([]resolved, len(c.Roots))
	aliases := map[string]int{}
	for i := range c.Roots {
		r := &c.Roots[i]
		at := func(field string) string { return fmt.Sprintf("roots[%d].%s", i, field) }
		if j, dup := aliases[r.Alias]; dup {
			add(Error, CodeDuplicateAlias, at("alias"), fmt.Sprintf("%s '%s' is already used by roots[%d] (line %d)", at("alias"), r.Alias, j, c.Line(fmt.Sprintf("roots/%d/alias", j))), "give one of them a distinct alias slug.")
		} else {
			aliases[r.Alias] = i
		}
		if d := r.EffectiveMaxDuration(); d >= c.EffectiveInterval() {
			add(Error, CodeDurationGE, at("max_duration"), fmt.Sprintf("%s '%s' is not under scan.interval '%s'; a scan could still be running when the next is due", at("max_duration"), d, c.EffectiveInterval()), "set max_duration below scan.interval, or raise the interval.")
		}
		checkNames(c, i, add)
		abs, err := ExpandPath(r.Path, home)
		if err != nil {
			add(Error, CodePathInvalid, at("path"), fmt.Sprintf("%s: %s", at("path"), err), "write an absolute path, or ~/... with no . or .. parts.")
			continue
		}
		res[i].abs = abs
		res[i].comps = contain.Components(abs)
		if class, bad := ref.Inside(res[i].comps); bad {
			add(Error, CodeRefusedRoot, at("path"), fmt.Sprintf("%s sits inside a %s refuse-list prefix", at("path"), class), "move the root out of every refused prefix; the walk will not read inside one.")
			continue
		}
		real, err := filepath.EvalSymlinks(abs)
		switch {
		case err == nil:
			res[i].real = real
			res[i].realComps = contain.Components(real)
			if class, bad := ref.Inside(res[i].realComps); bad {
				add(Error, CodeRefusedRoot, at("path"), fmt.Sprintf("%s resolves through a symlink into a %s refuse-list prefix", at("path"), class), "point the root at a path outside every refused prefix.")
				continue
			}
			if classes := ref.Under(res[i].realComps); len(classes) > 0 {
				add(Info, CodeRefusedInside, at("path"), fmt.Sprintf("%s '%s': %s refuse prefix applies under this root; those subtrees are skipped and counted", at(""), r.Alias, strings.Join(classes, " and ")), "")
			}
			if fi, err := os.Stat(real); err == nil && !fi.IsDir() {
				add(Error, CodeNotDirectory, at("path"), fmt.Sprintf("%s is not a directory", at("path")), "point the root at a directory.")
				continue
			}
			if r.Mode == WorkspaceRoot && isWorkspace(real) {
				add(Warning, CodeRootIsWorkspace, at("mode"), fmt.Sprintf("%s '%s' is workspace-root but is itself a Cargo workspace (it holds Cargo.toml and target/); a workspace-root measures the workspaces below it, so this scan finds none", at(""), r.Alias), "point a target-dir root at the target/ directory inside root '"+r.Alias+"'.")
			}
			if r.Mode == WorkspaceRoot && looksLikeTarget(real) {
				add(Warning, CodeLooksLikeTarget, at("mode"), fmt.Sprintf("%s '%s' is workspace-root but holds debug/ or release/ at the top level, so it looks like one target dir", at(""), r.Alias), "if so, set mode: target-dir.")
			}
		case os.IsNotExist(err):
			add(Warning, CodeRootMissing, at("path"), fmt.Sprintf("%s '%s' does not exist now; a scan reports root_missing until it does", at(""), r.Alias), "")
		default:
			add(Warning, CodeRootUnreadable, at("path"), fmt.Sprintf("%s '%s' cannot be resolved now (%s); a scan reports root_unreadable", at(""), r.Alias, errClass(err)), "")
		}
	}

	for i := range res {
		for j := i + 1; j < len(res); j++ {
			a, b := res[i], res[j]
			if a.abs == "" || b.abs == "" {
				continue
			}
			ra, rb := &c.Roots[i], &c.Roots[j]
			switch {
			case a.real != "" && a.real == b.real:
				add(Error, CodeSameRealPath, fmt.Sprintf("roots[%d].path", j), fmt.Sprintf("roots[%d] '%s' resolves to the same directory as roots[%d] '%s'; two roots would measure one tree", j, rb.Alias, i, ra.Alias), "drop one of the two roots, or point it at a different directory.")
			case nested(a.comps, b.comps) || (a.real != "" && b.real != "" && nested(a.realComps, b.realComps)):
				outer, inner, oi, ii := ra, rb, i, j
				if len(a.comps) > len(b.comps) {
					outer, inner, oi, ii = rb, ra, j, i
				}
				add(Error, CodeOverlappingRoot, fmt.Sprintf("roots[%d].path", j), fmt.Sprintf("roots[%d] ('%s') lies inside roots[%d] ('%s'); its files would be counted in both roots", ii, inner.Path, oi, outer.Path), "drop the nested root, or give it a path outside the other root.")
			}
		}
	}

	for i := range c.Roots {
		r := &c.Roots[i]
		add(Info, CodeModeEcho, fmt.Sprintf("roots[%d].mode", i), fmt.Sprintf("roots[%d] '%s': mode %s", i, r.Alias, r.Mode), "")
	}
	return finish(fs)
}

func ptr(path string) string {
	// roots[2].alias -> roots/2/alias
	r := strings.NewReplacer("[", "/", "]", "", ".", "/")
	return r.Replace(path)
}

func nested(a, b []string) bool {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true // equal or one is a prefix of the other
}

// isWorkspace reports whether dir holds Cargo.toml and a target directory.
func isWorkspace(dir string) bool {
	c, err := os.Lstat(filepath.Join(dir, "Cargo.toml"))
	if err != nil || c.IsDir() {
		return false
	}
	t, err := os.Stat(filepath.Join(dir, "target"))
	return err == nil && t.IsDir()
}

func looksLikeTarget(dir string) bool {
	for _, n := range []string{"debug", "release"} {
		if fi, err := os.Lstat(filepath.Join(dir, n)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

func errClass(err error) string { return contain.SkipReason(err) }

func checkNames(c *Config, i int, add func(sev, code, field, msg, fix string)) {
	r := &c.Roots[i]
	if len(r.Names) == 0 {
		return
	}
	keys := make([]string, 0, len(r.Names))
	for k := range r.Names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	owner := map[string]string{}
	for _, k := range keys {
		slug := r.Names[k]
		if prev, dup := owner[slug]; dup {
			add(Error, CodeDuplicateSlug, fmt.Sprintf("roots[%d].names", i), fmt.Sprintf("roots[%d].names: '%s' and '%s' both declare the slug '%s'; two workspaces would share one label value", i, prev, k, slug), "give each relative path a distinct slug.")
			continue
		}
		owner[slug] = k
	}
	for _, k := range keys {
		for other, slug := range r.Names {
			if other != k && Label(k) == slug {
				add(Error, CodeDuplicateSlug, fmt.Sprintf("roots[%d].names", i), fmt.Sprintf("roots[%d].names: the slug '%s' declared for '%s' equals the derived label of '%s'; two workspaces would share one label value", i, slug, other, k), "give each relative path a distinct slug.")
			}
		}
	}
}

func finish(fs []Finding) []Finding {
	sortFindings(fs)
	return fs
}

// HasErrors reports whether any finding is an error.
func HasErrors(fs []Finding) bool {
	for _, f := range fs {
		if f.Severity == Error {
			return true
		}
	}
	return false
}
