//go:build cgo

package main

import (
	"github.com/spf13/cobra"
)

var migrateDoltCmd = &cobra.Command{
	Use:   "dolt",
	Short: "Migrate from SQLite to Dolt backend",
	Long: `Migrate the current beads installation from SQLite to Dolt backend.

This command:
  1. Creates a backup of the SQLite database
  2. Creates a Dolt database in .beads/dolt/
  3. Imports all issues, labels, dependencies, and events
  4. Copies all config values
  5. Updates metadata.json to use Dolt backend

The original SQLite database is preserved (can be deleted after verification).

Examples:
  bd migrate dolt            # Interactive migration with confirmation
  bd migrate dolt --dry-run  # Preview what would happen
  bd migrate dolt --yes      # Non-interactive (for automation)`,
	Run: func(cmd *cobra.Command, args []string) {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		autoYes, _ := cmd.Flags().GetBool("yes")

		// Block writes in readonly mode (migration modifies data)
		if !dryRun {
			CheckReadonly("migrate dolt")
		}

		handleToDoltMigration(dryRun, autoYes)
	},
}

func init() {
	migrateDoltCmd.Flags().Bool("dry-run", false, "Preview migration without making changes")
	migrateDoltCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt (for automation)")
	migrateCmd.AddCommand(migrateDoltCmd)
}
