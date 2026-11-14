//go:build unix

package walk

import (
	"io/fs"
	"syscall"
)

// StatOf takes the size and identity out of a FileInfo from the OS. It is the
// one place the walk reads syscall.Stat_t; tests replace it to simulate a
// device boundary.
func StatOf(fi fs.FileInfo) (Stat, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Blocks < 0 {
		return Stat{}, false
	}
	return Stat{
		Blocks: uint64(st.Blocks),
		Dev:    uint64(st.Dev), //nolint:gosec // identity only, compared for equality
		Ino:    st.Ino,
		Nlink:  uint64(st.Nlink),
	}, true
}
