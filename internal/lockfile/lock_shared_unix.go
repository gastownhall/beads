//go:build unix

package lockfile

import (
	"os"

	"golang.org/x/sys/unix"
)

// FlockSharedNonBlock acquires a shared non-blocking lock on the file.
// Multiple processes can hold shared locks concurrently.
// Returns ErrLockBusy if an exclusive lock is already held.
func FlockSharedNonBlock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB)
	if err == unix.EWOULDBLOCK {
		return ErrLockBusy
	}
	return err
}

// FlockExclusiveNonBlock acquires an exclusive non-blocking lock on the file.
// Returns ErrLockBusy if any lock (shared or exclusive) is already held.
func FlockExclusiveNonBlock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == unix.EWOULDBLOCK {
		return ErrLockBusy
	}
	return err
}
