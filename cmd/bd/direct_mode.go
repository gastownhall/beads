package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/beads"
)

// ensureDirectMode makes sure the CLI is operating in direct-storage mode.
func ensureDirectMode(_ string) error {
	return ensureStoreActive()
}

// ensureStoreActive guarantees that a storage backend is initialized and tracked.
// Uses the factory to respect metadata.json backend configuration.
func ensureStoreActive() error {
	return ensureStoreActiveWithContext(getRootContext())
}

func ensureStoreActiveWithContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

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
			"Hint: run 'bd init' to create a database in the current directory")
	}

	// Use the factory to create the appropriate backend
	// based on metadata.json configuration and build tags
	store, err := newDoltStoreFromConfig(ctx, beadsDir)
	if err != nil {
		return fmt.Errorf("failed to open database: %w\nHint: %s", err, diagHint())
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
