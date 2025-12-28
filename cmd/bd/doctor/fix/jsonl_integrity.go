package fix

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/utils"
)

// JSONLIntegrity backs up a malformed JSONL export and regenerates it from the database.
// This is safe only when a database exists and is readable.
func JSONLIntegrity(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	beadsDir := filepath.Join(absPath, ".beads")

	// Resolve db path.
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return fmt.Errorf("cannot auto-repair JSONL: no database found")
	}

	// Resolve JSONL export path.
	jsonlPath := ""
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		if cfg.JSONLExport != "" && !isSystemJSONLFilename(cfg.JSONLExport) {
			p := cfg.JSONLPath(beadsDir)
			if _, err := os.Stat(p); err == nil {
				jsonlPath = p
			}
		}
	}
	if jsonlPath == "" {
		p := utils.FindJSONLInDir(beadsDir)
		if _, err := os.Stat(p); err == nil {
			jsonlPath = p
		}
	}
	if jsonlPath == "" {
		return fmt.Errorf("cannot auto-repair JSONL: no JSONL file found")
	}

	// Back up the JSONL.
	ts := time.Now().UTC().Format("20060102T150405Z")
	backup := jsonlPath + "." + ts + ".corrupt.backup.jsonl"
	if err := moveFile(jsonlPath, backup); err != nil {
		return fmt.Errorf("failed to back up JSONL: %w", err)
	}

	binary, err := getBdBinary()
	if err != nil {
		_ = moveFile(backup, jsonlPath)
		return err
	}

	// Re-export from DB.
	cmd := newBdCmd(binary, "--db", dbPath, "export", "-o", jsonlPath, "--force")
	cmd.Dir = absPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Best-effort rollback: restore the original JSONL, but keep the backup.
		failedTS := time.Now().UTC().Format("20060102T150405Z")
		if _, statErr := os.Stat(jsonlPath); statErr == nil {
			failed := jsonlPath + "." + failedTS + ".failed.regen.jsonl"
			_ = moveFile(jsonlPath, failed)
		}
		_ = copyFile(backup, jsonlPath)
		return fmt.Errorf("failed to regenerate JSONL from database: %w (backup: %s)", err, backup)
	}

	return nil
}
