package dolt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
)

// AccessLock coordinates access to the embedded Dolt database using flock.
// Shared locks allow concurrent readers; exclusive locks ensure single-writer.
// The lock file lives in .beads/dolt-access.lock (alongside daemon.lock).
type AccessLock struct {
	file *os.File
	path string
}

const (
	// accessLockFile is the name of the advisory lock file in .beads/.
	accessLockFile = "dolt-access.lock"

	// lockPollInterval is how often we retry acquiring the lock.
	lockPollInterval = 50 * time.Millisecond
)

// AcquireAccessLock acquires an advisory flock on the dolt-access.lock file.
// If exclusive is true, acquires an exclusive lock (for writes); otherwise shared (for reads).
// Polls with lockPollInterval until timeout expires. Returns ErrLockBusy on timeout.
//
// The doltDir parameter is the path to the dolt data directory (e.g., .beads/dolt).
// The lock file is placed in the parent directory (.beads/) alongside daemon.lock.
func AcquireAccessLock(doltDir string, exclusive bool, timeout time.Duration) (*AccessLock, error) {
	// Lock file goes in parent of dolt dir (i.e., .beads/)
	beadsDir := filepath.Dir(doltDir)
	lockPath := filepath.Join(beadsDir, accessLockFile)

	// Ensure parent directory exists
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	// Open or create lock file
	// #nosec G304 - controlled path derived from database configuration
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open access lock: %w", err)
	}

	// Pick the right lock function
	lockFn := lockfile.FlockSharedNonBlock
	if exclusive {
		lockFn = lockfile.FlockExclusiveNonBlock
	}

	// Try once immediately
	if err := lockFn(f); err == nil {
		return &AccessLock{file: f, path: lockPath}, nil
	} else if !errors.Is(err, lockfile.ErrLockBusy) {
		_ = f.Close()
		return nil, fmt.Errorf("access lock: %w", err)
	}

	// Poll until timeout
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(lockPollInterval)

		if err := lockFn(f); err == nil {
			return &AccessLock{file: f, path: lockPath}, nil
		} else if !errors.Is(err, lockfile.ErrLockBusy) {
			_ = f.Close()
			return nil, fmt.Errorf("access lock: %w", err)
		}
	}

	_ = f.Close()
	kind := "shared"
	if exclusive {
		kind = "exclusive"
	}
	return nil, fmt.Errorf("dolt access lock timeout (%s, %v): another bd process is using the database: %w",
		kind, timeout, lockfile.ErrLockBusy)
}

// Release releases the advisory lock and closes the underlying file.
// Safe to call multiple times (idempotent).
func (l *AccessLock) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = lockfile.FlockUnlock(l.file)
	_ = l.file.Close()
	l.file = nil
}
