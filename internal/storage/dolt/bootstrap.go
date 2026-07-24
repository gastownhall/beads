package dolt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/remotecache"
)

// staleLockAge is the maximum age of a lock file before it's considered stale.
// Bootstrap operations should complete well within this window.
const staleLockAge = 5 * time.Minute

// BootstrapFromRemote clones a Dolt database from a remote URL.
// This is used when no local .beads/dolt/ exists but config.yaml has
// sync.remote configured, enabling cold-start from any Dolt-compatible
// remote (git, DoltHub, S3, GCS, file, etc.).
//
// dolt clone creates <target>/.dolt/ directly (no database subdirectory),
// but the embedded driver expects <doltDir>/<database>/.dolt/. To reconcile,
// we clone into <doltDir>/<database>/ so the embedded driver finds it.
// Uses the default database name ("beads"). Prefer BootstrapFromRemoteWithDB
// when a configured database name is available.
//
// Returns true if the clone was performed, false if skipped (dolt dir already exists).
func BootstrapFromRemote(ctx context.Context, doltDir, remoteURL string) (bool, error) {
	return BootstrapFromRemoteWithDB(ctx, doltDir, remoteURL, configfile.DefaultDoltDatabase)
}

// BootstrapFromGitRemote is deprecated. Use BootstrapFromRemote instead.
func BootstrapFromGitRemote(ctx context.Context, doltDir, gitRemoteURL string) (bool, error) {
	return BootstrapFromRemote(ctx, doltDir, gitRemoteURL)
}

// BootstrapFromRemoteWithDB is like BootstrapFromRemote but allows
// specifying the database name (used by the embedded driver for the
// subdirectory structure). The database parameter must not be empty;
// callers should use cfg.GetDoltDatabase() which applies the fallback chain
// (env var → config → default).
func BootstrapFromRemoteWithDB(ctx context.Context, doltDir, remoteURL, database string) (bool, error) {
	// Skip if Dolt database already exists
	if doltExists(doltDir) {
		return false, nil
	}

	if err := remotecache.ValidateRemoteURL(remoteURL); err != nil {
		return false, fmt.Errorf("invalid remote URL: %w", err)
	}

	if err := ValidateDatabaseName(database); err != nil {
		return false, fmt.Errorf("invalid database name %q (use cfg.GetDoltDatabase() to resolve the configured name): %w", database, err)
	}

	// Verify dolt CLI is available
	if _, err := exec.LookPath("dolt"); err != nil {
		return false, fmt.Errorf("dolt CLI not found (required for remote bootstrap): %w", err)
	}

	// Create the parent dolt directory
	if err := os.MkdirAll(doltDir, 0o750); err != nil {
		return false, fmt.Errorf("failed to create dolt directory: %w", err)
	}

	// Clone into <doltDir>/<database>/ so the embedded driver can find it.
	// `dolt clone <url> <target>` creates <target>/.dolt/ directly.
	cloneTarget := filepath.Join(doltDir, database)
	// Record whether the target already existed before this clone attempt.
	// If it did, the failed-clone cleanup below must never touch it: it
	// wasn't created by us, so it could be a pre-existing Dolt repo (e.g.
	// from an earlier bootstrap that a stale/empty doltExists() check
	// missed) that we must not delete.
	targetPreExisted := pathExists(cloneTarget)
	cmd := exec.CommandContext(ctx, "dolt", doltCloneArgs(remoteURL, cloneTarget)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		if targetPreExisted {
			return false, fmt.Errorf("dolt clone failed: %w\nOutput: %s\nClone target %q already existed before this attempt; left untouched to avoid deleting a pre-existing Dolt repo", err, output, cloneTarget)
		}
		cleaned, cleanupErr := removeFailedCloneTargetWithRetry(cloneTarget)
		return false, formatFailedCloneTargetError(err, output, cloneTarget, cleaned, cleanupErr)
	}

	fmt.Fprintf(os.Stderr, "Bootstrapped from remote: %s\n", remoteURL)
	return true, nil
}

var failedCloneCleanupRetryDelays = []time.Duration{
	50 * time.Millisecond,
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
}

func removeFailedCloneTargetWithRetry(path string) (bool, error) {
	info, err := os.Lstat(filepath.Join(path, ".dolt"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}

	for attempt := 0; ; attempt++ {
		err := os.RemoveAll(path)
		if err == nil || os.IsNotExist(err) {
			return true, nil
		}
		if attempt >= len(failedCloneCleanupRetryDelays) {
			return true, err
		}
		time.Sleep(failedCloneCleanupRetryDelays[attempt])
	}
}

func formatFailedCloneTargetError(cloneErr error, output []byte, cloneTarget string, cleaned bool, cleanupErr error) error {
	if cleanupErr == nil && cleaned {
		return fmt.Errorf("dolt clone failed: %w\nOutput: %s\nCleaned up failed clone target %q; fix the clone error above and retry `bd bootstrap`", cloneErr, output, cloneTarget)
	}
	if cleanupErr == nil {
		return fmt.Errorf("dolt clone failed: %w\nOutput: %s", cloneErr, output)
	}
	if !cleaned {
		return fmt.Errorf("dolt clone failed: %w\nOutput: %s\nCould not inspect failed clone target %q before cleanup: %v\nOn Windows this usually means a dolt or bd process, or antivirus scanner, still has a file handle open under `.dolt/noms/LOCK`. Stop stuck dolt/bd processes, wait a moment, delete the directory manually if it remains, then retry `bd bootstrap`", cloneErr, output, cloneTarget, cleanupErr)
	}
	return fmt.Errorf("dolt clone failed: %w\nOutput: %s\nCould not clean up failed clone target %q after retrying: %v\nOn Windows this usually means a dolt or bd process, or antivirus scanner, still has a file handle open under `.dolt/noms/LOCK`. Stop stuck dolt/bd processes, wait a moment, delete the directory manually if it remains, then retry `bd bootstrap`", cloneErr, output, cloneTarget, cleanupErr)
}

func doltCloneArgs(remoteURL, target string) []string {
	args := []string{"clone"}
	if user := os.Getenv("DOLT_REMOTE_USER"); user != "" {
		args = append(args, "--user", user)
	}
	return append(args, remoteURL, target)
}

// BootstrapFromGitRemoteWithDB is deprecated. Use BootstrapFromRemoteWithDB instead.
func BootstrapFromGitRemoteWithDB(ctx context.Context, doltDir, gitRemoteURL, database string) (bool, error) {
	return BootstrapFromRemoteWithDB(ctx, doltDir, gitRemoteURL, database)
}

// pathExists reports whether path exists (of any type), without following
// symlinks. Used to detect whether a clone target pre-existed before a
// clone attempt, so failed-clone cleanup never deletes something it didn't
// create.
func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
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
		dbName = configfile.DefaultDoltDatabase
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
