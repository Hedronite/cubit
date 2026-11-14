package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/Hedronite/cubit/schemas"
)

// MaxBytes bounds the config file. A real one is a few kilobytes.
const MaxBytes = 1 << 20

// Defaults, as docs/config.md states them.
const (
	DefaultInterval       = 5 * time.Minute
	MinInterval           = 30 * time.Second
	DefaultMaxFiles       = 2_000_000
	DefaultMaxDuration    = 30 * time.Second
	DefaultDiscoveryDepth = 3
	DefaultMaxWorkspaces  = 64
	DepthGuard            = 64
)

// Modes.
const (
	WorkspaceRoot = "workspace-root"
	TargetDir     = "target-dir"
	CargoHome     = "cargo-home"
	Sccache       = "sccache"
)

// Severity of a Finding.
const (
	Error   = "error"
	Warning = "warning"
	Info    = "info"
)

// Duration is a config duration. UnmarshalYAML checks the grammar, which is
// stricter than time.ParseDuration: integer parts with ms, s, m or h only.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// ParseDuration parses `30s`, `5m`, `1h30m`.
func ParseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, errors.New("empty duration")
	}
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i {
			return 0, fmt.Errorf("duration %q: expected digits", s)
		}
		k := j
		for k < len(s) && (s[k] < '0' || s[k] > '9') {
			k++
		}
		switch s[j:k] {
		case "ms", "s", "m", "h":
		default:
			return 0, fmt.Errorf("duration %q: unit must be ms, s, m or h", s)
		}
		i = k
	}
	return time.ParseDuration(s)
}

// Config is the decoded file with defaults not yet applied; see Resolved fields
// on the methods below.
type Config struct {
	Schema string    `yaml:"schema"`
	Host   string    `yaml:"host"`
	Scan   ScanCfg   `yaml:"scan"`
	Refuse RefuseCfg `yaml:"refuse"`
	Roots  []Root    `yaml:"roots"`

	lines  map[string]int // "roots/2/alias" -> 1-based line
	source []string
}

// ScanCfg is the `scan:` block.
type ScanCfg struct {
	Interval   Duration `yaml:"interval"`
	MaxWorkers int      `yaml:"max_workers"`
}

// RefuseCfg is the `refuse:` block.
type RefuseCfg struct {
	Vault string   `yaml:"vault"`
	Extra []string `yaml:"extra"`
}

// Root is one entry of `roots:`. A zero numeric field means "not set"; use the
// accessor methods for the effective value.
type Root struct {
	Alias          string            `yaml:"alias"`
	Path           string            `yaml:"path"`
	Mode           string            `yaml:"mode"`
	MaxFiles       int64             `yaml:"max_files"`
	MaxDuration    Duration          `yaml:"max_duration"`
	DiscoveryDepth int               `yaml:"discovery_depth"`
	MaxWorkspaces  int               `yaml:"max_workspaces"`
	Names          map[string]string `yaml:"names"`
	Name           string            `yaml:"name"`
	Profiles       *bool             `yaml:"profiles"`
}

// EffectiveInterval is scan.interval or its default.
func (c *Config) EffectiveInterval() time.Duration {
	if c.Scan.Interval == 0 {
		return DefaultInterval
	}
	return time.Duration(c.Scan.Interval)
}

// EffectiveWorkers is scan.max_workers or min(4, GOMAXPROCS).
func (c *Config) EffectiveWorkers() int {
	if c.Scan.MaxWorkers > 0 {
		return c.Scan.MaxWorkers
	}
	return min(4, runtime.GOMAXPROCS(0))
}

// EffectiveMaxFiles is the root's max_files or the default.
func (r *Root) EffectiveMaxFiles() int64 {
	if r.MaxFiles == 0 {
		return DefaultMaxFiles
	}
	return r.MaxFiles
}

// EffectiveMaxDuration is the root's max_duration or the default.
func (r *Root) EffectiveMaxDuration() time.Duration {
	if r.MaxDuration == 0 {
		return DefaultMaxDuration
	}
	return time.Duration(r.MaxDuration)
}

// EffectiveDiscoveryDepth is discovery_depth or the default.
func (r *Root) EffectiveDiscoveryDepth() int {
	if r.DiscoveryDepth == 0 {
		return DefaultDiscoveryDepth
	}
	return r.DiscoveryDepth
}

// EffectiveMaxWorkspaces is max_workspaces or the default.
func (r *Root) EffectiveMaxWorkspaces() int {
	if r.MaxWorkspaces == 0 {
		return DefaultMaxWorkspaces
	}
	return r.MaxWorkspaces
}

// EmitProfiles is whether the per-profile breakdown is on (the default).
func (r *Root) EmitProfiles() bool {
	return r.Mode != CargoHome && r.Mode != Sccache && (r.Profiles == nil || *r.Profiles)
}

// WorkspaceName is the one workspace's name for a target-dir root.
func (r *Root) WorkspaceName() string {
	if r.Name != "" {
		return r.Name
	}
	return r.Alias
}

// Line is the 1-based line of a path such as roots[2].alias, or 0.
func (c *Config) Line(path string) int {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.lines[path]
}

func (c *Config) excerpt(line int) string { return excerptLine(c.source, line) }

// excerptLine is the trimmed source line, cut to about 72 characters.
func excerptLine(source []string, line int) string {
	if line < 1 || line > len(source) {
		return ""
	}
	s := strings.TrimSpace(source[line-1])
	if r := []rune(s); len(r) > 72 {
		s = string(r[:72]) + "…"
	}
	return s
}

var (
	schemaOnce sync.Once
	schemaVal  *jsonschema.Schema
	schemaErr  error
)

