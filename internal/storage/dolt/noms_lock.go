package dolt

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// CleanStaleNomsLocks removes stale Dolt noms LOCK files from all databases
// in the given dolt directory (typically .beads/dolt/).
//
// Dolt's noms storage layer creates a file-based LOCK at
// <db>/.dolt/noms/LOCK when opening a database. If the process is killed
// uncleanly (SIGKILL, OOM, etc.), this LOCK file persists and prevents the
// Dolt server from opening the database on restart — causing either a SIGSEGV
// (nil pointer dereference in DoltDB.SetCrashOnFatalError) or a "database is
// locked" error.
//
// LOCK files can appear at multiple nesting levels, including stats databases:
//   - <db>/.dolt/noms/LOCK
//   - <db>/.dolt/stats/.dolt/noms/LOCK
//   - .dolt/noms/LOCK (root-level)
//
// This function uses filepath.WalkDir to recursively find and remove all files
// named "LOCK" inside "noms" directories under doltDir.
//
// This function is safe to call before starting or connecting to a Dolt server
// because the server is not yet using the databases.
//
// Returns the number of lock files removed. Errors removing individual files
// are collected but do not abort the scan.
func CleanStaleNomsLocks(doltDir string) (removed int, errs []error) {
	// Quick check: if directory doesn't exist, nothing to clean.
	if _, err := os.Stat(doltDir); err != nil {
		return 0, nil
	}

	walkErr := filepath.WalkDir(doltDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			return nil
		}
		// Match files named "LOCK" inside a "noms" directory.
		if d.Name() == "LOCK" && filepath.Base(filepath.Dir(path)) == "noms" {
			if rmErr := os.Remove(path); rmErr != nil {
				errs = append(errs, fmt.Errorf("removing %s: %w", path, rmErr))
			} else {
				removed++
			}
		}
		return nil
	})
	if walkErr != nil {
		errs = append(errs, fmt.Errorf("walking %s: %w", doltDir, walkErr))
	}

	return removed, errs
}
