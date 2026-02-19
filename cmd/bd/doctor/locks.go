package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

)

// staleLockThresholds defines the age thresholds for each lock type.
// Lock files older than these thresholds are considered stale.
var staleLockThresholds = map[string]time.Duration{
	"bootstrap.lock":   5 * time.Minute, // Bootstrap should complete quickly
	".sync.lock":       1 * time.Hour,   // Sync can be slow for large repos
	"dolt-access.lock": 5 * time.Minute, // Embedded dolt advisory lock
}

// CheckStaleLockFiles detects leftover lock files from crashed processes.
// Stale lock files can block bootstrap and sync operations.
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

	// Check dolt-access.lock (embedded dolt advisory flock)
	accessLockPath := filepath.Join(beadsDir, "dolt-access.lock")
	if info, err := os.Stat(accessLockPath); err == nil {
		age := time.Since(info.ModTime())
		if age > staleLockThresholds["dolt-access.lock"] {
			staleFiles = append(staleFiles, "dolt-access.lock")
			details = append(details, fmt.Sprintf("dolt-access.lock: age %s (threshold: %s)",
				age.Round(time.Second), staleLockThresholds["dolt-access.lock"]))
		}
	}

	// Check Dolt internal LOCK files (noms layer filesystem lock).
	// These live at .beads/dolt/<database>/.dolt/noms/LOCK and are created
	// by the embedded Dolt engine. If a process crashes without closing
	// the embedded connector, the LOCK file persists and blocks future opens.
	doltDir := filepath.Join(beadsDir, "dolt")
	if dbEntries, err := os.ReadDir(doltDir); err == nil {
		for _, dbEntry := range dbEntries {
			if !dbEntry.IsDir() {
				continue
			}
			nomsLock := filepath.Join(doltDir, dbEntry.Name(), ".dolt", "noms", "LOCK")
			if info, err := os.Stat(nomsLock); err == nil {
				age := time.Since(info.ModTime())
				if age > 5*time.Minute {
					lockName := fmt.Sprintf("dolt/%s/.dolt/noms/LOCK", dbEntry.Name())
					staleFiles = append(staleFiles, lockName)
					details = append(details, fmt.Sprintf("%s: age %s (Dolt internal lock from crashed process)",
						lockName, age.Round(time.Second)))
				}
			}
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
