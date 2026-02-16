//go:build !cgo

package main

import (
	"fmt"
	"os"
)

// handleToDoltMigration is a stub for non-cgo builds.
// Dolt requires CGO, so this migration is not available.
func handleToDoltMigration(dryRun bool, autoYes bool) {
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"error":   "dolt_not_available",
			"message": "Dolt backend requires CGO. This binary was built without CGO support.",
		})
	} else {
		fmt.Fprintf(os.Stderr, "Error: Dolt backend requires CGO\n")
		fmt.Fprintf(os.Stderr, "This binary was built without CGO support.\n")
		fmt.Fprintf(os.Stderr, "To use Dolt, rebuild with: CGO_ENABLED=1 go build\n")
	}
	os.Exit(1)
}

// handleToSQLiteMigration is a stub for non-cgo builds.
func handleToSQLiteMigration(dryRun bool, autoYes bool) {
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"error":   "sqlite_removed",
			"message": "SQLite backend has been removed; migration to SQLite is no longer supported.",
		})
	} else {
		fmt.Fprintf(os.Stderr, "Error: SQLite backend has been removed\n")
		fmt.Fprintf(os.Stderr, "Dolt is now the only storage backend.\n")
	}
	os.Exit(1)
}

// listMigrations returns an empty list (no Dolt without CGO).
func listMigrations() []string {
	return nil
}
