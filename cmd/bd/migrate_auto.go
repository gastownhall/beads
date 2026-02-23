//go:build cgo

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// autoMigrateSQLiteToDolt finds the .beads directory and delegates to
// doAutoMigrateSQLiteToDolt for the actual migration logic.
func autoMigrateSQLiteToDolt() {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return
	}
	doAutoMigrateSQLiteToDolt(beadsDir)
}

// doAutoMigrateSQLiteToDolt detects a legacy SQLite beads.db in the given
// .beads directory and automatically migrates it to Dolt. This runs once,
// transparently, on the first bd command after upgrading to a Dolt-only CLI.
//
// The migration is best-effort: failures produce warnings, not fatal errors.
// After a successful migration, beads.db is renamed to beads.db.migrated.
//
// Edge cases handled:
//   - beads.db.migrated already exists → migration already completed, skip
//   - beads.db + dolt/ both exist → leftover SQLite, rename it
//   - Dolt directory already exists → no migration needed
//   - Corrupted SQLite → warn and skip
//   - Dolt server not running → warn and skip (retry on next command)
func doAutoMigrateSQLiteToDolt(beadsDir string) {
	// Check for SQLite database
	sqlitePath := findSQLiteDB(beadsDir)
	if sqlitePath == "" {
		return // No SQLite database, nothing to migrate
	}

	// Skip backup/migrated files
	base := filepath.Base(sqlitePath)
	if strings.Contains(base, ".backup") || strings.Contains(base, ".migrated") {
		return
	}

	// Check if Dolt already exists — if so, SQLite is leftover from a prior migration
	doltPath := filepath.Join(beadsDir, "dolt")
	if _, err := os.Stat(doltPath); err == nil {
		// Dolt exists alongside SQLite. Rename the leftover SQLite file.
		migratedPath := sqlitePath + ".migrated"
		if _, err := os.Stat(migratedPath); err != nil {
			// No .migrated file yet — rename now
			if err := os.Rename(sqlitePath, migratedPath); err == nil {
				debug.Logf("auto-migrate-sqlite: renamed leftover %s to %s", filepath.Base(sqlitePath), filepath.Base(migratedPath))
			}
		}
		return
	}

	ctx := context.Background()

	// Extract data from SQLite (read-only)
	fmt.Fprintf(os.Stderr, "Migrating SQLite database to Dolt...\n")
	data, err := extractFromSQLite(ctx, sqlitePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: SQLite auto-migration failed (extract): %v\n", err)
		fmt.Fprintf(os.Stderr, "Hint: run 'bd migrate dolt' manually, or remove %s to skip\n", base)
		return
	}

	if data.issueCount == 0 {
		debug.Logf("auto-migrate-sqlite: SQLite database is empty, skipping import")
	}

	// Determine database name from prefix
	dbName := "beads"
	if data.prefix != "" {
		dbName = "beads_" + data.prefix
	}

	// Load existing config for server connection settings
	doltCfg := &dolt.Config{
		Path:     doltPath,
		Database: dbName,
	}
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		doltCfg.ServerHost = cfg.GetDoltServerHost()
		doltCfg.ServerPort = cfg.GetDoltServerPort()
		doltCfg.ServerUser = cfg.GetDoltServerUser()
		doltCfg.ServerPassword = cfg.GetDoltServerPassword()
		doltCfg.ServerTLS = cfg.GetDoltServerTLS()
	}

	// Create Dolt store (connects to running dolt sql-server)
	doltStore, err := dolt.New(ctx, doltCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: SQLite auto-migration failed (dolt init): %v\n", err)
		fmt.Fprintf(os.Stderr, "Hint: ensure the Dolt server is running, then retry any bd command\n")
		return
	}

	// Import data
	imported, skipped, importErr := importToDolt(ctx, doltStore, data)
	if importErr != nil {
		_ = doltStore.Close()
		_ = os.RemoveAll(doltPath)
		fmt.Fprintf(os.Stderr, "Warning: SQLite auto-migration failed (import): %v\n", importErr)
		return
	}

	// Set sync mode
	if err := doltStore.SetConfig(ctx, "sync.mode", "dolt-native"); err != nil {
		debug.Logf("auto-migrate-sqlite: failed to set sync.mode: %v", err)
	}

	// Commit the migration
	commitMsg := fmt.Sprintf("Auto-migrate from SQLite: %d issues imported", imported)
	if err := doltStore.Commit(ctx, commitMsg); err != nil {
		debug.Logf("auto-migrate-sqlite: failed to create Dolt commit: %v", err)
	}

	_ = doltStore.Close()

	// Update metadata.json to point to Dolt
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	cfg.Backend = configfile.BackendDolt
	cfg.Database = "dolt"
	cfg.DoltDatabase = dbName
	if cfg.DoltServerPort == 0 {
		cfg.DoltServerPort = configfile.DefaultDoltServerPort
	}
	if err := cfg.Save(beadsDir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update metadata.json: %v\n", err)
	}

	// Write sync.mode to config.yaml
	if err := config.SaveConfigValue("sync.mode", string(config.SyncModeDoltNative), beadsDir); err != nil {
		debug.Logf("auto-migrate-sqlite: failed to write sync.mode to config.yaml: %v", err)
	}

	// Rename SQLite file to mark migration complete
	migratedPath := sqlitePath + ".migrated"
	if err := os.Rename(sqlitePath, migratedPath); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: migration succeeded but failed to rename %s: %v\n", base, err)
		fmt.Fprintf(os.Stderr, "Hint: manually rename or remove %s\n", sqlitePath)
	}

	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "Migrated %d issues from SQLite to Dolt (%d skipped)\n", imported, skipped)
	} else {
		fmt.Fprintf(os.Stderr, "Migrated %d issues from SQLite to Dolt\n", imported)
	}
}
