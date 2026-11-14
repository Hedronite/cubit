package walk

import (
	"sync/atomic"
	"time"
)

// Budget is one root's caps, shared by every goroutine working on that root.
// MaxFiles counts directory entries read, each once. The deadline is judged by
// the injected clock. Once either trips, Alive stays false.
type Budget struct {
	maxFiles int64
	deadline time.Time
	now      func() time.Time

	files    atomic.Int64
	filesCut atomic.Bool
	timeCut  atomic.Bool
}

// NewBudget starts the clock now. A zero maxFiles or maxDuration means no cap.
func NewBudget(maxFiles int64, maxDuration time.Duration, now func() time.Time) *Budget {
	b := &Budget{maxFiles: maxFiles, now: now}
	if maxDuration > 0 {
		b.deadline = now().Add(maxDuration)
	}
	return b
}

// Entries records n directory entries read and reports whether the walk may go on.
func (b *Budget) Entries(n int) bool {
	if b.maxFiles > 0 && b.files.Add(int64(n)) > b.maxFiles {
		b.filesCut.Store(true)
	}
	return b.Alive()
}

// Alive reports whether no cap has tripped. It reads the clock.
func (b *Budget) Alive() bool {
	if !b.deadline.IsZero() && b.now().After(b.deadline) {
		b.timeCut.Store(true)
	}
	return !b.filesCut.Load() && !b.timeCut.Load()
}

// DeadlineExceeded reports whether max_duration ran out.
func (b *Budget) DeadlineExceeded() bool { return b.timeCut.Load() }

// FilesExceeded reports whether max_files ran out.
func (b *Budget) FilesExceeded() bool { return b.filesCut.Load() }

// Entered is the number of entries read so far.
func (b *Budget) Entered() int64 { return b.files.Load() }
