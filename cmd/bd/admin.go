package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var adminCmd = &cobra.Command{
	Use:     "admin",
	GroupID: "advanced",
	Short:   "Administrative commands for database maintenance",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if isEmbeddedDolt {
			fmt.Fprintln(os.Stderr, "Error: 'bd admin' is not yet supported in embedded mode")
			os.Exit(1)
		}
	},
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
