package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/linear"
	"github.com/steveyegge/beads/internal/ui"
)

var linearHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Show Linear sync history",
	Long: `Show the history of Linear sync operations.

By default, shows the most recent sync runs in summary form.

Flags:
  --since DATE      Show sync runs since DATE (YYYY-MM-DD or RFC3339)
  --detail ID       Show per-issue outcomes for a specific sync run
  --rollback ID     Generate a rollback script for a specific sync run
  --limit N         Limit the number of runs shown (default 20)

Examples:
  bd linear history                           # Recent sync runs
  bd linear history --since=2026-05-01        # Runs since May 1st
  bd linear history --detail <run-id>         # Per-issue detail for a run
  bd linear history --rollback <run-id>       # Generate rollback script
  bd linear history --since=2026-05-01 --json # JSON output`,
	Run: runLinearHistory,
}

func init() {
	linearHistoryCmd.Flags().String("since", "", "Show sync runs since this date (YYYY-MM-DD or RFC3339)")
	linearHistoryCmd.Flags().String("detail", "", "Show per-issue outcomes for this sync run ID")
	linearHistoryCmd.Flags().String("rollback", "", "Generate a rollback script for this sync run ID")
	linearHistoryCmd.Flags().Int("limit", 20, "Maximum number of sync runs to show")
	linearCmd.AddCommand(linearHistoryCmd)
}

func runLinearHistory(cmd *cobra.Command, args []string) {
	sinceStr, _ := cmd.Flags().GetString("since")
	detailRunID, _ := cmd.Flags().GetString("detail")
	rollbackRunID, _ := cmd.Flags().GetString("rollback")
	limit, _ := cmd.Flags().GetInt("limit")

	ctx := rootCtx

	rawDB := getSyncHistoryDB()
	if rawDB == nil {
		FatalError("database not available for sync history")
	}

	histDB := linear.NewSyncHistoryDB(rawDB)

	if rollbackRunID != "" {
		showRollback(ctx, histDB, rollbackRunID)
		return
	}

	if detailRunID != "" {
		showRunDetail(ctx, histDB, detailRunID)
		return
	}

	var since *time.Time
	if sinceStr != "" {
		t, err := parseDateArg(sinceStr)
		if err != nil {
			FatalError("invalid --since value %q: %v", sinceStr, err)
		}
		since = &t
	}

	runs, err := histDB.ListSyncRuns(ctx, since, limit)
	if err != nil {
		FatalError("querying sync history: %v", err)
	}

	if len(runs) == 0 {
		if jsonOutput {
			outputJSON([]interface{}{})
		} else {
			fmt.Println("No sync history found.")
			if sinceStr != "" {
				fmt.Printf("Try a broader --since range or omit --since to see all runs.\n")
			}
		}
		return
	}

	if jsonOutput {
		outputJSON(runs)
		return
	}

	fmt.Printf("\n%s Linear Sync History (%d runs)\n\n", ui.RenderAccent("📋"), len(runs))
	fmt.Printf("%-36s  %-19s  %-6s  %5s  %5s  %5s  %5s  %s\n",
		"Run ID", "Started", "Dir", "New", "Upd", "Skip", "Fail", "Error")
	fmt.Println(strings.Repeat("─", 110))

	for _, run := range runs {
		errIndicator := ""
		if run.ErrorMessage != "" {
			errIndicator = truncateHistStr(run.ErrorMessage, 30)
		}
		dryTag := ""
		if run.DryRun {
			dryTag = " (dry)"
		}
		fmt.Printf("%-36s  %-19s  %-6s  %5d  %5d  %5d  %5d  %s\n",
			run.SyncRunID,
			run.StartedAt.Format("2006-01-02 15:04:05"),
			run.Direction+dryTag,
			run.IssuesCreated, run.IssuesUpdated, run.IssuesSkipped, run.IssuesFailed,
			errIndicator)
	}
	fmt.Printf("\nUse 'bd linear history --detail <run-id>' for per-issue details.\n\n")
}

