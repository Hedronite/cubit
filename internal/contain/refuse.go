package contain

import (
	"errors"
	"fmt"
	"strings"
)

// Builtin is the built-in refuse list. docs/refuse-list.md holds the same list
// in a fenced block, and a test keeps the two equal. Entries only ever get added.
var Builtin = []string{
	"~/.ssh",
	"~/.gnupg",
	"~/.aws",
	"~/.kube",
	"~/.config/gh",
	"~/.netrc",
	"~/.cargo/credentials*",
	"~/Library/Keychains",
	"~/Library/CloudStorage",
	"~/Library/Mobile Documents",
	"/etc/shadow",
	"/etc/ssh",
	"/proc",
	"/sys",
	"/dev",
	"~/.cargo/config",
	"~/.cargo/config.toml",
	"~/.docker",
	"~/.password-store",
	"~/.git-credentials",
	"~/.config/gcloud",
	"~/.config/op",
	"~/.local/share/keyrings",
	"~/Library/Safari",
	"~/Library/Application Support/Google/Chrome",
	"~/Library/Application Support/Firefox",
	"~/.mozilla",
	"~/.config/google-chrome",
	"~/.config/chromium",
	"/etc/NetworkManager/system-connections",
	"/run/secrets",
	"**/.ssh",
	"**/.gnupg",
	"**/.aws",
	"**/.kube",
	"**/.netrc",
	"**/.docker",
	"**/.password-store",
	"**/.config/gh",
	"**/.cargo/credentials*",
	"**/Library/Keychains",
}

// entry is one parsed refuse entry. comps are lowercased.
type entry struct {
	comps    []string
	anyDepth bool   // a "**/" entry: match at any depth
	glob     bool   // the last component is a prefix glob ("credentials*")
	class    string // "builtin" or "config", for messages that must not name a path
}

// Refuser matches paths against the built-in list plus configured additions.
// The zero value refuses nothing; build one with NewRefuser.
type Refuser struct {
	byLast map[string][]*entry // exact last component, lowercased
	globs  []*entry
	all    []*entry
}

// NewRefuser builds a Refuser. home is the invoking user's home directory
// (absolute); extra and vault are the config's additions, each absolute or "~/…".
func NewRefuser(home string, extra []string, vault string) (*Refuser, error) {
	r := &Refuser{byLast: map[string][]*entry{}}
	add := func(raw, class string) error {
		e, err := parseEntry(raw, home, class)
		if err != nil {
			return err
		}
		r.all = append(r.all, e)
		if e.glob {
			r.globs = append(r.globs, e)
			return nil
		}
		last := e.comps[len(e.comps)-1]
		r.byLast[last] = append(r.byLast[last], e)
		return nil
	}
	for _, raw := range Builtin {
		if err := add(raw, "builtin"); err != nil {
			return nil, err
		}
	}
	if vault != "" {
		if err := add(vault, "config"); err != nil {
			return nil, err
		}
	}
	for _, raw := range extra {
		if err := add(raw, "config"); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func parseEntry(raw, home, class string) (*entry, error) {
	e := &entry{class: class}
	var rest string
	switch {
	case strings.HasPrefix(raw, "**/"):
		e.anyDepth = true
		rest = raw[3:]
	case strings.HasPrefix(raw, "~/"):
		if !strings.HasPrefix(home, "/") {
			return nil, errors.New("a home directory is needed for ~/ refuse entries")
		}
		rest = strings.TrimPrefix(home, "/") + "/" + raw[2:]
	case strings.HasPrefix(raw, "/"):
		rest = raw[1:]
	default:
		return nil, fmt.Errorf("refuse entry %q is neither absolute, ~/ nor **/", raw)
	}
	for _, c := range strings.Split(rest, "/") {
		if c == "" {
			continue
		}
		if c == "." || c == ".." {
			return nil, fmt.Errorf("refuse entry %q holds a . or .. component", raw)
		}
		e.comps = append(e.comps, strings.ToLower(c))
	}
	if len(e.comps) == 0 {
		return nil, fmt.Errorf("refuse entry %q is empty", raw)
	}
	last := e.comps[len(e.comps)-1]
	if strings.HasSuffix(last, "*") {
		e.glob = true
		e.comps[len(e.comps)-1] = strings.TrimSuffix(last, "*")
	}
	return e, nil
}

// Refused reports whether the path, given as its components from the filesystem
// root (no separators, no "." or ".."), ends at a refused entry. The caller
// checks every component as it goes, so "lies below" is covered by not
// descending into a refused directory. Use Inside to test a whole path.
func (r *Refuser) Refused(path []string) bool {
	_, ok := r.match(path)
	return ok
}

func (r *Refuser) match(path []string) (*entry, bool) {
	if r == nil || len(path) == 0 {
		return nil, false
	}
	last := strings.ToLower(path[len(path)-1])
	for _, e := range r.byLast[last] {
		if e.matches(path) {
			return e, true
		}
	}
	for _, e := range r.globs {
		if e.matches(path) {
			return e, true
		}
	}
	return nil, false
}

func (e *entry) matches(path []string) bool {
	n := len(e.comps)
	if len(path) < n || (!e.anyDepth && len(path) != n) {
		return false
	}
	tail := path[len(path)-n:]
	for i, want := range e.comps {
		got := strings.ToLower(tail[i])
		if i == n-1 && e.glob {
			return strings.HasPrefix(got, want)
		}
		if got != want {
			return false
		}
	}
	return true
}

// Inside reports whether the path, or any ancestor of it, is refused, and the
// class of the entry that matched ("builtin" or "config"). It never returns the
// path or the entry text, so a caller can print it.
func (r *Refuser) Inside(path []string) (class string, refused bool) {
	for i := 1; i <= len(path); i++ {
		if e, ok := r.match(path[:i]); ok {
			return e.class, true
		}
	}
	return "", false
}

// Components splits an absolute, cleaned path into components.
func Components(abs string) []string {
	var out []string
	for _, c := range strings.Split(abs, "/") {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// Under reports the classes ("builtin" or "config") of the anchored entries
// that lie strictly below the directory root, given as components. A root may
// legally hold a refused prefix; the walk skips it and counts it. "**/" entries
// cannot be found without walking, so they are not reported here.
func (r *Refuser) Under(root []string) []string {
	if r == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range r.all {
		if e.anyDepth || len(e.comps) <= len(root) {
			continue
		}
		ok := true
		for i, c := range root {
			if strings.ToLower(c) != e.comps[i] {
				ok = false
				break
			}
		}
		if ok && !seen[e.class] {
			seen[e.class] = true
			out = append(out, e.class)
		}
	}
	return out
}
