package fix

import (
	"errors"
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

// integrityBdBinary finds the bd binary integrity recovery re-runs; tests
// replace it to drive the recovery's failure paths.
var integrityBdBinary = getBdBinary

// doltIntegrityRecovery backs up the corrupted Dolt database and reinitializes.
func doltIntegrityRecovery(path, beadsDir string) error {
	if err := serverModeIntegrityRecoveryGuard(beadsDir); err != nil {
		return err
	}

	doltPath, err := confinedDatabasePath(beadsDir, getDatabasePath(beadsDir))
	if err != nil {
		return err
	}

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
	bdBinary, err := integrityBdBinary()
	if err != nil {
		// Restore corrupted database on failure
		if cerr := revalidateIntegrityRestore(beadsDir, doltPath, backupPath); cerr != nil {
			return fmt.Errorf("%w\n%w", err, cerr)
		}
		_ = os.Rename(backupPath, doltPath)
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
		if cerr := revalidateIntegrityRestore(beadsDir, doltPath, backupPath); cerr != nil {
			return fmt.Errorf("failed to reinitialize Dolt database: %w\n%w", err, cerr)
		}
		fmt.Printf("  Warning: recovery failed, restoring corrupted Dolt database from %s\n", filepath.Base(backupPath))
		_ = os.RemoveAll(doltPath)
		_ = os.Rename(backupPath, doltPath)
		return fmt.Errorf("failed to reinitialize Dolt database: %w", err)
	}

	fmt.Printf("  Recovered Dolt database\n")
	fmt.Printf("  Corrupted database preserved at: %s\n", filepath.Base(backupPath))
	return nil
}

// revalidateIntegrityRestore re-admits doltPath before a failed recovery
// removes what bd init left there and moves the backup back: the location
// may have changed during recovery. On refusal the backup is left alone and
// the error names the path it was written to.
func revalidateIntegrityRestore(beadsDir, doltPath, backupPath string) error {
	_, err := confinedDatabasePath(beadsDir, doltPath)
	if err == nil {
		return nil
	}
	// Report only the reason: the admission remedy, selecting the location
	// through BEADS_DOLT_DATA_DIR, would exempt the changed location.
	var refusal *databaseRefusal
	if errors.As(err, &refusal) {
		err = errors.New(refusal.why)
	}
	return fmt.Errorf("not restoring the corrupted Dolt database: its location %s changed during recovery: %w\n"+
		"Check what is there now, then move the backup back by hand. It was written to %s; "+
		"if a directory above the database was replaced, the backup moved with that directory",
		remedy.PrintedValue(doltPath), err, remedy.PrintedValue(backupPath))
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
