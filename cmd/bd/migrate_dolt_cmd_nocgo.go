//go:build !cgo

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var migrateDoltCmd = &cobra.Command{
	Use:   "dolt",
	Short: "Migrate from SQLite to Dolt backend",
	Long:  `Migrate the current beads installation from SQLite to Dolt backend. (Requires CGO)`,
	Run: func(cmd *cobra.Command, args []string) {
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
	},
}

func init() {
	migrateDoltCmd.Flags().Bool("dry-run", false, "Preview migration without making changes")
	migrateDoltCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt (for automation)")
	migrateCmd.AddCommand(migrateDoltCmd)
}
