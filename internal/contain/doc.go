// Package contain is the only place Cubit touches the filesystem namespace.
//
// Every root is opened once with os.OpenRoot, and every directory below it is
// opened with Root.OpenRoot on a single path component, so an escape fails at
// the syscall rather than being caught by a check that could race. The refuse
// list (docs/refuse-list.md) is matched here, by name, before anything is
// opened or statted. The exception is building the list itself, which follows
// the symlinks on the way to each entry so that the real place is refused too.
package contain
