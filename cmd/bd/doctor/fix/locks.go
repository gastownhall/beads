package fix

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// StaleLockFiles removes stale lock files from the .beads directory.
// This is safe because:
// - Bootstrap/sync/startup locks use flock, which is released on process exit
// - If the flock is released but the file remains, the file is just clutter
func StaleLockFiles(path string) error {
	beadsDir := filepath.Join(path, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return nil
	}

	var removed []string
	var errors []string

	// Remove stale bootstrap lock
	bootstrapLockPath := filepath.Join(beadsDir, "dolt.bootstrap.lock")
	if info, err := os.Stat(bootstrapLockPath); err == nil {
		age := time.Since(info.ModTime())
		if age > 5*time.Minute {
			if err := os.Remove(bootstrapLockPath); err != nil {
				errors = append(errors, fmt.Sprintf("dolt.bootstrap.lock: %v", err))
			} else {
				removed = append(removed, "dolt.bootstrap.lock")
			}
		}
	}

	// Remove stale sync lock
	syncLockPath := filepath.Join(beadsDir, ".sync.lock")
	if info, err := os.Stat(syncLockPath); err == nil {
		age := time.Since(info.ModTime())
		if age > 1*time.Hour {
			if err := os.Remove(syncLockPath); err != nil {
				errors = append(errors, fmt.Sprintf(".sync.lock: %v", err))
			} else {
				removed = append(removed, ".sync.lock")
			}
		}
	}

	// Remove stale dolt-access.lock (embedded dolt advisory flock).
	// This lock uses flock which is released on process exit, but the file
	// persists and can confuse diagnostics or cause issues if flock behavior
	// varies across platforms.
	accessLockPath := filepath.Join(beadsDir, "dolt-access.lock")
	if info, err := os.Stat(accessLockPath); err == nil {
		age := time.Since(info.ModTime())
		if age > 5*time.Minute {
			if err := os.Remove(accessLockPath); err != nil {
				errors = append(errors, fmt.Sprintf("dolt-access.lock: %v", err))
			} else {
				removed = append(removed, "dolt-access.lock")
			}
		}
	}

	// Remove stale Dolt noms LOCK files via shared helper.
	// Same cleanup that runs pre-flight in PersistentPreRun.
	doltDir := getDatabasePath(beadsDir)
	if n, errs := dolt.CleanStaleNomsLocks(doltDir); n > 0 {
		removed = append(removed, fmt.Sprintf("%d noms LOCK file(s)", n))
		for _, e := range errs {
			errors = append(errors, e.Error())
		}
	}

	// Remove stale startup locks
	entries, err := os.ReadDir(beadsDir)
	if err == nil {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".startlock") {
				info, err := entry.Info()
				if err != nil {
					continue
				}
				age := time.Since(info.ModTime())
				if age > 30*time.Second {
					lockPath := filepath.Join(beadsDir, entry.Name())
					if err := os.Remove(lockPath); err != nil {
						errors = append(errors, fmt.Sprintf("%s: %v", entry.Name(), err))
					} else {
						removed = append(removed, entry.Name())
					}
				}
			}
		}
	}

	if len(removed) > 0 {
		fmt.Printf("  Removed stale lock files: %s\n", strings.Join(removed, ", "))
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to remove some lock files: %s", strings.Join(errors, "; "))
	}

	return nil
}
