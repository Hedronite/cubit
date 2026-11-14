//go:build unix

package main

import "syscall"

// wantFiles is the open-file soft limit Cubit asks for. A walk holds a few
// descriptors per level of depth on each worker, and a LaunchAgent starts with a
// soft limit as low as 256, so running out would show up as unreadable
// subtrees rather than as the real cause.
const wantFiles = 4096

// raiseFileLimit lifts the soft RLIMIT_NOFILE to min(hard, wantFiles) when it is
// lower, and reports the limit now in force.
func raiseFileLimit() (soft uint64) {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		return 0
	}
	want := uint64(wantFiles)
	if rl.Max < want {
		want = rl.Max
	}
	if rl.Cur < want {
		rl.Cur = want
		_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rl)
		// Read it back: report the limit in force, not the one asked for.
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
			return 0
		}
	}
	return rl.Cur
}
