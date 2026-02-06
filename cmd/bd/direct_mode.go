package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/syncbranch"
)

// ensureDirectMode makes sure the CLI is operating in direct-storage mode.
// If the daemon is active, it is cleanly disconnected and the shared store is opened.
func ensureDirectMode(reason string) error {
	if getDaemonClient() != nil {
		if err := fallbackToDirectMode(reason); err != nil {
			return err
		}
		return nil
	}
	return ensureStoreActive()
}

// fallbackToDirectMode disables the daemon client and ensures a local store is ready.
func fallbackToDirectMode(reason string) error {
	disableDaemonForFallback(reason)
	return ensureStoreActive()
}

// disableDaemonForFallback closes the daemon client and updates status metadata.
func disableDaemonForFallback(reason string) {
	if client := getDaemonClient(); client != nil {
		_ = client.Close()
		setDaemonClient(nil)
	}

	ds := getDaemonStatus()
	ds.Mode = "direct"
	ds.Connected = false
	ds.Degraded = true
	if reason != "" {
		ds.Detail = reason
	}
	if ds.FallbackReason == FallbackNone {
		ds.FallbackReason = FallbackDaemonUnsupported
	}
	setDaemonStatus(ds)

	if reason != "" {
		debug.Logf("Debug: %s\n", reason)
	}
}

// ensureStoreActive guarantees that a storage backend is initialized and tracked.
// Uses the factory to respect metadata.json backend configuration (SQLite or Dolt server).
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

	// GH#1349: Ensure sync branch worktree exists if configured.
	// This must happen before any JSONL operations to fix fresh clone scenario
	// where findJSONLPath would otherwise fall back to main's stale JSONL.
	if _, err := syncbranch.EnsureWorktree(context.Background()); err != nil {
		// Log warning but don't fail - operations can still work with main's JSONL
		// This allows graceful degradation if worktree creation fails
		debug.Logf("Warning: could not ensure sync worktree: %v", err)
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

	// Use factory to create the appropriate backend (SQLite or Dolt server)
	// based on metadata.json configuration
	store, err := factory.NewFromConfig(getRootContext(), beadsDir)
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

	if isAutoImportEnabled() && ShouldImportJSONL(rootCtx, store) {
		autoImportIfNewer()
	}

	return nil
}
