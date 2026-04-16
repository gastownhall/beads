//go:build windows

package lockfile

import (
	"errors"
	"os"
	"syscall"
)

var errProcessLocked = errors.New("lock already held by another process")

// errLockViolation is ERROR_LOCK_VIOLATION (Windows error code 33).
var errLockViolation = syscall.Errno(33)

// flockExclusive acquires an exclusive non-blocking lock on the file using LockFileEx
func flockExclusive(f *os.File) error {
	// LOCKFILE_EXCLUSIVE_LOCK (2) | LOCKFILE_FAIL_IMMEDIATELY (1) = 3
	const flags = syscall.LOCKFILE_EXCLUSIVE_LOCK | syscall.LOCKFILE_FAIL_IMMEDIATELY

	// Create overlapped structure for the entire file
	ol := &syscall.Overlapped{}

	// Lock entire file (0xFFFFFFFF, 0xFFFFFFFF = maximum range)
	err := syscall.LockFileEx(
		syscall.Handle(f.Fd()),
		flags,
		0,          // reserved
		0xFFFFFFFF, // number of bytes to lock (low)
		0xFFFFFFFF, // number of bytes to lock (high)
		ol,
	)

	if err == errLockViolation || err == syscall.EWOULDBLOCK {
		return errProcessLocked
	}

	return err
}

// FlockExclusiveNonBlocking attempts to acquire an exclusive lock without blocking.
// Returns ErrLocked if the lock is held by another process.
func FlockExclusiveNonBlocking(f *os.File) error {
	return flockExclusive(f)
}

// FlockExclusiveBlocking acquires an exclusive blocking lock on the file.
// This will wait until the lock is available.
func FlockExclusiveBlocking(f *os.File) error {
	// LOCKFILE_EXCLUSIVE_LOCK only (no FAIL_IMMEDIATELY = blocking)
	const flags = syscall.LOCKFILE_EXCLUSIVE_LOCK

	ol := &syscall.Overlapped{}

	return syscall.LockFileEx(
		syscall.Handle(f.Fd()),
		flags,
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		ol,
	)
}

// FlockUnlock releases a lock on the file.
func FlockUnlock(f *os.File) error {
	ol := &syscall.Overlapped{}

	return syscall.UnlockFileEx(
		syscall.Handle(f.Fd()),
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		ol,
	)
}
