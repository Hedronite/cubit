// Package report renders a scan document and config findings for people. It
// reads the same values `cubit scan --json` prints; it adds no number of its
// own. Sizes are bytes, exactly as in the JSON.
package report

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/Hedronite/cubit/internal/config"
	"github.com/Hedronite/cubit/internal/scan"
)

type row struct{ root, mode, bytes, size, state string }

// Table writes the human rendering of a scan document: one block per root with
// its workspaces or caches biggest first (ties by name), then the notes. file is
// the config file name, for the fix hints.
func Table(w io.Writer, d *scan.Document, file string) {
	rows := []row{{"ROOT", "MODE", "BYTES", "SIZE", "STATE"}}
	for i := range d.Roots {
		r := &d.Roots[i]
		state := "ok"
		switch {
		case !r.Success:
			state = "failed"
		case r.Partial:
			state = "partial"
		}
		bytes, size := "-", "-"
		if r.Success {
			bytes, size = strconv.FormatInt(r.Bytes, 10), Human(r.Bytes)
		}
		rows = append(rows, row{r.Alias, r.Mode, bytes, size, state})
		for _, c := range children(r) {
			rows = append(rows, row{"  " + c.name, "", strconv.FormatInt(c.bytes, 10), Human(c.bytes), ""})
		}
	}
	var wr, wm, wb, ws int
	for _, x := range rows {
		wr, wm, wb, ws = max(wr, len(x.root)), max(wm, len(x.mode)), max(wb, len(x.bytes)), max(ws, len(x.size))
	}
	for _, x := range rows {
		line := pad(x.root, wr) + "  " + pad(x.mode, wm) + "  " + pad(x.bytes, wb) + "  " + padLeft(x.size, ws) + "  " + x.state
		fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
	notes(w, d, file)
}

type child struct {
	name  string
	bytes int64
}

func children(r *scan.Root) []child {
	var out []child
	for _, ws := range r.Workspaces {
		out = append(out, child{ws.Name, ws.Bytes})
	}
	for _, c := range r.Caches {
		out = append(out, child{c.Kind, c.Bytes})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].bytes != out[j].bytes {
			return out[i].bytes > out[j].bytes
		}
		return out[i].name < out[j].name
	})
	if r.WorkspacesUnlabeled > 0 {
		out = append(out, child{fmt.Sprintf("(%d unlabeled)", r.WorkspacesUnlabeled), r.WorkspacesUnlabeledBytes})
	}
	return out
}

func pad(s string, n int) string { return s + strings.Repeat(" ", n-len(s)) }

func padLeft(s string, n int) string { return strings.Repeat(" ", n-len(s)) + s }

// Human renders bytes the way du -h does: base 1024, one decimal under ten, so
// 42000000000 reads 39G. It is for reading only; the BYTES column and the JSON
// carry the exact number.
func Human(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	v, u := float64(n), -1
	for v >= 1024 && u < 5 {
		v /= 1024
		u++
	}
	unit := "KMGTPE"[u]
	if v < 9.95 {
		return fmt.Sprintf("%.1f%c", v, unit)
	}
	if r := int64(v + 0.5); r < 1024 || u == 5 {
		return fmt.Sprintf("%d%c", r, unit)
	}
	return fmt.Sprintf("1.0%c", "KMGTPE"[u+1])
}

func notes(w io.Writer, d *scan.Document, file string) {
	byRoot := map[string][]scan.Warning{}
	for _, x := range d.Warnings {
		byRoot[x.Root] = append(byRoot[x.Root], x)
	}
	for i := range d.Roots {
		r := &d.Roots[i]
		if !r.Success {
			fmt.Fprintf(w, "error:   %s failed: %s; fix: %s\n", r.Alias, r.Error, fixFor(r.Error, r.Alias, file))
			continue
		}
		if r.Partial {
			fmt.Fprintf(w, "warning: %s partial: %s; every size is a lower bound\n", r.Alias, strings.Join(r.Errors, ", "))
		}
		if len(r.Skipped) > 0 {
			parts := make([]string, 0, len(r.Skipped))
			for _, s := range r.Skipped {
				parts = append(parts, fmt.Sprintf("%d %s", s.Count, s.Reason))
			}
			fmt.Fprintf(w, "warning: %s skipped %s\n", r.Alias, strings.Join(parts, ", "))
		}
		for _, x := range byRoot[r.Alias] {
			switch x.Reason {
			case "unmatched_name":
				fmt.Fprintf(w, "warning: %s: %d unmatched_name in names\n", r.Alias, x.Count)
			case "no_workspaces":
				fmt.Fprintf(w, "warning: %s: no_workspaces; fix: check the path and mode of root %s in %s, or for a single project point a target-dir root at its target/\n", r.Alias, r.Alias, file)
			}
		}
	}
}

func fixFor(reason, alias, file string) string {
	switch reason {
	case "root_missing":
		return "check that the path of root " + alias + " in " + file + " exists"
	case "root_unreadable":
		return "check permissions on the path of root " + alias + " in " + file + ", and that it is a directory"
	}
	return "see docs/metrics.md"
}

// Findings writes config findings in the house style: file, line and the
// problem, the source line as an excerpt, and the fix.
func Findings(w io.Writer, file string, fs []config.Finding) {
	for _, f := range fs {
		loc := file
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", file, f.Line)
		}
		fmt.Fprintf(w, "%s: %s: %s\n", loc, f.Severity, f.Message)
		if f.Excerpt != "" {
			fmt.Fprintf(w, "  | %s\n", f.Excerpt)
		}
		if f.Fix != "" {
			fmt.Fprintf(w, "  fix: %s\n", f.Fix)
		}
	}
}

// Summary is the closing line of `validate`. roots is -1 when the file did not
// decode, so the count is not known.
func Summary(w io.Writer, file string, fs []config.Finding, roots int) {
	var errs, warns int
	for _, f := range fs {
		switch f.Severity {
		case config.Error:
			errs++
		case config.Warning:
			warns++
		}
	}
	var parts []string
	if errs > 0 {
		parts = append(parts, fmt.Sprintf("%d error(s)", errs))
	}
	if errs > 0 && warns > 0 {
		parts = append(parts, fmt.Sprintf("%d warning(s)", warns))
	}
	switch {
	case errs > 0:
		if roots >= 0 {
			parts = append(parts, fmt.Sprintf("%d root(s)", roots))
		}
		fmt.Fprintf(w, "%s: not ok: %s\n", file, strings.Join(parts, ", "))
	case warns > 0:
		fmt.Fprintf(w, "%s: ok, %d root(s), %d warning(s)\n", file, roots, warns)
	default:
		fmt.Fprintf(w, "%s: ok, %d root(s)\n", file, roots)
	}
}
