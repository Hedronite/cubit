package serve

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestValidForIsTwiceTheInterval(t *testing.T) {
	for in, want := range map[time.Duration]int{
		0: 1, 400 * time.Millisecond: 1, 30 * time.Second: 60, 5 * time.Minute: 600, 45500 * time.Millisecond: 91,
	} {
		if got := validFor(in); got != want {
			t.Errorf("validFor(%v) = %d, want %d", in, got, want)
		}
	}
}

// Every served document carries the header, with valid_for from the interval.
func TestServedDocumentsCarryTheHeader(t *testing.T) {
	d := tree(t)
	sp := spec(t, wsRoot("a", d))
	digest := "sha256:" + strings.Repeat("d", 64)
	sp.Declaration, sp.ValidFor = &digest, 7 // serve decides valid_for itself
	r := start(t, Options{Spec: sp, Interval: 45 * time.Second}, slog.LevelError)
	defer r.cancel()
	select {
	case doc := <-r.scans:
		if !doc.Observed || doc.ValidFor != 90 || doc.Declaration == nil || *doc.Declaration != digest {
			t.Fatalf("observed %v valid_for %d declaration %v", doc.Observed, doc.ValidFor, doc.Declaration)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no scan")
	}
}
