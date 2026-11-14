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
		Blocks: u64(st.Blocks),
		Dev:    u64(st.Dev),
		Ino:    u64(st.Ino),
		Nlink:  u64(st.Nlink),
	}, true
}

// u64 widens a stat field. The fields are int32, uint16, uint32 or uint64
// depending on the platform, so the conversion is written once, generically,
// instead of being flagged as redundant on some platforms and needed on others.
// Device and inode numbers are identities, compared only for equality.
func u64[T ~int32 | ~int64 | ~uint16 | ~uint32 | ~uint64](v T) uint64 {
	return uint64(v)
}
