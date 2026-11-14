package walk

// Profile is the build-profile bucket a path's bytes land in.
type Profile int

// The five profiles, in the order cubit.scan/v0 lists them.
const (
	Debug Profile = iota
	Release
	Doc
	Incremental
	Other
	NumProfiles
)

var profileNames = [NumProfiles]string{"debug", "release", "doc", "incremental", "other"}

func (p Profile) String() string {
	if p < 0 || p >= NumProfiles {
		return "other"
	}
	return profileNames[p]
}

// ClassifyProfile is the rule in docs/metrics.md, stated over a whole path.
// rel is the entry's path below the target dir, one component per element; the
// target dir itself is the empty path. A component equal to "incremental" puts
// the bytes under incremental, wherever it sits. Otherwise the first component
// names the profile if it is debug, release or doc. Everything else, including
// cross-compile <triple>/ directories, tmp and package, is other.
func ClassifyProfile(rel []string) Profile {
	for _, c := range rel {
		if c == "incremental" {
			return Incremental
		}
	}
	if len(rel) == 0 {
		return Other
	}
	return topProfile(rel[0])
}

func topProfile(name string) Profile {
	switch name {
	case "debug":
		return Debug
	case "release":
		return Release
	case "doc":
		return Doc
	}
	return Other
}

// childProfile is ClassifyProfile folded down a walk: parent is the profile of
// the directory being read, depth its number of components below the target
// dir (0 for the target dir), name the entry.
func childProfile(parent Profile, depth int, name string) Profile {
	if name == "incremental" || parent == Incremental {
		return Incremental
	}
	if depth == 0 {
		return topProfile(name)
	}
	return parent
}
