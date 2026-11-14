package walk

import (
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/Hedronite/cubit/internal/contain"
)

// Logger carries what the walk may say about itself. A nil *Logger is silent.
// At Info and above a line holds the root alias, a depth and a reason, never a
// path; the relative path under the root appears only at Debug, and a refused
// name is never opened, so it can never appear.
type Logger struct {
	L       *slog.Logger // already carries the root alias
	fdSkips atomic.Int64
}

// Swap records a directory that discovery admitted and measurement then found
// replaced. It is the same event as a swap inside the walk.
func (l *Logger) Swap(depth int) { l.swap(depth, "") }

// swap records a directory replaced between the lstat and the open.
func (l *Logger) swap(depth int, rel string) {
	if l == nil || l.L == nil {
		return
	}
	l.L.Warn("directory swapped during the walk", "depth", depth,
		"hint", "the subtree was replaced while being read; the next scan measures it consistently")
	l.L.Debug("swapped directory", "depth", depth, "rel", rel)
}

// fds counts a directory skipped for file descriptor exhaustion. Done reports
// the total once per root scan. The skip itself is counted as io_error, which
// makes the root partial.
func (l *Logger) fds() {
	if l != nil {
		l.fdSkips.Add(1)
	}
}

// Done ends one root's scan: it reports file descriptor exhaustion, if any.
func (l *Logger) Done() {
	if l == nil || l.L == nil {
		return
	}
	if n := l.fdSkips.Swap(0); n > 0 {
		l.L.Warn("file descriptors exhausted; directories were skipped", "skipped", n,
			"hint", "raise the descriptor limit for the cubit process (ulimit -n) or lower scan.max_workers in the config")
	}
}

// fail counts err as a skip and logs what an operator can act on.
func (c Config) fail(t *Tally, err error, comps []string, rootLen int) {
	depth := max(len(comps)-rootLen, 0)
	switch {
	case errors.Is(err, contain.ErrChanged):
		rel := ""
		if rootLen <= len(comps) {
			rel = strings.Join(comps[rootLen:], "/")
		}
		c.Log.swap(depth, rel)
	case errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE):
		c.Log.fds()
	}
	t.skip(contain.SkipReason(err))
}
