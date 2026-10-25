//go:build darwin || linux

package doltserver

import (
	"errors"
	"os"
	"syscall"
)

// rootOwnedBySelf reports whether the directory info describes is owned by
// the user running this process.
//
// SweepDeadSuiteRoots globs a world-writable temp directory, so on a shared
// box (/tmp, a CI runner with several users) anyone could plant a directory
// carrying this suite's prefix and an owner marker naming a PID that is not
// running, and have the sweep delete a tree of their choosing under the
// caller's identity. Ownership is the cheap check that closes it. An
// unreadable or unexpected stat shape reports false — not ours, leave it.
func rootOwnedBySelf(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(stat.Uid) == os.Getuid()
}

// processAlive reports whether pid still names a running process, using the
// signal-0 probe (kill(2) performs its permission and existence checks but
// delivers nothing).
//
// Only ESRCH — "no such process" — counts as dead. EPERM means the process
// exists and belongs to another user, and any other errno means the probe
// itself failed; both report alive, so SweepDeadSuiteRoots leaves the root
// untouched rather than deleting a tree on an inconclusive read.
func processAlive(pid int) bool {
	// kill(0, sig) signals the caller's whole process group and kill(-1, …)
	// every process it may signal, so a nonsensical PID must never reach the
	// syscall. Report it alive: an unusable marker is not proof of death.
	if pid <= 0 {
		return true
	}
	err := syscall.Kill(pid, 0)
	return !errors.Is(err, syscall.ESRCH)
}
