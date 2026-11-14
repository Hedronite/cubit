// Package walk measures allocated bytes under a directory tree and finds Cargo
// workspaces. It reads only through internal/contain: every directory is opened
// as an os.Root, symlinks are never followed, and refused names are matched
// before anything is opened or statted.
//
// A unit (one target dir, one cache area) is walked by exactly one goroutine in
// sorted name order, so a complete walk gives the same answer however many
// workers the caller runs.
package walk
