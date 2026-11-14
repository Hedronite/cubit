package config

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Finding is one problem, tied to a place in the file. Code is stable: scripts
// match on it, and a test pins the set. A finding never holds a filesystem path
// of a refused location, only its class.
type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	File     string `json:"file,omitempty"`  // set by the caller that knows the file name
	Line     int    `json:"line,omitempty"`  // 1-based, 0 when unknown
	Field    string `json:"field,omitempty"` // roots[2].alias
	Message  string `json:"message"`
	Excerpt  string `json:"excerpt,omitempty"` // the source line, trimmed
	Fix      string `json:"fix,omitempty"`
}

// The stable finding codes. Codes lists them all, and docs/config.md describes
// each; a test holds the two together.
const (
	CodeSlugPattern     = "slug_pattern"
	CodePathRelative    = "path_relative"
	CodeBadDuration     = "bad_duration"
	CodeBadEnum         = "bad_enum"
	CodeKeyForMode      = "key_for_mode"
	CodeMissingKey      = "missing_key"
	CodeOutOfRange      = "out_of_range"
	CodeUnknownKey      = "unknown_key"
	CodeYAMLSyntax      = "yaml_syntax"
	CodeFileShape       = "file_shape"
	CodeBadSchema       = "bad_schema"
	CodeNamesKey        = "names_key"
	CodeWrongType       = "wrong_type"
	CodeInvalidValue    = "invalid_value"
	CodeDuplicateAlias  = "duplicate_alias"
	CodeOverlappingRoot = "overlapping_roots"
	CodeRefusedRoot     = "refused_root"
	CodeRefusedInside   = "refused_inside"
	CodeSameRealPath    = "same_real_path"
	CodeIntervalFloor   = "interval_floor"
	CodeDurationGE      = "duration_ge_interval"
	CodeDuplicateSlug   = "duplicate_slug"
	CodePathInvalid     = "path_invalid"
	CodeNotDirectory    = "not_a_directory"
	CodeRootMissing     = "root_missing"
	CodeRootUnreadable  = "root_unreadable"
	CodeLooksLikeTarget = "looks_like_target"
	CodeModeEcho        = "mode_echo"
	CodeHomeUnknown     = "home_unknown"
	CodeRootIsWorkspace = "root_is_workspace"
)

// Codes is every code a finding can carry.
var Codes = []string{
	CodeSlugPattern, CodePathRelative, CodeBadDuration, CodeBadEnum, CodeKeyForMode, CodeMissingKey,
	CodeOutOfRange, CodeUnknownKey, CodeYAMLSyntax, CodeFileShape, CodeBadSchema, CodeNamesKey,
	CodeWrongType, CodeInvalidValue, CodeDuplicateAlias, CodeOverlappingRoot, CodeRefusedRoot,
	CodeRefusedInside, CodeSameRealPath, CodeIntervalFloor, CodeDurationGE, CodeDuplicateSlug,
	CodePathInvalid, CodeNotDirectory, CodeRootMissing, CodeRootUnreadable, CodeLooksLikeTarget,
	CodeModeEcho, CodeHomeUnknown, CodeRootIsWorkspace,
}

// Keys by place. A key valid in one root mode and not another is key_for_mode.
var (
	topKeys    = []string{"schema", "host", "scan", "refuse", "roots"}
	scanKeys   = []string{"interval", "max_workers"}
	refuseKeys = []string{"vault", "extra"}
	rootKeys   = []string{"alias", "path", "mode", "max_files", "max_duration"}
	modeKeys   = map[string][]string{
		WorkspaceRoot: {"discovery_depth", "max_workspaces", "names", "profiles"},
		TargetDir:     {"name", "profiles"},
		CargoHome:     nil,
		Sccache:       nil,
	}
	modes = []string{WorkspaceRoot, TargetDir, CargoHome, Sccache}
)

// limit is a numeric bound the schema states, with the default for the fix line.
type limit struct {
	min, max, def int64
}

var limits = map[string]limit{
	"discovery_depth": {1, 6, 3},
	"max_workspaces":  {1, 256, 64},
	"max_files":       {1000, 100_000_000, 2_000_000},
	"max_workers":     {1, 16, 4},
}

