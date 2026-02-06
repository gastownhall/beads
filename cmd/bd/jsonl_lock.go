package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/beads/internal/debug"
)

const (
	// jsonlLockFileName is the name of the lock file used to coordinate JSONL access
	jsonlLockFileName = ".jsonl.lock"

	// jsonlLockTimeout is the maximum time to wait for the lock before giving up
	jsonlLockTimeout = 30 * time.Second

	// jsonlLockPollInterval is how often to retry acquiring the lock
	jsonlLockPollInterval = 50 * time.Millisecond
)

// JSONLLock provides file-based locking for coordinating JSONL access between:
// - bd sync export operations
// - daemon auto-flush operations
// - auto-import operations
//
// The lock prevents race conditions where multiple processes write to issues.jsonl
// simultaneously, causing data loss (last writer wins) or partial reads.
//
// Lock modes:
// - Exclusive: Used for write operations (export, auto-flush)
// - Shared: Used for read operations (auto-import)
//
// The lock file is located at .beads/.jsonl.lock
type JSONLLock struct {
	flock    *flock.Flock
	beadsDir string
	mode     string // "exclusive" or "shared"
}

// newJSONLLock creates a new JSONLLock for the given beads directory.
func newJSONLLock(beadsDir string) *JSONLLock {
	lockPath := filepath.Join(beadsDir, jsonlLockFileName)
	return &JSONLLock{
		flock:    flock.New(lockPath),
		beadsDir: beadsDir,
	}
}

// AcquireExclusive acquires an exclusive lock for write operations.
// This blocks other writers and readers until the lock is released.
// Use this for export and auto-flush operations.
//
// The lock will be retried with polling until the context is done or timeout is reached.
// Returns an error if the lock cannot be acquired within the timeout.
func (l *JSONLLock) AcquireExclusive(ctx context.Context) error {
	return l.acquireWithRetry(ctx, true)
}

// AcquireShared acquires a shared lock for read operations.
// This allows multiple concurrent readers but blocks writers.
// Use this for auto-import operations.
//
// The lock will be retried with polling until the context is done or timeout is reached.
// Returns an error if the lock cannot be acquired within the timeout.
func (l *JSONLLock) AcquireShared(ctx context.Context) error {
	return l.acquireWithRetry(ctx, false)
}

// TryAcquireExclusive attempts to acquire an exclusive lock without blocking.
// Returns true if the lock was acquired, false otherwise.
func (l *JSONLLock) TryAcquireExclusive() (bool, error) {
	locked, err := l.flock.TryLock()
	if err != nil {
		return false, fmt.Errorf("failed to acquire exclusive JSONL lock: %w", err)
	}
	if locked {
		l.mode = "exclusive"
		debug.Logf("acquired exclusive JSONL lock: %s", l.flock.Path())
	}
	return locked, nil
}

// TryAcquireShared attempts to acquire a shared lock without blocking.
// Returns true if the lock was acquired, false otherwise.
func (l *JSONLLock) TryAcquireShared() (bool, error) {
	locked, err := l.flock.TryRLock()
	if err != nil {
		return false, fmt.Errorf("failed to acquire shared JSONL lock: %w", err)
	}
	if locked {
		l.mode = "shared"
		debug.Logf("acquired shared JSONL lock: %s", l.flock.Path())
	}
	return locked, nil
}

// Release releases the lock.
// Safe to call multiple times (idempotent).
func (l *JSONLLock) Release() error {
	if l.flock == nil {
		return nil
	}
	debug.Logf("releasing %s JSONL lock: %s", l.mode, l.flock.Path())
	return l.flock.Unlock()
}

// acquireWithRetry attempts to acquire the lock with retries until timeout.
func (l *JSONLLock) acquireWithRetry(ctx context.Context, exclusive bool) error {
	// Create a timeout context if needed
	timeoutCtx, cancel := context.WithTimeout(ctx, jsonlLockTimeout)
	defer cancel()

	start := time.Now()
	lockType := "shared"
	if exclusive {
		lockType = "exclusive"
	}

	for {
		var locked bool
		var err error

		if exclusive {
			locked, err = l.flock.TryLock()
		} else {
			locked, err = l.flock.TryRLock()
		}

		if err != nil {
			return fmt.Errorf("failed to acquire %s JSONL lock: %w", lockType, err)
		}

		if locked {
			l.mode = lockType
			debug.Logf("acquired %s JSONL lock after %v: %s", lockType, time.Since(start), l.flock.Path())
			return nil
		}

		// Check if we should give up
		select {
		case <-timeoutCtx.Done():
			elapsed := time.Since(start)
			return fmt.Errorf(
				"timeout waiting for %s JSONL lock after %v (another process may be syncing or exporting - try again in a moment)",
				lockType, elapsed,
			)
		default:
			// Wait before retrying
			time.Sleep(jsonlLockPollInterval)
		}
	}
}

// WithJSONLLockExclusive executes a function while holding an exclusive JSONL lock.
// The lock is automatically released when the function returns.
// If the lock cannot be acquired within the timeout, an error is returned without
// executing the function.
func WithJSONLLockExclusive(ctx context.Context, beadsDir string, fn func() error) error {
	lock := newJSONLLock(beadsDir)
	if err := lock.AcquireExclusive(ctx); err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	return fn()
}

// WithJSONLLockShared executes a function while holding a shared JSONL lock.
// The lock is automatically released when the function returns.
// If the lock cannot be acquired within the timeout, an error is returned without
// executing the function.
func WithJSONLLockShared(ctx context.Context, beadsDir string, fn func() error) error {
	lock := newJSONLLock(beadsDir)
	if err := lock.AcquireShared(ctx); err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	return fn()
}

// TryWithJSONLLockExclusive attempts to execute a function with an exclusive lock.
// If the lock cannot be acquired immediately, returns false without executing fn.
// If the lock is acquired, executes fn and returns true with any error from fn.
func TryWithJSONLLockExclusive(beadsDir string, fn func() error) (bool, error) {
	lock := newJSONLLock(beadsDir)
	locked, err := lock.TryAcquireExclusive()
	if err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	defer func() { _ = lock.Release() }()

	return true, fn()
}
