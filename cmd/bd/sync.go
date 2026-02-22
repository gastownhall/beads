package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// syncCmd is a deprecated no-op that directs users to bd dolt push/pull.
var syncCmd = &cobra.Command{
	Use:     "sync",
	GroupID: "sync",
	Short:   "Deprecated: use 'bd dolt push' and 'bd dolt pull' instead",
	Long: `bd sync is deprecated and is now a no-op.

Use Dolt remote commands directly:
  bd dolt push     Push to Dolt remote
  bd dolt pull     Pull from Dolt remote`,
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Println("bd sync is deprecated. Use 'bd dolt push' and 'bd dolt pull' instead.")
	},
}

func init() {
	// Keep all legacy flags so old invocations don't error out.
	syncCmd.Flags().StringP("message", "m", "", "Deprecated: no-op")
	syncCmd.Flags().Bool("dry-run", false, "Deprecated: no-op")
	syncCmd.Flags().Bool("no-push", false, "Deprecated: no-op")
	syncCmd.Flags().Bool("import", false, "Deprecated: use 'bd import' instead")
	syncCmd.Flags().Bool("import-only", false, "Deprecated: use 'bd import' instead")
	syncCmd.Flags().Bool("export", false, "Deprecated: use 'bd export' instead")
	syncCmd.Flags().Bool("flush-only", false, "Deprecated: no-op")
	syncCmd.Flags().Bool("pull", false, "Deprecated: use 'bd dolt pull' instead")
	syncCmd.Flags().Bool("no-git-history", false, "Deprecated: no-op")
	syncCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.AddCommand(syncCmd)
}