func compiled() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		var meta struct {
			ID string `json:"$id"`
		}
		if schemaErr = json.Unmarshal(schemas.Config, &meta); schemaErr != nil {
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemas.Config))
		if err != nil {
			schemaErr = err
			return
		}
		c := jsonschema.NewCompiler()
		if schemaErr = c.AddResource(meta.ID, doc); schemaErr != nil {
			return
		}
		schemaVal, schemaErr = c.Compile(meta.ID)
	})
	return schemaVal, schemaErr
}

// Parse decodes and checks one config document against the schema. It touches
// no filesystem. The returned Config is nil when any finding is an error.
func Parse(data []byte) (*Config, []Finding) {
	if len(data) > MaxBytes {
		return nil, []Finding{{Code: CodeFileShape, Severity: Error, Message: fmt.Sprintf("this file is larger than %d bytes", MaxBytes), Fix: "a Cubit config is a few kilobytes; check this is the right file."}}
	}
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, []Finding{{Code: CodeFileShape, Severity: Error, Message: "this file is empty", Fix: "start from examples/cubit.yaml."}}
		}
		src := strings.Split(string(data), "\n")
		f := yamlFinding(err)
		f.Excerpt = excerptLine(src, f.Line)
		if f.Line > 0 && f.Line <= len(src) && strings.Contains(src[f.Line-1], "\t") {
			f.Fix = "indent with spaces; YAML forbids tabs."
		}
		return nil, []Finding{f}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, []Finding{{Code: CodeFileShape, Severity: Error, Message: "this file holds more than one YAML document", Fix: "keep one document and remove the --- separators."}}
	}
	c := &Config{lines: map[string]int{}, source: strings.Split(string(data), "\n")}
	var fs []Finding
	if f := noAliases(&doc); f != nil {
		return nil, []Finding{*f}
	}
	walkLines(&doc, "", c.lines)

	generic, err := toJSON(&doc)
	if err != nil {
		return nil, []Finding{{Code: CodeFileShape, Severity: Error, Message: err.Error(), Fix: "use plain string keys and scalar, list or map values."}}
	}
	s, err := compiled()
	if err != nil {
		return nil, []Finding{{Code: CodeInvalidValue, Severity: Error, Message: "embedded schema failed to compile: " + err.Error()}}
	}
	if verr := s.Validate(generic); verr != nil {
		var ve *jsonschema.ValidationError
		if errors.As(verr, &ve) {
			fs = append(fs, schemaFindings(c, ve, generic)...)
		} else {
			fs = append(fs, Finding{Code: CodeInvalidValue, Severity: Error, Message: verr.Error()})
		}
	}
	if len(fs) > 0 {
		sortFindings(fs)
		return nil, fs
	}
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	var out Config
	if err := strict.Decode(&out); err != nil {
		f := yamlFinding(err)
		f.Excerpt = c.excerpt(f.Line)
		return nil, []Finding{f}
	}
	out.lines, out.source = c.lines, c.source
	return &out, nil
}

func yamlFinding(err error) Finding {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	f := Finding{Code: CodeYAMLSyntax, Severity: Error}
	// yaml errors read "line 7: ...".
	if rest, ok := strings.CutPrefix(msg, "line "); ok {
		if n, tail, ok := strings.Cut(rest, ":"); ok {
			if line, err := strconv.Atoi(n); err == nil {
				f.Line = line
				msg = strings.TrimSpace(tail)
			}
		}
	}
	f.Message = "this file is not valid YAML: " + msg
	f.Fix = "put each list item on its own line, indented under its key, and check the quoting."
	if strings.Contains(msg, "tab") {
		f.Fix = "indent with spaces; YAML forbids tabs."
	}
	return f
}

func noAliases(n *yaml.Node) *Finding {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return &Finding{Code: CodeFileShape, Severity: Error, Line: n.Line, Message: "YAML anchors and aliases are not allowed", Fix: "write the value out in full."}
	}
	for _, c := range n.Content {
		if f := noAliases(c); f != nil {
			return f
		}
	}
	return nil
}

// walkLines records the line of every mapping value and sequence item under a
// JSON-pointer-like key (roots/2/alias).
func walkLines(n *yaml.Node, p string, out map[string]int) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walkLines(c, p, out)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			kp := p + "/" + strings.ReplaceAll(strings.ReplaceAll(k.Value, "~", "~0"), "/", "~1")
			out[kp] = k.Line
			walkLines(v, kp, out)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			kp := p + "/" + strconv.Itoa(i)
			out[kp] = c.Line
			walkLines(c, kp, out)
		}
	}
}

// toJSON turns a YAML node into the JSON data model, refusing non-string keys.
func toJSON(n *yaml.Node) (any, error) {
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, err
	}
	if err := checkKeys(v); err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(b))
}

func checkKeys(v any) error {
	switch t := v.(type) {
	case map[string]any:
		for _, x := range t {
			if err := checkKeys(x); err != nil {
				return err
			}
		}
	case map[any]any:
		return errors.New("a mapping key is not a string")
	case []any:
		for _, x := range t {
			if err := checkKeys(x); err != nil {
				return err
			}
		}
	}
	return nil
}

// lineFor finds the line of a pointer, falling back to its nearest ancestor.
func (c *Config) lineFor(ptr string) int {
	for ptr != "" {
		if l, ok := c.lines[ptr]; ok {
			return l
		}
		i := strings.LastIndexByte(ptr, '/')
		if i < 0 {
			break
		}
		ptr = ptr[:i]
	}
	return 0
}

func pointerPath(ptr string) string {
	if ptr == "" {
		return ""
	}
	var b strings.Builder
	for _, part := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		if _, err := strconv.Atoi(part); err == nil {
			b.WriteString("[" + part + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(part)
	}
	return b.String()
}
