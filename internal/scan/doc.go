// Package scan turns a validated config into one cubit.scan/v0 document. It
// owns the rules above the walk: which target dirs belong to which root, how
// workspaces are labelled and capped, and what makes a root partial or failed.
// The document is the single source that `cubit scan` prints and that the
// metrics are rendered from.
package scan
