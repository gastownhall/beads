//go:build windows

package lockfile

import (
	"os"
	"syscall"
)

// FlockSharedNonBlock acquires a shared non-blocking lock on the file.
// Multiple processes can hold shared locks concurrently.
// Returns ErrLockBusy if an exclusive lock is already held.
func FlockSharedNonBlock(f *os.File) error {
	// Shared + fail immediately (no LOCKFILE_EXCLUSIVE_LOCK)
	const flags = syscall.LOCKFILE_FAIL_IMMEDIATELY

	ol := &syscall.Overlapped{}
	err := syscall.LockFileEx(
		syscall.Handle(f.Fd()),
		flags,
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		ol,
	)

	if err == errLockViolation || err == syscall.EWOULDBLOCK {
		return ErrLockBusy
	}
	return err
}

// FlockExclusiveNonBlock acquires an exclusive non-blocking lock on the file.
// Returns ErrLockBusy if any lock (shared or exclusive) is already held.
func FlockExclusiveNonBlock(f *os.File) error {
	const flags = syscall.LOCKFILE_EXCLUSIVE_LOCK | syscall.LOCKFILE_FAIL_IMMEDIATELY

	ol := &syscall.Overlapped{}
	err := syscall.LockFileEx(
		syscall.Handle(f.Fd()),
		flags,
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		ol,
	)

	if err == errLockViolation || err == syscall.EWOULDBLOCK {
		return ErrLockBusy
	}
	return err
}
