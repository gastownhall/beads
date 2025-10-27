package types

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ShouldSkipDatabase checks if the given beads directory has an exclusive lock file.
// It returns true if the database should be skipped (lock is valid and holder is alive),
// false otherwise. It also returns the lock holder name if skipping, and any error encountered.
//
// The function will:
// - Return false if no lock file exists (proceed with database)
// - Return true if lock exists and holder process is alive (skip database)
// - Remove stale locks (dead process) and return false (proceed with database)
// - Return true on malformed locks (fail-safe, skip database)
func ShouldSkipDatabase(beadsDir string) (skip bool, holder string, err error) {
	lockPath := filepath.Join(beadsDir, ".exclusive-lock")

	// Check if lock file exists
	data, err := os.ReadFile(lockPath) // #nosec G304 - controlled path from config
	if err != nil {
		if os.IsNotExist(err) {
			// No lock file, proceed with database
			return false, "", nil
		}
		// Error reading lock file, fail-safe: skip database
		return true, "", fmt.Errorf("failed to read lock file: %w", err)
	}

	// Parse lock file
	var lock ExclusiveLock
	if err := json.Unmarshal(data, &lock); err != nil {
		// Malformed lock file, fail-safe: skip database
		return true, "", fmt.Errorf("malformed lock file: %w", err)
	}

	// Validate lock
	if err := lock.Validate(); err != nil {
		// Invalid lock file, fail-safe: skip database
		return true, "", fmt.Errorf("invalid lock file: %w", err)
	}

	// Check if holder process is alive
	if !IsProcessAlive(lock.PID, lock.Hostname) {
		// Stale lock, remove it and proceed
		if err := os.Remove(lockPath); err != nil {
			// Failed to remove stale lock, fail-safe: skip database
			return true, lock.Holder, fmt.Errorf("failed to remove stale lock: %w", err)
		}
		// Stale lock removed successfully, return holder so caller can log it
		return false, lock.Holder, nil
	}

	// Lock is valid and holder is alive, skip database
	return true, lock.Holder, nil
}
