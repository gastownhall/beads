package fix

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/configfile"
)

// BrokenMigrationState restores the SQLite backend in metadata.json when the
// Dolt migration failed and left the database in an inconsistent state.
//
// Pre-conditions (re-verified before applying):
//   - metadata.json has backend=dolt
//   - No dolt/ directory exists
//   - A SQLite database file (beads.db or beads.db.migrated) exists
//
// Fixes GH#2016.
func BrokenMigrationState(path string) error {
	beadsDir := filepath.Join(path, ".beads")

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return fmt.Errorf("cannot load metadata.json: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("metadata.json not found in %s", beadsDir)
	}

	// Re-verify conditions before applying fix
	if cfg.GetBackend() != configfile.BackendDolt {
		return fmt.Errorf("backend is %q, not dolt — fix not applicable", cfg.GetBackend())
	}

	doltDir := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltDir); err == nil {
		return fmt.Errorf("dolt/ directory exists — backend may be valid, not fixing")
	}

	// Check for SQLite data to restore
	sqlitePath := filepath.Join(beadsDir, "beads.db")
	migratedPath := filepath.Join(beadsDir, "beads.db.migrated")

	hasSQLite := false
	if _, err := os.Stat(sqlitePath); err == nil {
		hasSQLite = true
	}

	// If beads.db.migrated exists but beads.db doesn't, rename it back
	if !hasSQLite {
		if _, err := os.Stat(migratedPath); err == nil {
			if err := os.Rename(migratedPath, sqlitePath); err != nil {
				return fmt.Errorf("failed to restore beads.db from .migrated: %w", err)
			}
			fmt.Fprintf(os.Stderr, "  Restored beads.db from beads.db.migrated\n")
			hasSQLite = true
		}
	}

	if !hasSQLite {
		return fmt.Errorf("no SQLite database found to restore — run 'bd init' for fresh database")
	}

	// Update metadata.json to point back to SQLite
	cfg.Backend = configfile.BackendSQLite
	cfg.Database = "beads.db"
	if err := cfg.Save(beadsDir); err != nil {
		return fmt.Errorf("failed to update metadata.json: %w", err)
	}

	fmt.Fprintf(os.Stderr, "  Restored backend to SQLite in metadata.json\n")
	fmt.Fprintf(os.Stderr, "  Run 'bd migrate --to-dolt' when ready to migrate\n")
	return nil
}

// SQLiteResidue renames a leftover beads.db to beads.db.migrated after
// verifying the Dolt backend is active. Guards against accidentally
// renaming the active SQLite database on a SQLite-backend project.
func SQLiteResidue(path string) error {
	beadsDir := filepath.Join(path, ".beads")

	// Safety guard: only rename if backend is Dolt
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return fmt.Errorf("cannot load metadata.json: cannot verify backend")
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		return fmt.Errorf("backend is %q, not dolt — refusing to rename beads.db", cfg.GetBackend())
	}

	sqlitePath := filepath.Join(beadsDir, "beads.db")
	migratedPath := sqlitePath + ".migrated"

	if _, err := os.Stat(sqlitePath); os.IsNotExist(err) {
		return fmt.Errorf("beads.db not found — nothing to clean up")
	}

	// Don't overwrite an existing .migrated file from a previous migration attempt
	if _, err := os.Stat(migratedPath); err == nil {
		return fmt.Errorf("beads.db.migrated already exists — remove or move it first, then re-run 'bd doctor --fix'")
	}

	if err := os.Rename(sqlitePath, migratedPath); err != nil {
		return fmt.Errorf("failed to rename beads.db: %w", err)
	}

	fmt.Fprintf(os.Stderr, "  Renamed beads.db to beads.db.migrated\n")
	return nil
}
