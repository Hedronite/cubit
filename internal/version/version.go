// Package version holds the build identity that `cubit version` prints and
// cubit_build_info exports.
package version

import (
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"
)

// Version is set at release time with -ldflags "-X .../internal/version.Version=v1.2.3".
var Version = "dev"

// Info is the build identity.
type Info struct {
	Version   string `json:"version"`
	Revision  string `json:"revision"`
	GoVersion string `json:"go_version"`
}

// Get reads the identity. The revision comes from the VCS stamp Go embeds, and
// is "unknown" when there is none.
func Get() Info {
	rev := "unknown"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				rev = s.Value
				if len(rev) > 12 {
					rev = rev[:12]
				}
			}
		}
	}
	return Info{Version: Version, Revision: rev, GoVersion: runtime.Version()}
}

// Line is the one stable line `cubit version` prints.
func (i Info) Line() string {
	return fmt.Sprintf("cubit %s (%s, %s)", i.Version, i.Revision, i.GoVersion)
}

// JSON is the one-line JSON form.
func (i Info) JSON() string {
	b, _ := json.Marshal(i)
	return string(b)
}