func sortFindings(fs []Finding) {
	rank := map[string]int{Error: 0, Warning: 1, Info: 2}
	sort.SliceStable(fs, func(i, j int) bool {
		if rank[fs[i].Severity] != rank[fs[j].Severity] {
			return rank[fs[i].Severity] < rank[fs[j].Severity]
		}
		if fs[i].Line != fs[j].Line {
			return fs[i].Line < fs[j].Line
		}
		return fs[i].Field < fs[j].Field
	})
}

var quoted = regexp.MustCompile(`'([^']*)'`)

// schemaFindings turns the validator's leaf errors into findings with plain
// words, a code, and a fix that says what to do. doc is the decoded document,
// for the values and modes the messages quote.
func schemaFindings(c *Config, ve *jsonschema.ValidationError, doc any) []Finding {
	var out []Finding
	seen := map[string]bool{}
	sawNames := false
	var walk func(u *jsonschema.OutputUnit)
	walk = func(u *jsonschema.OutputUnit) {
		if strings.Contains(u.KeywordLocation, "propertyNames") {
			// The validator loses the key and its place for these, so they are
			// found from the document below.
			sawNames = true
			return
		}
		if u.Error != nil {
			for _, f := range classify(c, u, doc) {
				key := f.Code + "|" + f.Field + "|" + f.Message
				if !seen[key] {
					seen[key] = true
					out = append(out, f)
				}
			}
		}
		for i := range u.Errors {
			walk(&u.Errors[i])
		}
	}
	walk(ve.DetailedOutput())
	if sawNames {
		out = append(out, namesFindings(c, doc)...)
	}
	return out
}

var namesKeyOK = regexp.MustCompile(`^[^/\\\x00]+(/[^/\\\x00]+)*$`)

// namesFindings checks every names key the way the schema's propertyNames rule does.
func namesFindings(c *Config, doc any) []Finding {
	var out []Finding
	roots, _ := valueAt(doc, "/roots").([]any)
	for i := range roots {
		names, _ := valueAt(doc, fmt.Sprintf("/roots/%d/names", i)).(map[string]any)
		keys := make([]string, 0, len(names))
		for k := range names {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ok := len(k) <= 512 && namesKeyOK.MatchString(k)
			for _, part := range strings.Split(k, "/") {
				if part == "." || part == ".." {
					ok = false
				}
			}
			if ok {
				continue
			}
			field := fmt.Sprintf("roots[%d].names", i)
			line := c.lineFor(fmt.Sprintf("/roots/%d/names/%s", i, escapePtr(k)))
			out = append(out, Finding{
				Code: CodeNamesKey, Severity: Error, Line: line, Field: field, Excerpt: c.excerpt(line),
				Message: fmt.Sprintf("%s: the key '%s' is not a usable relative path", field, k),
				Fix:     "write the workspace's path under the root as on disk, with no '.' or '..' parts, no leading or trailing '/', no backslash.",
			})
		}
	}
	return out
}

