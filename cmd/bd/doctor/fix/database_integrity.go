package fix

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
)

// DatabaseIntegrity attempts to recover from database corruption by:
//  1. Backing up the corrupt database
//  2. Re-initializing via bd init (which will clone from remote if configured)
//
// For Dolt backends: backs up .beads/dolt and reinitializes via bd init --force.
// For SQLite backends: backs up the .db file and reinitializes via bd init.
func DatabaseIntegrity(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	beadsDir := filepath.Join(absPath, ".beads")

	// Dolt backend: backup and reinitialize
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.GetBackend() == configfile.BackendDolt {
		return doltIntegrityRecovery(absPath, beadsDir)
	}

	// SQLite backend: backup and reinitialize
	return sqliteIntegrityRecovery(absPath, beadsDir)
}

// doltIntegrityRecovery backs up the corrupted Dolt database and reinitializes.
func doltIntegrityRecovery(path, beadsDir string) error {
	doltPath := getDatabasePath(beadsDir)

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
		// Restore corrupted database on failure
		_ = os.Rename(backupPath, doltPath)
		return err
	}

	// Reinitialize: bd init --force -q
	// bd init will clone from remote if sync.git-remote is configured.
	fmt.Printf("  Reinitializing Dolt database (will clone from remote if configured)\n")
	initCmd := newBdCmd(bdBinary, "init", "--force", "-q", "--skip-hooks")
	initCmd.Dir = path
	initCmd.Stdout = os.Stdout
	initCmd.Stderr = os.Stderr

	if err := initCmd.Run(); err != nil {
		// Restore backup on failure
		fmt.Printf("  Warning: recovery failed, restoring corrupted Dolt database from %s\n", filepath.Base(backupPath))
		_ = os.RemoveAll(doltPath)
		_ = os.Rename(backupPath, doltPath)
		return fmt.Errorf("failed to reinitialize Dolt database: %w", err)
	}

	fmt.Printf("  Recovered Dolt database\n")
	fmt.Printf("  Corrupted database preserved at: %s\n", filepath.Base(backupPath))
	return nil
}

// sqliteIntegrityRecovery backs up the corrupted SQLite database and reinitializes.
func sqliteIntegrityRecovery(path, beadsDir string) error {
	// Resolve database path
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, "beads.db")
	}

	// Check if database exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return fmt.Errorf("no database to recover at %s", dbPath)
	}

	// Back up corrupt DB and its sidecar files.
	ts := time.Now().UTC().Format("20060102T150405Z")
	backupDB := dbPath + "." + ts + ".corrupt.backup.db"
	if err := moveFile(dbPath, backupDB); err != nil {
		return fmt.Errorf("failed to back up database: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		sidecar := dbPath + suffix
		if _, err := os.Stat(sidecar); err == nil {
			_ = moveFile(sidecar, backupDB+suffix)
		}
	}

	// Reinitialize via bd init
	bdBinary, err := getBdBinary()
	if err != nil {
		return err
	}

	fmt.Printf("  Reinitializing database\n")
	initCmd := newBdCmd(bdBinary, "init", "--force", "-q", "--skip-hooks")
	initCmd.Dir = path
	initCmd.Stdout = os.Stdout
	initCmd.Stderr = os.Stderr

	if err := initCmd.Run(); err != nil {
		// Best-effort rollback
		_ = copyFile(backupDB, dbPath)
		return fmt.Errorf("failed to reinitialize database: %w (backup: %s)", err, backupDB)
	}

	return nil
}
