//go:build unix

package lockfile

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var errProcessLocked = errors.New("lock already held by another process")

// flockSyscall is flock(2) for the lock helpers' acquisitions; a var so
// tests can stand in a filesystem whose lock call fails.
var flockSyscall = unix.Flock

// flockExclusive acquires an exclusive non-blocking lock on the file
func flockExclusive(f *os.File) error {
	err := flockSyscall(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == unix.EWOULDBLOCK {
		return errProcessLocked
	}
	return classifyLockErr(err)
}

// FlockExclusiveNonBlocking attempts to acquire an exclusive lock without blocking.
// Returns ErrLocked if the lock is held by another process.
func FlockExclusiveNonBlocking(f *os.File) error {
	return flockExclusive(f)
}

// FlockExclusiveBlocking acquires an exclusive blocking lock on the file.
// This will wait until the lock is available.
func FlockExclusiveBlocking(f *os.File) error {
	return classifyLockErr(flockSyscall(int(f.Fd()), unix.LOCK_EX))
}

// classifyLockErr marks errnos that mean the filesystem cannot lock at all
// (ENOLCK; ENOSYS, ENOTSUP and EOPNOTSUPP via errors.ErrUnsupported) as
// *UnsupportedError. Every other error, nil included, is returned unchanged.
func classifyLockErr(err error) error {
	if err != nil && (errors.Is(err, unix.ENOLCK) || errors.Is(err, errors.ErrUnsupported)) {
		return &UnsupportedError{Op: "flock", Err: err}
	}
	return err
}

// FlockUnlock releases a lock on the file.
func FlockUnlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
