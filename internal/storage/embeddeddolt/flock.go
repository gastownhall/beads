//go:build cgo

package embeddeddolt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	backoff "github.com/cenkalti/backoff/v4"
	"github.com/steveyegge/beads/internal/lockfile"
)

// Unlocker is the interface for releasing an acquired lock.
type Unlocker interface {
	Unlock()
}

// Lock holds an exclusive flock on the embeddeddolt data directory.
// Used by commands that require single-writer access (e.g., bd init).
type Lock struct {
	f *os.File
}

// TryLock attempts to acquire a non-blocking exclusive flock on <dataDir>/.lock.
// dataDir is created if it does not exist. Returns the held lock on success.
// If another process holds the lock, returns an error directing the user to
// the dolt server backend for concurrent access.
func TryLock(dataDir string) (*Lock, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("embeddeddolt: creating data directory for lock: %w", err)
	}

	lockPath := filepath.Join(dataDir, ".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // lockPath is derived from dataDir, not user input
	if err != nil {
		return nil, fmt.Errorf("embeddeddolt: opening lock file: %w", err)
	}

	if err := lockfile.FlockExclusiveNonBlocking(f); err != nil {
		_ = f.Close()
		if lockfile.IsLocked(err) {
			return nil, fmt.Errorf("embeddeddolt: another process holds the exclusive lock on %s; "+
				"the embedded backend supports only one writer at a time — "+
				"use the dolt server backend for concurrent access", dataDir)
		}
		return nil, fmt.Errorf("embeddeddolt: acquiring lock: %w", err)
	}

	return &Lock{f: f}, nil
}

// WaitLock blocks until an exclusive flock on <dataDir>/.lock can be acquired
// or the context is canceled. It uses exponential backoff with non-blocking
// lock attempts so the wait is interruptible via context cancellation.
// Non-lock filesystem errors are returned immediately without retrying.
func WaitLock(ctx context.Context, dataDir string) (*Lock, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("embeddeddolt: creating data directory for lock: %w", err)
	}

	lockPath := filepath.Join(dataDir, ".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // lockPath is derived from dataDir, not user input
	if err != nil {
		return nil, fmt.Errorf("embeddeddolt: opening lock file: %w", err)
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 50 * time.Millisecond
	bo.MaxInterval = 2 * time.Second
	bo.MaxElapsedTime = 0 // wait until context cancellation

	err = backoff.Retry(func() error {
		lockErr := lockfile.FlockExclusiveNonBlocking(f)
		if lockErr == nil {
			return nil // acquired
		}
		if lockfile.IsLocked(lockErr) {
			return lockErr // retryable
		}
		// Filesystem error — not retryable.
		return backoff.Permanent(lockErr)
	}, backoff.WithContext(bo, ctx))

	if err != nil {
		_ = f.Close()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("embeddeddolt: waiting for lock on %s: %w", dataDir, ctx.Err())
		}
		return nil, fmt.Errorf("embeddeddolt: acquiring lock: %w", err)
	}

	return &Lock{f: f}, nil
}

// Unlock releases the flock and closes the underlying file.
// Panics on failure to prevent deadlocks.
func (l *Lock) Unlock() {
	if err := lockfile.FlockUnlock(l.f); err != nil {
		panic(fmt.Sprintf("embeddeddolt: failed to release lock: %v", err))
	}
	if err := l.f.Close(); err != nil {
		panic(fmt.Sprintf("embeddeddolt: failed to close lock file: %v", err))
	}
}

// NoopLock is a lock that does nothing. Used in server mode where the
// external dolt sql-server handles its own concurrency.
type NoopLock struct{}

// Unlock is a no-op.
func (NoopLock) Unlock() {}
