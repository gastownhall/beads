//go:build windows

package lockfile

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// FlockSharedNonBlock acquires a shared non-blocking lock on the file.
// Multiple processes can hold shared locks concurrently.
// Returns ErrLockBusy if an exclusive lock is already held.
func FlockSharedNonBlock(f *os.File) error {
	// Shared + fail immediately (no LOCKFILE_EXCLUSIVE_LOCK)
	const flags = windows.LOCKFILE_FAIL_IMMEDIATELY

	ol := &windows.Overlapped{}
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		flags,
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		ol,
	)

	if err == windows.ERROR_LOCK_VIOLATION || err == syscall.EWOULDBLOCK {
		return ErrLockBusy
	}
	return err
}

// FlockExclusiveNonBlock acquires an exclusive non-blocking lock on the file.
// Returns ErrLockBusy if any lock (shared or exclusive) is already held.
func FlockExclusiveNonBlock(f *os.File) error {
	const flags = windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY

	ol := &windows.Overlapped{}
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		flags,
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		ol,
	)

	if err == windows.ERROR_LOCK_VIOLATION || err == syscall.EWOULDBLOCK {
		return ErrLockBusy
	}
	return err
}
