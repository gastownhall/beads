//go:build unix

package lockfile

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var errProcessLocked = errors.New("lock already held by another process")

// flockExclusive acquires an exclusive non-blocking lock on the file
func flockExclusive(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == unix.EWOULDBLOCK {
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
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}

// FlockUnlock releases a lock on the file.
func FlockUnlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
