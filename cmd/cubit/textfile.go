package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/Hedronite/cubit/internal/collect"
	"github.com/Hedronite/cubit/internal/scan"
	"github.com/Hedronite/cubit/internal/textfile"
	"github.com/Hedronite/cubit/internal/version"
)

// openTextfile prepares --textfile-dir. A problem is an exit code of the
// caller's choosing: 4 for scan (nothing is scanned), 5 for serve (the
// listener never opens). The message names the operator's own directory, and a
// refused place by its class only.
func openTextfile(dir string, spec scan.Spec, code int) (*textfile.Writer, error) {
	w, err := textfile.Open(dir, home(), spec.Refuse)
	if err != nil {
		return nil, &exitError{code, preflightMessage(dir, err)}
	}
	return w, nil
}

func preflightMessage(dir string, err error) string {
	var refused *textfile.RefusedError
	var unsafe *textfile.DirModeError
	switch {
	case errors.As(err, &unsafe):
		return fmt.Sprintf("--textfile-dir %s: writable by everyone (mode %04o), so anyone could replace cubit.prom with false numbers; restrict it, for example chmod o-w on the directory", dir, unsafe.Mode)
	case errors.Is(err, textfile.ErrNotFile):
		return fmt.Sprintf("--textfile-dir %s: cubit.prom in it is a directory; remove or rename it", dir)
	case errors.Is(err, textfile.ErrForeignFile):
		return fmt.Sprintf("--textfile-dir %s: cubit.prom in it belongs to another user; remove it, or run cubit as its owner", dir)
	case errors.As(err, &refused):
		if refused.Class == "config" {
			return "--textfile-dir sits inside a refused prefix declared in this file (refuse.vault or refuse.extra); choose a directory outside every refused prefix"
		}
		return "--textfile-dir sits inside a builtin refuse-list prefix; choose a directory outside every refused prefix"
	case errors.Is(err, textfile.ErrNotExist):
		return fmt.Sprintf("--textfile-dir %s: no such directory; create it first (cubit never creates directories)", dir)
	case errors.Is(err, textfile.ErrNotDir):
		return fmt.Sprintf("--textfile-dir %s: not a directory; pass the directory node_exporter reads, not a file", dir)
	case errors.Is(err, textfile.ErrNotWritable):
		return fmt.Sprintf("--textfile-dir %s: not writable by this user; fix the directory permissions or run cubit as the user that owns it", dir)
	case errors.Is(err, textfile.ErrLocked):
		return fmt.Sprintf("another cubit holds the textfile lock in %s (one writer per directory); stop the other process or use a different directory", dir)
	case errors.Is(err, textfile.ErrNoLocking):
		return fmt.Sprintf("--textfile-dir %s: the filesystem does not support file locks; use a directory on a local filesystem", dir)
	}
	return fmt.Sprintf("--textfile-dir %s: %s; check the directory", dir, textfile.Text(err))
}

// writeFailure is the line `scan` prints when the write fails after a scan.
func writeFailure(dir string, err error) string {
	cause := textfile.Errno(err)
	if cause == "" {
		cause = textfile.Text(err)
	}
	return fmt.Sprintf("failed to write %s in %s: %s; the scan result is still valid and printed on stdout; fix the directory (disk full, permissions) and rerun if needed", textfile.FileName, dir, cause)
}

// oneShotMetrics renders a finished scan for the textfile, through a fresh
// collector. A one-shot scan has no earlier scan, so a failed root has no last
// good values and publishes health series only, and the process-scoped
// counters are left out (see collect.Gauges).
func oneShotMetrics(spec scan.Spec, doc *scan.Document) ([]byte, error) {
	aliases := make([]string, len(spec.Roots))
	for i, r := range spec.Roots {
		aliases[i] = r.Alias
	}
	col := collect.New(version.Get(), aliases)
	if err := col.Update(doc, time.Now()); err != nil {
		return nil, err
	}
	return col.Gauges(), nil
}
