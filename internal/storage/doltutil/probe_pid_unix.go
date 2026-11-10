//go:build unix

package doltutil

import (
	"errors"
	"syscall"
)

// sweepsOtherRunsProbes says the sweep can tell a dead run's probe from a
// live one on this platform, so it removes another run's leftover.
const sweepsOtherRunsProbes = true

// probeProcessAlive reports whether a process with pid is running, with the
// kill(pid, 0) that internal/doltserver uses, read more strictly: there any
// error means dead, here only ESRCH ("no such process") does. EPERM means the
// process exists under another user, and any other errno means the check
// itself failed, and both report alive so the caller leaves the remote in
// place on an inconclusive read. kill(0, sig) would signal the caller's own
// process group and kill(-1, sig) everything it may signal, so a pid that is
// not positive never reaches the syscall and reports alive.
func probeProcessAlive(pid int) bool {
	if pid <= 0 {
		return true
	}
	err := syscall.Kill(pid, 0)
	return !errors.Is(err, syscall.ESRCH)
}
