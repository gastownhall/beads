// Package filelock provides cross-process file locking for callers outside
// the storage adapter packages. internal/metrics needs the same on-disk lock
// eventkit.FileFlusher itself uses (see internal/metrics/flusher.go), but the
// dolt-storage-boundary depguard rule in .golangci.yml restricts
// github.com/dolthub/* imports to internal/storage/** and
// internal/doltserver/** so a subsystem like metrics can't reach into Dolt's
// dependency tree directly. This package is the one place inside that
// boundary allowed to import github.com/dolthub/fslock; everything outside
// internal/storage goes through here instead, narrowed to the three methods
// callers actually need.
package filelock

import (
	"context"

	"github.com/dolthub/fslock"
)

// Lock is a cross-process, advisory file lock. The zero value is not usable;
// construct one with New.
type Lock struct {
	inner *fslock.Lock
}

// New opens (without acquiring) a lock backed by the file at path.
func New(path string) (*Lock, error) {
	inner, err := fslock.New(path)
	if err != nil {
		return nil, err
	}
	return &Lock{inner: inner}, nil
}

// TryLock acquires the lock without blocking, returning an error immediately
// if it is already held by another holder.
func (l *Lock) TryLock() error {
	return l.inner.TryLock()
}

// LockWithContext blocks until the lock is acquired or ctx is done, whichever
// comes first.
func (l *Lock) LockWithContext(ctx context.Context) error {
	return l.inner.LockWithContext(ctx)
}

// Unlock releases the lock.
func (l *Lock) Unlock() error {
	return l.inner.Unlock()
}
