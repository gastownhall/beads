// Package util provides shared utilities for the db storage backends.
package util

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/lockfile"
)

// Unlocker is the interface for releasing an acquired lock.
type Unlocker interface {
	Unlock()
}

// Lock holds an exclusive flock on a file.
type Lock struct {
	f         *os.File
	retirable bool
	retired   bool
}

// TryLock attempts to acquire a non-blocking exclusive flock on lockPath.
// The parent directory is created (mode 0700) if it does not exist. On
// contention returns an error that satisfies lockfile.IsLocked, so callers
// can detect "another holder is alive" and produce their own contextual
// error message.
func TryLock(lockPath string) (*Lock, error) {
	return tryLock(lockPath, openLockFile)
}

// TryRetirableLock is TryLock for a lease whose holder must remove the lock
// file while still holding it (see Retire). On Windows the file is opened
// with delete sharing, which ordinary TryLock leases deliberately lack;
// elsewhere it behaves exactly like TryLock. Contention still satisfies
// lockfile.IsLocked.
func TryRetirableLock(lockPath string) (*Lock, error) {
	l, err := tryLock(lockPath, openRetirableLockFile)
	if err != nil {
		return nil, err
	}
	l.retirable = true
	return l, nil
}

func tryLock(lockPath string, open func(string) (*os.File, error)) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, fmt.Errorf("util: creating lock directory: %w", err)
	}
	f, err := open(lockPath)
	if err != nil {
		return nil, fmt.Errorf("util: opening lock file: %w", err)
	}
	if err := lockfile.FlockExclusiveNonBlocking(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

func openLockFile(lockPath string) (*os.File, error) {
	return os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // lockPath comes from caller-derived dirs, not user input
}

// Retire removes the lock file of a lease acquired with TryRetirableLock
// while the lease stays held, so no other holder can slip in between
// retirement and release. On Windows the pathname stays unavailable to new
// opens until Unlock closes the lease; elsewhere it is removed immediately.
// A pathname that is already gone counts as retired. Retire is idempotent
// and refuses ordinary or released leases.
func (l *Lock) Retire() error {
	if l == nil || l.f == nil || l.f.Fd() == ^uintptr(0) {
		return errors.New("util: retire: lease is not held")
	}
	if !l.retirable {
		return fmt.Errorf("util: retire %s: lease was not acquired with TryRetirableLock", l.f.Name())
	}
	if l.retired {
		return nil
	}
	if err := retireLockFile(l.f); err != nil {
		return fmt.Errorf("util: retiring lock file: %w", err)
	}
	l.retired = true
	return nil
}

// File returns the underlying *os.File. Useful for fork+exec lock-fd
// inheritance: pass it via cmd.ExtraFiles, then Close() the parent's fd —
// the child retains the lock through its inherited fd, which references the
// same open file description.
func (l *Lock) File() *os.File {
	return l.f
}

// Unlock releases the flock and closes the underlying file. Panics on
// failure to prevent silent deadlocks.
//
// Do NOT call Unlock in fork+exec handoff scenarios where the lock fd has
// been inherited by a child: flock's LOCK_UN releases the lock from the OFD,
// which would silently drop the child's lock too. In that case use
// l.File().Close() to drop only the parent's fd reference.
func (l *Lock) Unlock() {
	if err := lockfile.FlockUnlock(l.f); err != nil {
		panic(fmt.Sprintf("util: failed to release lock: %v", err))
	}
	if err := l.f.Close(); err != nil {
		panic(fmt.Sprintf("util: failed to close lock file: %v", err))
	}
}

// NoopLock is a lock that does nothing. Used when the caller wants to skip
// lock acquisition (e.g., server mode where the external server handles its
// own concurrency).
type NoopLock struct{}

// Unlock is a no-op.
func (NoopLock) Unlock() {}
