package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage"
)

// ErrNoBeadsDatabase reports that no beads workspace could be resolved at all,
// as distinct from a workspace whose storage failed to open. Callers that must
// tell "nothing here" apart from "the store is broken" — `bd prime`'s memory
// injection, for one — match on it with errors.Is rather than on the message.
var ErrNoBeadsDatabase = errors.New("no beads database found")

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
	return ensureStoreActiveWith(ctx, newDoltStoreFromConfig)
}

// ensureReadOnlyStoreActiveWithContext is ensureStoreActiveWithContext with a
// store opened by newReadOnlyStoreFromConfig, whose open does not migrate or
// initialize the schema. On embedded Dolt the store is an OpenReadOnly store,
// which refuses every write (ErrReadOnly); it is recorded in
// writeRefusingLazyStore.
func ensureReadOnlyStoreActiveWithContext(ctx context.Context) error {
	return ensureStoreActiveWith(ctx, func(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
		st, err := newReadOnlyStoreFromConfig(ctx, beadsDir)
		if err == nil && isEmbeddedStore(st) {
			writeRefusingLazyStore = st
		}
		return st, err
	})
}

// writeRefusingLazyStore is the embedded store ensureReadOnlyStoreActiveWithContext
// last opened (bd prime's store). It refuses writes, so post-command steps that
// write must skip it rather than fail: see autoBackupBackendForCommand.
var writeRefusingLazyStore storage.DoltStorage

// activeStoreRefusesWrites reports whether st is the write-refusing store
// ensureReadOnlyStoreActiveWithContext opened.
func activeStoreRefusesWrites(st storage.DoltStorage) bool {
	return st != nil && writeRefusingLazyStore != nil && st == writeRefusingLazyStore
}

func ensureStoreActiveWith(ctx context.Context, open func(context.Context, string) (storage.DoltStorage, error)) error {
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
		return fmt.Errorf("%w.\n"+
			"Hint: run 'bd init' to create a database in the current directory", ErrNoBeadsDatabase)
	}

	// Use the factory to create the appropriate backend
	// based on metadata.json configuration and build tags
	store, err := open(ctx, beadsDir)
	if err != nil {
		return fmt.Errorf("%w\nHint: %s", openStoreError(backendNameForErrorFraming(beadsDir), err), diagHint())
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
