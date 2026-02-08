package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
)

// staleLockThresholds defines the age thresholds for each lock type.
// Lock files older than these thresholds are considered stale.
var staleLockThresholds = map[string]time.Duration{
	"bootstrap.lock": 5 * time.Minute, // Bootstrap should complete quickly
	".sync.lock":     1 * time.Hour,   // Sync can be slow for large repos
	"daemon.lock":    0,               // Handled separately via flock check
}

// CheckStaleLockFiles detects leftover lock files from crashed processes.
// Stale lock files can block bootstrap, sync, and daemon operations.
func CheckStaleLockFiles(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Lock Files",
			Status:   StatusOK,
			Message:  "N/A (no .beads directory)",
			Category: CategoryRuntime,
		}
	}

	var staleFiles []string
	var details []string

	// Check bootstrap lock (dolt.bootstrap.lock)
	bootstrapLockPath := filepath.Join(beadsDir, "dolt.bootstrap.lock")
	if info, err := os.Stat(bootstrapLockPath); err == nil {
		age := time.Since(info.ModTime())
		if age > staleLockThresholds["bootstrap.lock"] {
			staleFiles = append(staleFiles, "dolt.bootstrap.lock")
			details = append(details, fmt.Sprintf("dolt.bootstrap.lock: age %s (threshold: %s)",
				age.Round(time.Second), staleLockThresholds["bootstrap.lock"]))
		}
	}

	// Check sync lock (.sync.lock)
	syncLockPath := filepath.Join(beadsDir, ".sync.lock")
	if info, err := os.Stat(syncLockPath); err == nil {
		age := time.Since(info.ModTime())
		if age > staleLockThresholds[".sync.lock"] {
			staleFiles = append(staleFiles, ".sync.lock")
			details = append(details, fmt.Sprintf(".sync.lock: age %s (threshold: %s)",
				age.Round(time.Second), staleLockThresholds[".sync.lock"]))
		}
	}

	// Check daemon lock - use flock probe instead of age
	daemonLockPath := filepath.Join(beadsDir, "daemon.lock")
	if _, err := os.Stat(daemonLockPath); err == nil {
		// Check if daemon is actually running via flock
		running, _ := lockfile.TryDaemonLock(beadsDir)
		if !running {
			staleFiles = append(staleFiles, "daemon.lock")
			details = append(details, "daemon.lock: file exists but no daemon process holds the lock")
		}
	}

	// Check startup lock (bd.sock.startlock)
	// Look for any .startlock files in beadsDir
	entries, err := os.ReadDir(beadsDir)
	if err == nil {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".startlock") {
				info, err := entry.Info()
				if err != nil {
					continue
				}
				age := time.Since(info.ModTime())
				// Startup locks should be very short-lived (< 30 seconds)
				if age > 30*time.Second {
					staleFiles = append(staleFiles, entry.Name())
					details = append(details, fmt.Sprintf("%s: age %s (startup locks should be < 30s)",
						entry.Name(), age.Round(time.Second)))
				}
			}
		}
	}

	if len(staleFiles) == 0 {
		return DoctorCheck{
			Name:     "Lock Files",
			Status:   StatusOK,
			Message:  "No stale lock files",
			Category: CategoryRuntime,
		}
	}

	return DoctorCheck{
		Name:     "Lock Files",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d stale lock file(s): %s", len(staleFiles), strings.Join(staleFiles, ", ")),
		Detail:   strings.Join(details, "; "),
		Fix:      "Run 'bd doctor --fix' to remove stale lock files, or delete manually from .beads/",
		Category: CategoryRuntime,
	}
}
