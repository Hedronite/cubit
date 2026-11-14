package scan

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"time"
)

// APIVersion is the value of the document's apiVersion field.
const APIVersion = "cubit.scan/v0"

// Document is cubit.scan/v0. Field order is the order the JSON is written in.
type Document struct {
	APIVersion  string    `json:"apiVersion"`
	GeneratedAt string    `json:"generated_at"`
	Host        string    `json:"host,omitempty"`
	Roots       []Root    `json:"roots"`
	Caps        DocCaps   `json:"caps"`
	Warnings    []Warning `json:"warnings"`
}

// DocCaps are the process-wide caps.
type DocCaps struct {
	MaxWorkers int `json:"max_workers"`
	DepthGuard int `json:"depth_guard"`
}

// RootCaps are one root's own caps.
type RootCaps struct {
	MaxFiles           int64   `json:"max_files"`
	MaxDurationSeconds float64 `json:"max_duration_seconds"`
}

// Root is one scanned root.
type Root struct {
	Alias                    string      `json:"alias"`
	Mode                     string      `json:"mode"`
	Bytes                    int64       `json:"bytes"`
	Success                  bool        `json:"success"`
	Partial                  bool        `json:"partial"`
	Error                    string      `json:"error,omitempty"`
	LastSuccessAt            string      `json:"last_success_at,omitempty"`
	DurationSeconds          float64     `json:"duration_seconds"`
	Caps                     RootCaps    `json:"caps"`
	DiscoveryDepth           *int        `json:"discovery_depth,omitempty"`
	ProfilesEmitted          bool        `json:"profiles_emitted"`
	Workspaces               []Workspace `json:"workspaces"`
	WorkspacesFound          int         `json:"workspaces_found"`
	WorkspacesUnlabeled      int         `json:"workspaces_unlabeled"`
	WorkspacesUnlabeledBytes int64       `json:"workspaces_unlabeled_bytes"`
	Caches                   []Cache     `json:"caches"`
	Skipped                  []Skip      `json:"skipped"`

	// Errors is every error reason that applied, in the schema's order. The
	// document shows only the first (Error); the metrics counter increments for
	// all of them. It is not part of the JSON.
	Errors []string `json:"-"`
}

// Workspace is one labelled workspace.
type Workspace struct {
	Name     string    `json:"name"`
	Bytes    int64     `json:"bytes"`
	Profiles *Profiles `json:"profiles,omitempty"`
}

// Profiles is the per-profile split. All five keys always appear.
type Profiles struct {
	Debug       int64 `json:"debug"`
	Release     int64 `json:"release"`
	Doc         int64 `json:"doc"`
	Incremental int64 `json:"incremental"`
	Other       int64 `json:"other"`
}

// Cache is one fixed cache area.
type Cache struct {
	Kind  string `json:"kind"`
	Bytes int64  `json:"bytes"`
}

// Skip is a count of entries or subtrees skipped for one reason.
type Skip struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// Warning names a root, a closed reason and a count. Never a path.
type Warning struct {
	Root   string `json:"root"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// JSON renders the document: two-space indent, trailing newline. Output is
// sorted and deterministic.
func (d *Document) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Decode reads a document strictly: an unknown field is an error.
func Decode(r io.Reader) (*Document, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var d Document
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, io.ErrUnexpectedEOF
	}
	return &d, nil
}

// Normalize re-renders a document through JSON, which is the round trip the
// fuzz target pins as byte-stable.
func Normalize(b []byte) ([]byte, error) {
	d, err := Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return d.JSON()
}

// ExitCode is the public contract of docs/cli.md: 0 every root is usable and
// complete, 2 no root is usable, 1 anything between.
func (d *Document) ExitCode() int {
	usable, clean := false, true
	for i := range d.Roots {
		r := &d.Roots[i]
		if r.Success {
			usable = true
		}
		if !r.Success || r.Partial {
			clean = false
		}
	}
	switch {
	case clean && usable:
		return 0
	case !usable:
		return 2
	default:
		return 1
	}
}

func round3(s float64) float64 { return math.Round(s*1000) / 1000 }

func rfc3339(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }
