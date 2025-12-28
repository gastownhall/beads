package fix

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
)

// DatabaseIntegrity attempts to recover from database corruption by:
//  1. Backing up the corrupt database (and WAL/SHM if present)
//  2. Re-initializing the database from the working tree JSONL export
//
// This is intentionally conservative: it will not delete JSONL, and it preserves the
// original DB as a backup for forensic recovery.
func DatabaseIntegrity(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	beadsDir := filepath.Join(absPath, ".beads")

	// Best-effort: stop any running daemon to reduce the chance of DB file locks.
	_ = Daemon(absPath)

	// Resolve database path (respects metadata.json database override).
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	// Find JSONL source of truth.
	jsonlPath := ""
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		if cfg.JSONLExport != "" && !isSystemJSONLFilename(cfg.JSONLExport) {
			candidate := cfg.JSONLPath(beadsDir)
			if _, err := os.Stat(candidate); err == nil {
				jsonlPath = candidate
			}
		}
	}
	if jsonlPath == "" {
		for _, name := range []string{"issues.jsonl", "beads.jsonl"} {
			candidate := filepath.Join(beadsDir, name)
			if _, err := os.Stat(candidate); err == nil {
				jsonlPath = candidate
				break
			}
		}
	}
	if jsonlPath == "" {
		return fmt.Errorf("cannot auto-recover: no JSONL export found in %s", beadsDir)
	}

	// Back up corrupt DB and its sidecar files.
	ts := time.Now().UTC().Format("20060102T150405Z")
	backupDB := dbPath + "." + ts + ".corrupt.backup.db"
	if err := moveFile(dbPath, backupDB); err != nil {
		// Retry once after attempting to kill daemons again (helps on platforms with strict file locks).
		_ = Daemon(absPath)
		if err2 := moveFile(dbPath, backupDB); err2 != nil {
			// Prefer the original error (more likely root cause).
			return fmt.Errorf("failed to back up database: %w", err)
		}
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		sidecar := dbPath + suffix
		if _, err := os.Stat(sidecar); err == nil {
			_ = moveFile(sidecar, backupDB+suffix) // best effort
		}
	}

	// Rebuild by importing from the working tree JSONL into a fresh database.
	bdBinary, err := getBdBinary()
	if err != nil {
		return err
	}

	// Use import (not init) so we always hydrate from the working tree JSONL, not git-tracked blobs.
	args := []string{"--db", dbPath, "import", "-i", jsonlPath, "--force", "--no-git-history"}
	cmd := newBdCmd(bdBinary, args...)
	cmd.Dir = absPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		// Best-effort rollback: attempt to restore the original DB, while preserving the backup.
		failedTS := time.Now().UTC().Format("20060102T150405Z")
		if _, statErr := os.Stat(dbPath); statErr == nil {
			failedDB := dbPath + "." + failedTS + ".failed.init.db"
			_ = moveFile(dbPath, failedDB)
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				_ = moveFile(dbPath+suffix, failedDB+suffix)
			}
		}
		_ = copyFile(backupDB, dbPath)
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, statErr := os.Stat(backupDB + suffix); statErr == nil {
				_ = copyFile(backupDB+suffix, dbPath+suffix)
			}
		}
		return fmt.Errorf("failed to rebuild database from JSONL: %w (backup: %s)", err, backupDB)
	}

	return nil
}
