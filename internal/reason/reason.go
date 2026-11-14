// Package reason holds the closed enums of cubit.scan/v0 (docs/metrics.md), so
// the walk, the scan and the report cannot spell one differently from the
// schema. A test compares these lists to the schema's enums.
package reason

// Skip reasons: entries or subtrees skipped, in cubit_skipped_entries.
const (
	Refused          = "refused"
	SymlinkEscape    = "symlink_escape"
	SymlinkLoop      = "symlink_loop"
	PermissionDenied = "permission_denied"
	ForeignDevice    = "foreign_device"
	DuplicateTarget  = "duplicate_target"
	Vanished         = "vanished"
	DepthExceeded    = "depth_exceeded"
	DedupeOverflow   = "dedupe_overflow"
	IOError          = "io_error"
)

// Error reasons: why a root failed or is partial, in cubit_scan_errors_total.
const (
	RootMissing       = "root_missing"
	RootUnreadable    = "root_unreadable"
	DeadlineExceeded  = "deadline_exceeded"
	MaxFilesExceeded  = "max_files_exceeded"
	SubtreeUnreadable = "subtree_unreadable"
)

// Warning reasons: only in the scan document.
const (
	NoWorkspaces  = "no_workspaces"
	UnmatchedName = "unmatched_name"
)

// Skip, Error and Warning list every value of each enum.
var (
	Skip    = []string{Refused, SymlinkEscape, SymlinkLoop, PermissionDenied, ForeignDevice, DuplicateTarget, Vanished, DepthExceeded, DedupeOverflow, IOError}
	Error   = []string{RootMissing, RootUnreadable, DeadlineExceeded, MaxFilesExceeded, SubtreeUnreadable}
	Warning = []string{NoWorkspaces, UnmatchedName}
)

// MakesPartial reports whether a skip leaves bytes unmeasured, so the root is
// partial. Refused, symlink, device, duplicate and vanished skips do not: what
// they skip is excluded by design or gone.
func MakesPartial(skip string) bool {
	return skip == PermissionDenied || skip == IOError || skip == DepthExceeded
}