func classify(c *Config, u *jsonschema.OutputUnit, doc any) []Finding {
	ptr := u.InstanceLocation
	msg := u.Error.String()
	kw := u.KeywordLocation
	if i := strings.LastIndexByte(kw, '/'); i >= 0 {
		kw = kw[i+1:]
	}
	field := pointerPath(ptr)
	name := lastName(ptr)
	line := c.lineFor(ptr)
	mk := func(code, fld, message, fix string) Finding {
		l := line
		if fld != field {
			if l2 := c.lineFor(ptr + "/" + escapePtr(strings.TrimPrefix(fld, field+"."))); l2 != 0 {
				l = l2
			}
		}
		return Finding{Code: code, Severity: Error, Line: l, Field: fld, Message: message, Excerpt: c.excerpt(l), Fix: fix}
	}
	val := valueAt(doc, ptr)

	switch {
	case msg == "false schema" || strings.HasPrefix(msg, "additional properties") || strings.HasPrefix(msg, "unevaluated properties"):
		container, keys := ptr, []string(nil)
		if msg == "false schema" {
			container = parentPtr(ptr)
			keys = []string{name}
		} else {
			for _, m := range quoted.FindAllStringSubmatch(msg, -1) {
				keys = append(keys, m[1])
			}
		}
		var out []Finding
		for _, k := range keys {
			if f, ok := unknownKey(c, doc, container, k, mk); ok {
				out = append(out, f)
			}
		}
		return out
	case kw == "required":
		var out []Finding
		for _, m := range quoted.FindAllStringSubmatch(msg, -1) {
			k := m[1]
			fld := joinField(field, k)
			out = append(out, mk(CodeMissingKey, fld, fld+" is required", missingFix(k)))
		}
		return out
	case kw == "enum":
		bad := fmt.Sprint(val)
		fix := "use one of " + strings.Join(modes, ", ") + "."
		if best, ok := closest(bad, modes, 3); ok {
			fix = fmt.Sprintf("use one of %s; '%s' looks closest.", strings.Join(modes, ", "), best)
		}
		return []Finding{mk(CodeBadEnum, field, fmt.Sprintf("%s '%s' is not a mode", field, bad), fix)}
	case kw == "const":
		return []Finding{mk(CodeBadSchema, field, fmt.Sprintf("%s must be 'cubit.config/v0'", field), "write schema: cubit.config/v0 as the first key.")}
	case kw == "type":
		return []Finding{mk(CodeWrongType, field, fmt.Sprintf("%s has the wrong type (%s)", field, msg), "check the value's type in docs/config.md.")}
	case kw == "pattern" || kw == "not":
		return []Finding{patternFinding(mk, field, name, ptr, fmt.Sprint(val), false)}
	case kw == "minimum" || kw == "maximum" || kw == "maxItems" || kw == "minItems" || kw == "maxProperties" || kw == "minLength" || kw == "maxLength":
		return []Finding{rangeFinding(mk, field, name, kw, val)}
	}
	return []Finding{mk(CodeInvalidValue, field, fmt.Sprintf("%s: %s", field, msg), "check this field in docs/config.md.")}
}

func patternFinding(mk func(code, fld, message, fix string) Finding, field, name, ptr, val string, names bool) Finding {
	switch {
	case names:
		return mk(CodeNamesKey, field, fmt.Sprintf("%s: the key '%s' is not a usable relative path", field, val), "write the workspace's path under the root as on disk, with no '.' or '..' parts and no leading or trailing '/'.")
	case name == "interval" || name == "max_duration":
		return mk(CodeBadDuration, field, fmt.Sprintf("%s '%s' is not a duration", field, val), "write a duration like 30s, 5m, 1h30m: whole numbers, no spaces or words.")
	case name == "path" || name == "vault" || strings.Contains(ptr, "/extra/"):
		return mk(CodePathRelative, field, fmt.Sprintf("%s '%s': paths must be absolute or start with '~/'; Cubit resolves them from the config, not from the working directory", field, val), "use an absolute path or '~/...'.")
	}
	fix := "use lowercase letters, digits and '-'."
	if s := suggestSlug(val); s != "" {
		fix = fmt.Sprintf("rename it to '%s'.", s)
	}
	return mk(CodeSlugPattern, field, fmt.Sprintf("%s '%s': use lowercase letters, digits and '-', start with a letter or digit, at most 32 characters", field, val), fix)
}

func rangeFinding(mk func(code, fld, message, fix string) Finding, field, name, kw string, val any) Finding {
	if l, ok := limits[name]; ok {
		n, _ := toInt(val)
		if n > l.max {
			return mk(CodeOutOfRange, field, fmt.Sprintf("%s %d is above the cap of %d", field, n, l.max), fmt.Sprintf("use %d or less (the default is %d).", l.max, l.def))
		}
		return mk(CodeOutOfRange, field, fmt.Sprintf("%s %d is below the minimum of %d", field, n, l.min), fmt.Sprintf("use %d or more (the default is %d).", l.min, l.def))
	}
	switch kw {
	case "maxItems", "maxProperties":
		return mk(CodeOutOfRange, field, field+" has too many entries", "remove entries: roots allow 32, refuse.extra 64, names 256.")
	case "minItems":
		return mk(CodeOutOfRange, field, field+" needs at least one entry", "add a root.")
	}
	return mk(CodeOutOfRange, field, field+" is out of range", "check the limits in docs/config.md.")
}

