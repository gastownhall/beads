// Package filelock provides cross-process file locking for callers outside
// the storage adapter packages. internal/metrics needs the same on-disk lock
// eventkit.FileFlusher itself uses (see internal/metrics/flusher.go), but the
// dolt-storage-boundary depguard rule in .golangci.yml restricts
// github.com/dolthub/* imports to internal/storage/** and
// internal/doltserver/** so a subsystem like metrics can't reach into Dolt's
// dependency tree directly. This package is the one place inside that
// boundary allowed to import github.com/dolthub/fslock; everything outside
// internal/storage goes through here instead, narrowed to the few methods
// callers actually need. Note that depguard runs with `tests: false`, so that
// boundary is enforced mechanically for production code only — a test file
// importing fslock directly would lint clean, so keep test holders on this
// wrapper by convention (internal/metrics/flusher_test.go does).
package filelock

import (
	"context"

	"github.com/dolthub/fslock"
)

// ErrLocked is what TryLock returns when another holder already owns the
// lock. It is the ONLY error that means "someone else won"; every other error
// means locking itself did not work here, because fslock hands back the raw
// open(2)/flock(2) failure verbatim — ENOLCK or EOPNOTSUPP on an NFS home
// without lockd, a 9p/drvfs $HOME such as WSL /mnt/c, some FUSE mounts. A
// caller that collapses the two classes silently loses whatever the lock
// guards on such a host, permanently and with nothing logged, so match this
// with errors.Is and treat the rest as "locking is unavailable". It is
// re-exported here rather than matched at the call site because the
// dolt-storage-boundary depguard rule keeps github.com/dolthub/fslock out of
// callers like internal/metrics.
var ErrLocked = fslock.ErrLocked

// Lock is a cross-process, advisory file lock. The zero value is not usable;
// construct one with New. A Lock owns an open handle to the directory holding
// the lock file, so every New must be paired with a Close — see Close.
type Lock struct {
	inner *fslock.Lock
}

// New opens (without acquiring) a lock backed by the file at path. It opens a
// handle to path's parent directory, which Close releases, so a caller that
// does not Close leaks one directory descriptor per New for the life of the
// process.
func New(path string) (*Lock, error) {
	inner, err := fslock.New(path)
	if err != nil {
		return nil, err
	}
	return &Lock{inner: inner}, nil
}

// TryLock acquires the lock without blocking. It returns ErrLocked
// immediately if another holder already owns it, and any other failure
// verbatim — see ErrLocked for why callers must tell those two apart.
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

// Close releases the directory handle New opened. It does NOT release a held
// lock — call Unlock first — and the Lock must not be used afterwards. Pair
// every New with a Close (`defer lock.Close()` registered before the Unlock
// defer, so the LIFO order unlocks and then closes); otherwise the handle
// survives until the process exits.
func (l *Lock) Close() error {
	return l.inner.Close()
}
