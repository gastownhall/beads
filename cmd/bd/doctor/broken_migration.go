package doctor

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/configfile"
)

// CheckBrokenMigrationState detects when metadata.json claims the backend is
// Dolt, but no Dolt database directory exists and no Dolt server is reachable.
// This happens when a migration fails partway or writes to the wrong server.
// Fixes GH#2016.
func CheckBrokenMigrationState(path string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Broken Migration State",
			Status:   StatusOK,
			Message:  "N/A (no .beads directory)",
			Category: CategoryMaintenance,
		}
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return DoctorCheck{
			Name:     "Broken Migration State",
			Status:   StatusOK,
			Message:  "N/A (no config)",
			Category: CategoryMaintenance,
		}
	}

	// Only relevant when backend claims to be Dolt
	if cfg.GetBackend() != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Broken Migration State",
			Status:   StatusOK,
			Message:  fmt.Sprintf("Backend is %q, not dolt", cfg.GetBackend()),
			Category: CategoryMaintenance,
		}
	}

	// In server mode, there is no local dolt/ directory — data lives on the
	// remote Dolt SQL server. This is expected and NOT a broken state.
	if cfg.IsDoltServerMode() {
		return DoctorCheck{
			Name:     "Broken Migration State",
			Status:   StatusOK,
			Message:  "Dolt server mode (no local dolt/ directory expected)",
			Category: CategoryMaintenance,
		}
	}

	// Check if dolt/ directory exists (required for embedded mode)
	doltDir := getDatabasePath(beadsDir)
	doltDirExists := false
	if _, err := os.Stat(doltDir); err == nil {
		doltDirExists = true
	}

	if doltDirExists {
		return DoctorCheck{
			Name:     "Broken Migration State",
			Status:   StatusOK,
			Message:  "Dolt directory exists",
			Category: CategoryMaintenance,
		}
	}

	// Backend says dolt but no dolt/ dir — check if a SQLite DB exists
	// that we could restore from
	sqliteExists := false
	for _, name := range []string{"beads.db", "beads.db.migrated"} {
		if _, err := os.Stat(filepath.Join(beadsDir, name)); err == nil {
			sqliteExists = true
			break
		}
	}
	// Also check for backups
	backupExists := false
	entries, _ := os.ReadDir(beadsDir)
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".db" {
			if len(e.Name()) > 10 && e.Name()[:6] == "beads." {
				backupExists = true
				break
			}
		}
	}

	detail := "metadata.json says backend=dolt but no dolt/ directory found."
	if sqliteExists {
		detail += " A SQLite database file exists and may contain your data."
	}
	if backupExists {
		detail += " Pre-migration backup file(s) found."
	}

	fix := "Run 'bd doctor --fix' to restore the SQLite backend in metadata.json"
	if !sqliteExists && !backupExists {
		fix = "No SQLite data found. Run 'bd init' to create a fresh database, or restore from git"
	}

	return DoctorCheck{
		Name:     "Broken Migration State",
		Status:   StatusError,
		Message:  "Dolt backend configured but no Dolt database found",
		Detail:   detail,
		Fix:      fix,
		Category: CategoryMaintenance,
	}
}