// unknownKey explains a key the schema did not accept. A key that is valid here
// but has a bad value is reported by the rule that rejected the value, and
// yields ok=false so it is not reported twice.
func unknownKey(c *Config, doc any, container, key string, mk func(code, fld, message, fix string) Finding) (Finding, bool) {
	fld := joinField(pointerPath(container), key)
	known, where := keysAt(doc, container)
	// A key that is real, but for another mode.
	if strings.HasPrefix(container, "/roots/") && strings.Count(container, "/") == 2 {
		mode := fmt.Sprint(valueAt(doc, container+"/mode"))
		var ok []string
		for m, ks := range modeKeys {
			for _, k := range ks {
				if k == key {
					ok = append(ok, m)
				}
			}
		}
		sort.Strings(ok)
		for _, m := range ok {
			if m == mode {
				return Finding{}, false
			}
		}
		for _, k := range rootKeys {
			if k == key {
				return Finding{}, false
			}
		}
		if len(ok) > 0 {
			return mk(CodeKeyForMode, fld, fmt.Sprintf("%s only applies when mode is %s (this root is %s)", fld, strings.Join(ok, " or "), mode),
				fmt.Sprintf("remove %s, or change the root to %s.", key, ok[0])), true
		}
	}
	fix := "remove it."
	if best, ok := closest(key, known, 2); ok {
		fix = fmt.Sprintf("remove it; did you mean '%s'?", best)
	} else if loc := elsewhere(key); loc != "" {
		fix = fmt.Sprintf("remove it; '%s' lives under %s.", key, loc)
	}
	for _, k := range known {
		if k == key {
			return Finding{}, false
		}
	}
	return mk(CodeUnknownKey, fld, fmt.Sprintf("%s is not a key here%s", fld, where), fix), true
}

func elsewhere(key string) string {
	for _, k := range scanKeys {
		if k == key {
			return "scan:"
		}
	}
	for _, k := range refuseKeys {
		if k == key {
			return "refuse:"
		}
	}
	for _, k := range topKeys {
		if k == key {
			return "the top level"
		}
	}
	for _, k := range rootKeys {
		if k == key {
			return "each root"
		}
	}
	return ""
}

// keysAt lists the valid keys at a container, for the closest-match hint.
func keysAt(doc any, container string) ([]string, string) {
	switch {
	case container == "":
		return topKeys, ""
	case container == "/scan":
		return scanKeys, ""
	case container == "/refuse":
		return refuseKeys, ""
	case strings.HasPrefix(container, "/roots/") && strings.Count(container, "/") == 2:
		mode := fmt.Sprint(valueAt(doc, container+"/mode"))
		return append(append([]string(nil), rootKeys...), modeKeys[mode]...), ""
	}
	return nil, ""
}

func missingFix(k string) string {
	switch k {
	case "mode":
		return "add 'mode:' as one of " + strings.Join(modes, ", ") + "."
	case "alias":
		return "add 'alias:', a lowercase slug such as 'example-projects'."
	case "path":
		return "add 'path:', absolute or '~/...'."
	case "roots":
		return "add a 'roots:' list with at least one root."
	case "schema":
		return "add 'schema: cubit.config/v0' as the first key."
	}
	return "add the key (see examples/cubit.yaml)."
}

// suggestSlug lowercases and replaces anything outside the alias grammar with '-'.
func suggestSlug(s string) string {
	var b []byte
	for i := 0; i < len(s) && len(b) < 32; i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			c += 'a' - 'A'
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			c = '-'
		}
		b = append(b, c)
	}
	out := strings.Trim(string(b), "-")
	if out == "" {
		return ""
	}
	return out
}

// closest returns the candidate nearest to s by edit distance, if one is within max
// and clearly nearest.
func closest(s string, cands []string, max int) (string, bool) {
	best, bestD, ties := "", max+1, 0
	for _, c := range cands {
		d := editDistance(strings.ToLower(s), c)
		switch {
		case d < bestD:
			best, bestD, ties = c, d, 1
		case d == bestD:
			ties++
		}
	}
	return best, bestD <= max && ties == 1
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// JSON-pointer helpers over the decoded document.

func parentPtr(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

func lastName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		p = p[i+1:]
	}
	p = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
	return p
}

func escapePtr(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func joinField(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func valueAt(doc any, ptr string) any {
	cur := doc
	if ptr == "" {
		return cur
	}
	for _, part := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch t := cur.(type) {
		case map[string]any:
			cur = t[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(t) {
				return nil
			}
			cur = t[i]
		default:
			return nil
		}
	}
	return cur
}

func toInt(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case int64:
		return t, true
	case int:
		return int64(t), true
	}
	if n, ok := v.(interface{ Int64() (int64, error) }); ok {
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}
