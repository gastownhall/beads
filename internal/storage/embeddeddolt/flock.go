//go:build embeddeddolt

package embeddeddolt

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gastownhall/beads/internal/lockfile"
)

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
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		return nil, fmt.Errorf("embeddeddolt: creating data directory for lock: %w", err)
	}

	lockPath := filepath.Join(dataDir, ".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("embeddeddolt: opening lock file: %w", err)
	}

	if err := lockfile.FlockExclusiveNonBlocking(f); err != nil {
		f.Close()
		if lockfile.IsLocked(err) {
			return nil, fmt.Errorf("embeddeddolt: another process holds the exclusive lock on %s; "+
				"the embedded backend supports only one writer at a time — "+
				"use the dolt server backend for concurrent access", dataDir)
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
