package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
)

// syncCmd exports Dolt database to JSONL for backward compatibility.
// With Dolt-native storage, writes are persisted immediately — but callers
// (hooks, scripts) still expect "bd sync" to produce an up-to-date JSONL file.
var syncCmd = &cobra.Command{
	Use:     "sync",
	GroupID: "sync",
	Short:   "Export database to JSONL (Dolt persists writes immediately)",
	Long: `With Dolt-native storage, all writes are persisted immediately.
This command exports the database to JSONL so that the on-disk JSONL file
stays in sync with Dolt, which is required by bd doctor and git-based workflows.

For Dolt remote operations, use:
  bd dolt push     Push to Dolt remote
  bd dolt pull     Pull from Dolt remote

For data interchange:
  bd export        Export database to JSONL
  bd import        Import JSONL into database`,
	Run: func(_ *cobra.Command, _ []string) {
		// The global store is already opened by PersistentPreRun with the
		// access lock held. Use it directly instead of spawning a subprocess
		// (which would deadlock on the same lock).
		if store == nil {
			return // No database open, nothing to export
		}
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			return
		}
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := exportToJSONLWithStore(rootCtx, store, jsonlPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: export failed: %v\n", err)
		}
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
