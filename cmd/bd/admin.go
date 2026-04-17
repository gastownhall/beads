package main

import (
	"github.com/spf13/cobra"
)

var adminCmd = &cobra.Command{
	Use:     "admin",
	GroupID: "advanced",
	Short:   "Administrative commands for database maintenance",
	Long: `Administrative commands for beads database maintenance.

These commands are for advanced users and should be used carefully:
  cleanup   Delete closed issues (issue lifecycle)
  compact   Compact old closed issues to save space (storage optimization)
  reset     Remove all beads data and configuration (full reset)

For routine maintenance, prefer 'bd doctor --fix' which handles common repairs
automatically. Use these admin commands for targeted database operations.`,
}

func init() {
	rootCmd.AddCommand(adminCmd)
	adminCmd.AddCommand(cleanupCmd)
	adminCmd.AddCommand(compactCmd)
	adminCmd.AddCommand(resetCmd)
}
