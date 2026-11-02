package fix

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
)

// DatabaseIntegrity attempts to recover from database corruption by:
//  1. Backing up the corrupt database
//  2. Re-initializing via bd init (which will clone from remote if configured)
func DatabaseIntegrity(path string) error {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	return doltIntegrityRecovery(absPath, beadsDir)
}

// doltIntegrityRecovery backs up the corrupted Dolt database and reinitializes.
func doltIntegrityRecovery(path, beadsDir string) error {
	if err := serverModeIntegrityRecoveryGuard(beadsDir); err != nil {
		return err
	}

	doltPath := getDatabasePath(beadsDir)

	// Confinement before anything destructive: the metadata this repair reads
	// accepts relative or absolute dolt_data_dir values and an absolute
	// database (Config.Save strips only the first), so an unvalidated
	// selection could point the rename below — and the rollback removal — at
	// data outside this workspace. The check resolves the workspace root
	// itself through a supported symlink and honors an explicitly env-selected
	// data directory; every metadata-selected escape is refused here, before
	// the backup rename and before the bd binary lookup.
	confinedPath, confErr := confinedDatabasePath(beadsDir, doltPath)
	if confErr != nil {
		return fmt.Errorf("refusing integrity recovery: %w", confErr)
	}
	doltPath = confinedPath

	// Check if dolt directory exists
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return fmt.Errorf("no Dolt database to recover at %s", doltPath)
	}

	// Back up corrupted dolt directory
	ts := time.Now().UTC().Format("20060102T150405Z")
	backupPath := doltPath + "." + ts + ".corrupt.backup"
	fmt.Printf("  Backing up corrupted Dolt database to %s\n", filepath.Base(backupPath))
	if err := os.Rename(doltPath, backupPath); err != nil {
		return fmt.Errorf("failed to backup corrupted Dolt database: %w", err)
	}

	// Get bd binary path
	bdBinary, err := getBdBinary()
	if err != nil {
		// Restore corrupted database on failure. A rollback refusal keeps the
		// original failure (the error below still wraps it) instead of
		// reporting success while the backup stays behind.
		if rbErr := restoreIntegrityBackup(beadsDir, doltPath, backupPath); rbErr != nil {
			return fmt.Errorf("%w; rollback refused: %v", err, rbErr)
		}
		return err
	}

	// Reinitialize: bd init --force -q
	// bd init will clone from remote if sync.remote is configured.
	fmt.Printf("  Reinitializing Dolt database (will clone from remote if configured)\n")
	initCmd := newBdCmd(bdBinary, "init", "--force", "-q", "--skip-hooks")
	initCmd.Dir = path
	initCmd.Stdout = os.Stdout
	initCmd.Stderr = os.Stderr

	if err := initCmd.Run(); err != nil {
		// Restore backup on failure
		fmt.Printf("  Warning: recovery failed, restoring corrupted Dolt database from %s\n", filepath.Base(backupPath))
		if rbErr := restoreIntegrityBackup(beadsDir, doltPath, backupPath); rbErr != nil {
			return fmt.Errorf("failed to reinitialize Dolt database: %w; rollback refused: %v", err, rbErr)
		}
		return fmt.Errorf("failed to reinitialize Dolt database: %w", err)
	}

	fmt.Printf("  Recovered Dolt database\n")
	fmt.Printf("  Corrupted database preserved at: %s\n", filepath.Base(backupPath))
	return nil
}

// restoreIntegrityBackup rolls back a failed integrity recovery: it removes
// the database the failed reinitialization left at the destination and
// restores the corrupt backup over it. The destination is revalidated first
// — the reinitialization ran a subprocess in the workspace, so the entry
// that was confined before the backup rename may since have been replaced
// (e.g. with a symlink to unrelated data); a refusal removes and restores
// nothing, preserves the backup, and reports its location.
func restoreIntegrityBackup(beadsDir, doltPath, backupPath string) error {
	confinedPath, err := confinedDatabasePath(beadsDir, doltPath)
	if err != nil {
		return fmt.Errorf(
			"refusing to restore through %s: %w; the corrupted database is preserved at %s",
			remedy.PrintedValue(doltPath), err, remedy.PrintedValue(backupPath))
	}
	_ = os.RemoveAll(confinedPath)
	_ = os.Rename(backupPath, confinedPath)
	return nil
}

func serverModeIntegrityRecoveryGuard(beadsDir string) error {
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil || !cfg.IsDoltServerMode() {
		return nil
	}

	dbName := cfg.GetDoltDatabase()
	if dbName == "" {
		dbName = configfile.DefaultDoltDatabase
	}

	return fmt.Errorf(
		"automatic integrity recovery is disabled for server-mode repos because it can replace the wrong Dolt root; preserve %s and verify the configured database %q manually before reinitializing",
		getDatabasePath(beadsDir),
		dbName,
	)
}
