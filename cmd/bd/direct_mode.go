package main

import (
	"fmt"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

// ensureDirectMode makes sure the CLI is operating in direct-storage mode.
// If the daemon is active, it is cleanly disconnected and the shared store is opened.
func ensureDirectMode(reason string) error {
	if daemonClient != nil {
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
	if daemonClient != nil {
		_ = daemonClient.Close()
		daemonClient = nil
	}

	daemonStatus.Mode = "direct"
	daemonStatus.Connected = false
	daemonStatus.Degraded = true
	if reason != "" {
		daemonStatus.Detail = reason
	}
	if daemonStatus.FallbackReason == FallbackNone {
		daemonStatus.FallbackReason = FallbackDaemonUnsupported
	}

	if reason != "" {
		debug.Logf("Debug: %s\n", reason)
	}
}

// ensureStoreActive guarantees that a local SQLite store is initialized and tracked.
func ensureStoreActive() error {
	storeMutex.Lock()
	active := storeActive && store != nil
	storeMutex.Unlock()
	if active {
		return nil
	}

	if dbPath == "" {
		if found := beads.FindDatabasePath(); found != "" {
			dbPath = found
		} else {
			return fmt.Errorf("no beads database found. Hint: run 'bd init' in this directory")
		}
	}

	sqlStore, err := sqlite.New(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	storeMutex.Lock()
	store = sqlStore
	storeActive = true
	storeMutex.Unlock()

	checkVersionMismatch()
	if autoImportEnabled {
		autoImportIfNewer()
	}

	return nil
}