// CheckEmbeddedModeConcurrency detects when a project is using Dolt embedded
// mode but shows signs of concurrent access issues (lock files, multiple
// processes). Recommends switching to server mode for multi-agent workflows.
// Addresses GH#2086.
func CheckEmbeddedModeConcurrency(path string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return DoctorCheck{
			Name:     "Embedded Mode Concurrency",
			Status:   StatusOK,
			Message:  "N/A (no config)",
			Category: CategoryRuntime,
		}
	}

	// Only relevant for Dolt backend in embedded mode
	if cfg.GetBackend() != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Embedded Mode Concurrency",
			Status:   StatusOK,
			Message:  "N/A (not dolt backend)",
			Category: CategoryRuntime,
		}
	}

	if cfg.IsDoltServerMode() {
		return DoctorCheck{
			Name:     "Embedded Mode Concurrency",
			Status:   StatusOK,
			Message:  "Using server mode (multi-writer supported)",
			Category: CategoryRuntime,
		}
	}

	// In embedded mode — check for signs of concurrent access problems
	var issues []string

	// Check for dolt-access.lock (advisory lock from embedded mode)
	accessLockPath := filepath.Join(beadsDir, "dolt-access.lock")
	if _, err := os.Stat(accessLockPath); err == nil {
		issues = append(issues, "dolt-access.lock present (embedded mode advisory lock)")
	}

	// Check for noms LOCK files in dolt database directories
	doltDir := getDatabasePath(beadsDir)
	if entries, err := os.ReadDir(doltDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				nomsLock := filepath.Join(doltDir, entry.Name(), ".dolt", "noms", "LOCK")
				if _, err := os.Stat(nomsLock); err == nil {
					issues = append(issues, fmt.Sprintf("noms LOCK in %s (Dolt database lock)", entry.Name()))
				}
			}
		}
	}

	// Check for stale noms LOCK at the top level too
	topNomsDir := filepath.Join(doltDir, ".dolt", "noms")
	if _, err := os.Stat(filepath.Join(topNomsDir, "LOCK")); err == nil {
		issues = append(issues, "noms LOCK in dolt root (Dolt database lock)")
	}

	if len(issues) == 0 {
		return DoctorCheck{
			Name:     "Embedded Mode Concurrency",
			Status:   StatusOK,
			Message:  "No concurrent access issues detected",
			Category: CategoryRuntime,
		}
	}

	return DoctorCheck{
		Name:    "Embedded Mode Concurrency",
		Status:  StatusWarning,
		Message: fmt.Sprintf("Embedded mode with %d lock indicator(s) — concurrent access may cause failures", len(issues)),
		Detail: fmt.Sprintf("Detected: %s. Embedded mode is single-writer only. "+
			"If you run multiple bd processes (e.g., multiple Claude Code windows), "+
			"switch to server mode for reliable concurrent access.", joinIssues(issues)),
		Fix:      "Switch to server mode: bd dolt set mode server && bd dolt start",
		Category: CategoryRuntime,
	}
}

// joinIssues joins issue strings with semicolons.
func joinIssues(issues []string) string {
	result := ""
	for i, s := range issues {
		if i > 0 {
			result += "; "
		}
		result += s
	}
	return result
}

// CheckSQLiteResidue detects when a migration completed (backend=dolt) but
// the original SQLite file still exists with data. This is not critical
// but indicates the migration didn't fully clean up.
func CheckSQLiteResidue(path string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return DoctorCheck{
			Name:     "SQLite Residue",
			Status:   StatusOK,
			Message:  "N/A",
			Category: CategoryMaintenance,
		}
	}

	if cfg.GetBackend() != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "SQLite Residue",
			Status:   StatusOK,
			Message:  "N/A (not dolt backend)",
			Category: CategoryMaintenance,
		}
	}

	sqlitePath := filepath.Join(beadsDir, "beads.db")
	info, err := os.Stat(sqlitePath)
	if os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "SQLite Residue",
			Status:   StatusOK,
			Message:  "No leftover SQLite database",
			Category: CategoryMaintenance,
		}
	}

	if err != nil || info.Size() == 0 {
		return DoctorCheck{
			Name:     "SQLite Residue",
			Status:   StatusOK,
			Message:  "N/A",
			Category: CategoryMaintenance,
		}
	}

	return DoctorCheck{
		Name:     "SQLite Residue",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("beads.db still exists (%d bytes) after migration to Dolt", info.Size()),
		Detail:   "This file may be a leftover from migration. Verify your Dolt data is complete, then rename or remove it.",
		Fix:      "Rename beads.db to beads.db.migrated",
		Category: CategoryMaintenance,
	}
}
