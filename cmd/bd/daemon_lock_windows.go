//go:build windows

package main

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// flockExclusive acquires an exclusive non-blocking lock on the file using LockFileEx
func flockExclusive(f *os.File) error {
	// LOCKFILE_EXCLUSIVE_LOCK (2) | LOCKFILE_FAIL_IMMEDIATELY (1) = 3
	const flags = windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY

	// Create overlapped structure for the entire file
	ol := &windows.Overlapped{}

	// Lock entire file (0xFFFFFFFF, 0xFFFFFFFF = maximum range)
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		flags,
		0,             // reserved
		0xFFFFFFFF,    // number of bytes to lock (low)
		0xFFFFFFFF,    // number of bytes to lock (high)
		ol,
	)

	if err == windows.ERROR_LOCK_VIOLATION || err == syscall.EWOULDBLOCK {
		return ErrDaemonLocked
	}

	return err
}
