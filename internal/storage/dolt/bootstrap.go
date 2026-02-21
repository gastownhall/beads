package dolt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
)

// staleLockAge is the maximum age of a lock file before it's considered stale.
// Bootstrap operations should complete well within this window.
const staleLockAge = 5 * time.Minute

// BootstrapFromGitRemote clones a Dolt database from a git remote URL.
// This is used when no local .beads/dolt/ exists but config.yaml has
// sync.git-remote configured, enabling cold-start from a git remote
// that already contains Dolt data on refs/dolt/data.
//
// dolt clone creates <target>/.dolt/ directly (no database subdirectory),
// but the embedded driver expects <doltDir>/<database>/.dolt/. To reconcile,
// we clone into <doltDir>/<database>/ so the embedded driver finds it.
// If database is empty, "beads" is used.
//
// Returns true if the clone was performed, false if skipped (dolt dir already exists).
func BootstrapFromGitRemote(ctx context.Context, doltDir, gitRemoteURL string) (bool, error) {
	return BootstrapFromGitRemoteWithDB(ctx, doltDir, gitRemoteURL, "")
}

// BootstrapFromGitRemoteWithDB is like BootstrapFromGitRemote but allows
// specifying the database name (used by the embedded driver for the
// subdirectory structure).
func BootstrapFromGitRemoteWithDB(ctx context.Context, doltDir, gitRemoteURL, database string) (bool, error) {
	// Skip if Dolt database already exists
	if doltExists(doltDir) {
		return false, nil
	}

	if database == "" {
		database = "beads"
	}

	// Verify dolt CLI is available
	if _, err := exec.LookPath("dolt"); err != nil {
		return false, fmt.Errorf("dolt CLI not found (required for git remote bootstrap): %w", err)
	}

	// Create the parent dolt directory
	if err := os.MkdirAll(doltDir, 0o750); err != nil {
		return false, fmt.Errorf("failed to create dolt directory: %w", err)
	}

	// Clone into <doltDir>/<database>/ so the embedded driver can find it.
	// `dolt clone <url> <target>` creates <target>/.dolt/ directly.
	cloneTarget := filepath.Join(doltDir, database)
	cmd := exec.CommandContext(ctx, "dolt", "clone", gitRemoteURL, cloneTarget)
	if output, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("dolt clone failed: %w\nOutput: %s", err, output)
	}

	fmt.Fprintf(os.Stderr, "Bootstrapped from git remote: %s\n", gitRemoteURL)
	return true, nil
}

// doltExists checks if a Dolt database directory exists
func doltExists(doltPath string) bool {
	// The embedded Dolt driver creates the database in a subdirectory
	// named after the database (default: "beads"), with .dolt inside that.
	// So we check for any subdirectory containing a .dolt directory.
	entries, err := os.ReadDir(doltPath)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		// Use os.Stat to follow symlinks - entry.IsDir() returns false for symlinks
		fullPath := filepath.Join(doltPath, entry.Name())
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		if info.IsDir() {
			doltDir := filepath.Join(fullPath, ".dolt")
			if doltInfo, err := os.Stat(doltDir); err == nil && doltInfo.IsDir() {
				return true
			}
		}
	}
	return false
}

// schemaReady checks if the Dolt database has the required schema
// This is a simple check based on the existence of expected files.
// We avoid opening a connection here since the caller will do that.
func schemaReady(_ context.Context, doltPath string, dbName string) bool {
	if dbName == "" {
		dbName = "beads"
	}
	// The embedded Dolt driver stores databases in subdirectories.
	// Check for the expected database name's config.json which indicates
	// the database was initialized.
	configPath := filepath.Join(doltPath, dbName, ".dolt", "config.json")
	_, err := os.Stat(configPath)
	return err == nil
}

// acquireBootstrapLock acquires an exclusive lock for bootstrap operations.
// Uses non-blocking flock with polling to respect the timeout deadline.
// Detects and cleans up stale lock files from crashed processes.
func acquireBootstrapLock(lockPath string, timeout time.Duration) (*os.File, error) {
	// Check for stale lock file before attempting to acquire.
	// If the lock file is very old, the holding process likely crashed
	// without cleanup. Remove it so we can proceed.
	if info, err := os.Stat(lockPath); err == nil {
		age := time.Since(info.ModTime())
		if age > staleLockAge {
			fmt.Fprintf(os.Stderr, "Bootstrap: removing stale lock file (age: %s)\n", age.Round(time.Second))
			_ = os.Remove(lockPath) // Best effort cleanup of lock file
		}
	}

	// Create lock file
	// #nosec G304 - controlled path
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to create lock file: %w", err)
	}

	// Try to acquire lock with non-blocking flock and polling.
	deadline := time.Now().Add(timeout)
	for {
		err := lockfile.FlockExclusiveNonBlocking(f)
		if err == nil {
			// Lock acquired - update modification time for stale detection
			return f, nil
		}

		if !lockfile.IsLocked(err) {
			// Unexpected error (not contention)
			_ = f.Close() // Best effort cleanup on error path
			return nil, fmt.Errorf("failed to acquire bootstrap lock: %w", err)
		}

		if time.Now().After(deadline) {
			_ = f.Close() // Best effort cleanup on error path
			return nil, fmt.Errorf("timeout after %s waiting for bootstrap lock (another bootstrap may be running)", timeout)
		}

		// Wait briefly before retrying
		time.Sleep(100 * time.Millisecond)
	}
}

// releaseBootstrapLock releases the bootstrap lock and removes the lock file
func releaseBootstrapLock(f *os.File, lockPath string) {
	if f != nil {
		_ = lockfile.FlockUnlock(f) // Best effort: unlock may fail if fd is bad
		_ = f.Close()               // Best effort cleanup
	}
	// Clean up lock file
	_ = os.Remove(lockPath) // Best effort cleanup of lock file
}
