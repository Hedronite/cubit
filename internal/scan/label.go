package scan

import (
	"sort"

	"github.com/Hedronite/cubit/internal/config"
	"github.com/Hedronite/cubit/internal/walk"
)

type item struct {
	rel, name string
	bytes     int64
	prof      [walk.NumProfiles]int64
}

// labelWorkspaces fills the workspace fields of a workspace-root or target-dir
// root from its measured units: derived and declared labels, collision
// suffixes, the largest-first max_workspaces cut, and the unlabeled totals.
func labelWorkspaces(out *Root, rs RootSpec, results []unitResult) {
	var items []item
	for _, r := range results {
		if r.absent {
			continue
		}
		items = append(items, item{rel: r.unit.rel, bytes: r.tally.Bytes, prof: r.tally.Profiles})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].rel < items[j].rel })

	if rs.Mode == config.TargetDir {
		for i := range items {
			items[i].name = rs.Name
		}
	} else {
		used := map[string]bool{}
		unique := func(label, rel string) string {
			if !used[label] {
				used[label] = true
				return label
			}
			s := label
			for n := 8; n <= 32; n += 4 {
				s = config.Suffixed(label, rel, n)
				if !used[s] {
					break
				}
			}
			used[s] = true
			return s
		}
		for i := range items {
			if slug, ok := rs.Names[items[i].rel]; ok {
				items[i].name = unique(slug, items[i].rel)
			}
		}
		for i := range items {
			if items[i].name == "" {
				items[i].name = unique(config.Label(items[i].rel), items[i].rel)
			}
		}
	}

	out.WorkspacesFound = len(items)
	out.ProfilesEmitted = rs.Profiles
	for i := range items {
		out.Bytes += items[i].bytes
	}

	keep := len(items)
	if rs.Mode == config.WorkspaceRoot && rs.MaxWorkspaces > 0 && keep > rs.MaxWorkspaces {
		keep = rs.MaxWorkspaces
	}
	order := append([]item(nil), items...)
	sort.Slice(order, func(i, j int) bool {
		if order[i].bytes != order[j].bytes {
			return order[i].bytes > order[j].bytes
		}
		return order[i].name < order[j].name
	})
	for _, it := range order[keep:] {
		out.WorkspacesUnlabeled++
		out.WorkspacesUnlabeledBytes += it.bytes
	}
	labelled := order[:keep]
	sort.Slice(labelled, func(i, j int) bool { return labelled[i].name < labelled[j].name })
	for _, it := range labelled {
		w := Workspace{Name: it.name, Bytes: it.bytes}
		if rs.Profiles {
			p := Profiles{
				Debug: it.prof[walk.Debug], Release: it.prof[walk.Release], Doc: it.prof[walk.Doc],
				Incremental: it.prof[walk.Incremental], Other: it.prof[walk.Other],
			}
			w.Profiles = &p
		}
		out.Workspaces = append(out.Workspaces, w)
	}
}