func showRunDetail(ctx context.Context, histDB *linear.SyncHistoryDB, runID string) {
	run, err := histDB.GetSyncRun(ctx, runID)
	if err != nil {
		FatalError("fetching sync run: %v", err)
	}
	if run == nil {
		FatalError("sync run %s not found", runID)
	}

	items, err := histDB.GetSyncRunItems(ctx, runID)
	if err != nil {
		FatalError("fetching sync items: %v", err)
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"run":   run,
			"items": items,
		})
		return
	}

	fmt.Printf("\n%s Sync Run Detail\n\n", ui.RenderAccent("🔍"))
	fmt.Printf("  Run ID:     %s\n", run.SyncRunID)
	fmt.Printf("  Started:    %s\n", run.StartedAt.Format(time.RFC3339))
	fmt.Printf("  Completed:  %s\n", run.CompletedAt.Format(time.RFC3339))
	dur := run.CompletedAt.Sub(run.StartedAt)
	fmt.Printf("  Duration:   %s\n", dur.Truncate(time.Millisecond))
	fmt.Printf("  Direction:  %s\n", run.Direction)
	if run.DryRun {
		fmt.Printf("  Dry Run:    yes\n")
	}
	if run.ConflictResolution != "" {
		fmt.Printf("  Conflicts:  %s\n", run.ConflictResolution)
	}
	fmt.Printf("  Created:    %d\n", run.IssuesCreated)
	fmt.Printf("  Updated:    %d\n", run.IssuesUpdated)
	fmt.Printf("  Skipped:    %d\n", run.IssuesSkipped)
	fmt.Printf("  Failed:     %d\n", run.IssuesFailed)
	if run.ErrorMessage != "" {
		fmt.Printf("  Error:      %s\n", run.ErrorMessage)
	}

	if len(items) == 0 {
		fmt.Printf("\n  No per-issue items recorded.\n\n")
		return
	}

	fmt.Printf("\n  Per-Issue Outcomes (%d items):\n\n", len(items))
	fmt.Printf("  %-20s  %-20s  %-6s  %-10s  %8s  %s\n",
		"Bead ID", "Linear ID", "Dir", "Outcome", "Duration", "Error")
	fmt.Println("  " + strings.Repeat("─", 90))

	for _, item := range items {
		durStr := ""
		if item.DurationMs > 0 {
			durStr = fmt.Sprintf("%dms", item.DurationMs)
		}
		errStr := ""
		if item.ErrorMessage != "" {
			errStr = truncateHistStr(item.ErrorMessage, 30)
		}
		fmt.Printf("  %-20s  %-20s  %-6s  %-10s  %8s  %s\n",
			truncateHistStr(item.BeadID, 20),
			truncateHistStr(item.LinearID, 20),
			item.Direction, item.Outcome, durStr, errStr)

		if len(item.BeforeValues) > 0 || len(item.AfterValues) > 0 {
			renderFieldDiff(item.BeforeValues, item.AfterValues)
		}
	}
	fmt.Println()
}

func renderFieldDiff(before, after map[string]string) {
	allKeys := make(map[string]bool)
	for k := range before {
		allKeys[k] = true
	}
	for k := range after {
		allKeys[k] = true
	}
	for k := range allKeys {
		bv := before[k]
		av := after[k]
		if bv != av {
			fmt.Printf("    %s: %q → %q\n", k, truncateHistStr(bv, 50), truncateHistStr(av, 50))
		}
	}
}

func truncateHistStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}

func showRollback(ctx context.Context, histDB *linear.SyncHistoryDB, runID string) {
	mutations, err := linear.GenerateRollbackMutations(ctx, histDB, runID)
	if err != nil {
		FatalError("generating rollback: %v", err)
	}

	if jsonOutput {
		outputJSON(mutations)
		return
	}

	fmt.Print(linear.RollbackScript(mutations))
}

func parseDateArg(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02T15:04:05", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("expected YYYY-MM-DD or RFC3339 format, got %q", s)
}
