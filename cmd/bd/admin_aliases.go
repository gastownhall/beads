package main

import (
	"github.com/spf13/cobra"
)

// Hidden aliases for backwards compatibility.
// These commands forward to their admin subcommand equivalents.
// They are hidden from help output but still work for scripts/muscle memory.

var cleanupAliasCmd = &cobra.Command{
	Use:        "cleanup",
	Hidden:     true,
	Deprecated: "use 'bd admin cleanup' instead (will be removed in v1.0.0)",
	Short:      "Alias for 'bd admin cleanup' (deprecated)",
	Long:       cleanupCmd.Long,
	Run: func(cmd *cobra.Command, args []string) {
		cleanupCmd.Run(cmd, args)
	},
}

var compactAliasCmd = &cobra.Command{
	Use:        "compact",
	Hidden:     true,
	Deprecated: "use 'bd admin compact' instead (will be removed in v1.0.0)",
	Short:      "Alias for 'bd admin compact' (deprecated)",
	Long:       compactCmd.Long,
	Run: func(cmd *cobra.Command, args []string) {
		compactCmd.Run(cmd, args)
	},
}

var resetAliasCmd = &cobra.Command{
	Use:        "reset",
	Hidden:     true,
	Deprecated: "use 'bd admin reset' instead (will be removed in v1.0.0)",
	Short:      "Alias for 'bd admin reset' (deprecated)",
	Long:       resetCmd.Long,
	Run: func(cmd *cobra.Command, args []string) {
		resetCmd.Run(cmd, args)
	},
}

func init() {
	// Copy flags from original commands to aliases, binding to same global variables
	// This ensures that when the alias command runs, the global flag variables are set correctly

	// Cleanup alias flags - these read from cmd.Flags() in the Run function
	cleanupAliasCmd.Flags().BoolP("force", "f", false, "Actually delete (without this flag, shows error)")
	cleanupAliasCmd.Flags().Bool("dry-run", false, "Preview what would be deleted without making changes")
	cleanupAliasCmd.Flags().Bool("cascade", false, "Recursively delete all dependent issues")
	cleanupAliasCmd.Flags().Int("older-than", 0, "Only delete issues closed more than N days ago (0 = all closed issues)")
	cleanupAliasCmd.Flags().Bool("hard", false, "Bypass tombstone TTL safety; use --older-than days as cutoff")
	cleanupAliasCmd.Flags().Bool("ephemeral", false, "Only delete closed wisps (transient molecules)")

	// Compact alias flags - must bind to same global variables as compactCmd
	compactAliasCmd.Flags().BoolVar(&compactDryRun, "dry-run", false, "Preview without compacting")
	compactAliasCmd.Flags().IntVar(&compactTier, "tier", 1, "Compaction tier (1 or 2)")
	compactAliasCmd.Flags().BoolVar(&compactAll, "all", false, "Process all candidates")
	compactAliasCmd.Flags().StringVar(&compactID, "id", "", "Compact specific issue")
	compactAliasCmd.Flags().BoolVar(&compactForce, "force", false, "Force compact (bypass checks, requires --id)")
	compactAliasCmd.Flags().IntVar(&compactBatch, "batch-size", 10, "Issues per batch")
	compactAliasCmd.Flags().IntVar(&compactWorkers, "workers", 5, "Parallel workers")
	compactAliasCmd.Flags().BoolVar(&compactStats, "stats", false, "Show compaction statistics")
	compactAliasCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON format")
	compactAliasCmd.Flags().BoolVar(&compactAnalyze, "analyze", false, "Analyze mode: export candidates for agent review")
	compactAliasCmd.Flags().BoolVar(&compactApply, "apply", false, "Apply mode: accept agent-provided summary")
	compactAliasCmd.Flags().BoolVar(&compactAuto, "auto", false, "Auto mode: AI-powered compaction (legacy)")
	compactAliasCmd.Flags().BoolVar(&compactPrune, "prune", false, "Prune mode: remove expired tombstones from issues.jsonl")
	compactAliasCmd.Flags().IntVar(&compactOlderThan, "older-than", 0, "Prune tombstones older than N days (default: 30)")
	compactAliasCmd.Flags().StringVar(&compactSummary, "summary", "", "Path to summary file (use '-' for stdin)")
	compactAliasCmd.Flags().StringVar(&compactActor, "actor", "agent", "Actor name for audit trail")
	compactAliasCmd.Flags().IntVar(&compactLimit, "limit", 0, "Limit number of candidates (0 = no limit)")
	compactAliasCmd.Flags().BoolVar(&compactDolt, "dolt", false, "Dolt mode: run Dolt garbage collection on .beads/dolt")

	// Reset alias flags - these read from cmd.Flags() in the Run function
	resetAliasCmd.Flags().Bool("force", false, "Actually perform the reset (required)")

	// Register hidden aliases on root command
	rootCmd.AddCommand(cleanupAliasCmd)
	rootCmd.AddCommand(compactAliasCmd)
	rootCmd.AddCommand(resetAliasCmd)
}
