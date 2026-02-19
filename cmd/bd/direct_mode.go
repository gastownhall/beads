package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// ensureDirectMode makes sure the CLI is operating in direct-storage mode.
func ensureDirectMode(_ string) error {
	return ensureStoreActive()
}

// ensureStoreActive guarantees that a storage backend is initialized and tracked.
// Uses the factory to respect metadata.json backend configuration (SQLite, Dolt embedded, or Dolt server).
func ensureStoreActive() error {
	lockStore()
	active := isStoreActive() && getStore() != nil
	unlockStore()
	if active {
		return nil
	}

	// Find the .beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return fmt.Errorf("no beads database found.\n" +
			"Hint: run 'bd init' to create a database in the current directory,\n" +
			"      or use 'bd --no-db' for JSONL-only mode")
	}

	// Check if this is a JSONL-only project
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(jsonlPath); err == nil {
		// JSONL exists - check if no-db mode is configured
		if isNoDbModeConfigured(beadsDir) {
			return fmt.Errorf("this project uses JSONL-only mode (no SQLite database).\n" +
				"Hint: use 'bd --no-db <command>' or set 'no-db: true' in config.yaml")
		}
	}

	// Use dolt.NewFromConfig to create the appropriate backend
	// based on metadata.json configuration
	store, err := dolt.NewFromConfig(getRootContext(), beadsDir)
	if err != nil {
		// Check for fresh clone scenario (JSONL exists but no database)
		if _, statErr := os.Stat(jsonlPath); statErr == nil {
			return fmt.Errorf("found JSONL file but no database: %s\n"+
				"Hint: run 'bd init' to create the database and import issues,\n"+
				"      or use 'bd --no-db' for JSONL-only mode", jsonlPath)
		}
		return fmt.Errorf("failed to open database: %w", err)
	}

	// Update the database path for compatibility with code that expects it
	if dbPath := beads.FindDatabasePath(); dbPath != "" {
		setDBPath(dbPath)
	}

	lockStore()
	setStore(store)
	setStoreActive(true)
	unlockStore()

	return nil
}
