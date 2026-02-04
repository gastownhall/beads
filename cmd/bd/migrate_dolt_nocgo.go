//go:build !cgo || windows
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
// Dolt requires CGO, so this migration from Dolt is not available.
func handleToSQLiteMigration(dryRun bool, autoYes bool) {
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

