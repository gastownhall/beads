//go:build windows

package doltutil

import (
	"errors"

	"golang.org/x/sys/windows"
)

// sweepsOtherRunsProbes says the sweep can tell a dead run's probe from a
// live one on this platform, so it removes another run's leftover.
const sweepsOtherRunsProbes = true

// probeProcessAlive reports whether a process with pid is running, the way
// internal/doltserver does on Windows: opening the process is not enough,
// since an exited process stays openable while another handle refers to it,
// so a zero-timeout wait tells a signaled (exited) process from a running
// one. A pid that cannot be opened reads as dead, except for access denied,
// which means the process exists under another user and reads as alive; a
// wait that fails reads as alive too, so the caller leaves the remote in
// place on an inconclusive read.
func probeProcessAlive(pid int) bool {
	if pid <= 0 {
		return true
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	status, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return true
	}
	return status != uint32(windows.WAIT_OBJECT_0)
}
