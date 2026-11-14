package main

import (
	"syscall"
	"testing"
)

func TestRaiseFileLimit(t *testing.T) {
	var before syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &before); err != nil {
		t.Skip(err)
	}
	got := raiseFileLimit()
	want := uint64(wantFiles)
	if before.Max < want {
		want = before.Max
	}
	if before.Cur >= want {
		want = before.Cur // already enough: never lowered
	}
	if got < want {
		t.Errorf("soft limit %d, want at least %d", got, want)
	}
}
