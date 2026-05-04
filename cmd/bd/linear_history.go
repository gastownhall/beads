package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
)

var linearHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Show Linear sync history",
	Long: `Show the audit log of past Linear sync operations.

By default, shows the last 10 sync runs in summary form.

Examples:
  bd linear history                          # Last 10 sync runs
  bd linear history --since=2026-05-01       # Runs since a date
  bd linear history --limit=25               # Last 25 runs
  bd linear history --detail <run-id>        # Per-issue detail for a run`,
	Run: runLinearHistory,
}

func init() {
	linearHistoryCmd.Flags().String("since", "", "Show runs starting at or after this date (YYYY-MM-DD or RFC3339)")
	linearHistoryCmd.Flags().String("detail", "", "Show per-issue detail for a specific sync run ID")
	linearHistoryCmd.Flags().Int("limit", 10, "Maximum number of runs to show")

	linearCmd.AddCommand(linearHistoryCmd)
}

func runLinearHistory(cmd *cobra.Command, args []string) {
	if err := ensureStoreActive(); err != nil {
		FatalError("database not available: %v", err)
	}

	db := getSyncHistoryDB()
	if db == nil {
		FatalError("sync history requires a Dolt-backed database")
	}

	ctx := rootCtx
	detailID, _ := cmd.Flags().GetString("detail")

	if detailID != "" {
		showSyncHistoryDetail(ctx, db, detailID)
		return
	}

	sinceStr, _ := cmd.Flags().GetString("since")
	limit, _ := cmd.Flags().GetInt("limit")

	var since *time.Time
	if sinceStr != "" {
		parsed, err := parseDateOrRFC3339(sinceStr)
		if err != nil {
			FatalError("invalid --since value %q: %v", sinceStr, err)
		}
		since = &parsed
	}

	entries, err := tracker.QuerySyncHistory(ctx, db, "Linear", since, limit)
	if err != nil {
		FatalError("querying sync history: %v", err)
	}

	if jsonOutput {
		outputJSON(entries)
		return
	}

	if len(entries) == 0 {
		fmt.Println("No sync history found")
		if since != nil {
			fmt.Printf("  (filtered: --since=%s)\n", sinceStr)
		}
		return
	}

	fmt.Printf("Linear Sync History (%d runs)\n", len(entries))
	fmt.Println("========================================")
	for _, e := range entries {
		status := "✓"
		if !e.Success {
			status = "✗"
		}
		if e.DryRun {
			status = "~"
		}
		fmt.Printf("\n%s  %s  [%s]  %s\n",
			status,
			e.StartedAt.Local().Format("2006-01-02 15:04:05"),
			e.Direction,
			e.SyncRunID[:8],
		)
		fmt.Printf("    Created: %d  Updated: %d  Skipped: %d  Failed: %d",
			e.IssuesCreated, e.IssuesUpdated, e.IssuesSkipped, e.IssuesFailed)
		if e.Conflicts > 0 {
			fmt.Printf("  Conflicts: %d", e.Conflicts)
		}
		fmt.Println()
		if e.ErrorMessage != "" {
			fmt.Printf("    Error: %s\n", e.ErrorMessage)
		}
	}
	fmt.Println()
	fmt.Println("Use --detail <run-id> to see per-issue outcomes")
}

func showSyncHistoryDetail(ctx context.Context, db *sql.DB, runID string) {
	entries, err := tracker.QuerySyncHistory(ctx, db, "", nil, 0)
	if err != nil {
		FatalError("querying sync history: %v", err)
	}

	// Find matching entry (supports prefix match)
	var entry *tracker.SyncHistoryEntry
	for i := range entries {
		if entries[i].SyncRunID == runID || len(runID) >= 8 && len(entries[i].SyncRunID) >= len(runID) && entries[i].SyncRunID[:len(runID)] == runID {
			entry = &entries[i]
			break
		}
	}
	if entry == nil {
		FatalError("sync run %q not found", runID)
	}

	items, err := tracker.QuerySyncHistoryItems(ctx, db, entry.SyncRunID)
	if err != nil {
		FatalError("querying sync history items: %v", err)
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"run":   entry,
			"items": items,
		})
		return
	}

	status := "Success"
	if !entry.Success {
		status = "Failed"
	}
	if entry.DryRun {
		status = "Dry Run"
	}

	fmt.Printf("Sync Run: %s\n", entry.SyncRunID)
	fmt.Printf("Status:   %s\n", status)
	fmt.Printf("Tracker:  %s\n", entry.Tracker)
	fmt.Printf("Dir:      %s\n", entry.Direction)
	fmt.Printf("Started:  %s\n", entry.StartedAt.Local().Format(time.RFC3339))
	fmt.Printf("Ended:    %s\n", entry.CompletedAt.Local().Format(time.RFC3339))
	fmt.Printf("Duration: %s\n", entry.CompletedAt.Sub(entry.StartedAt).Round(time.Millisecond))
	if entry.Actor != "" {
		fmt.Printf("Actor:    %s\n", entry.Actor)
	}
	fmt.Printf("Created: %d  Updated: %d  Skipped: %d  Failed: %d  Conflicts: %d\n",
		entry.IssuesCreated, entry.IssuesUpdated, entry.IssuesSkipped, entry.IssuesFailed, entry.Conflicts)

	if entry.ErrorMessage != "" {
		fmt.Printf("\nError: %s\n", entry.ErrorMessage)
	}

	if len(items) == 0 {
		fmt.Println("\nNo per-issue detail recorded for this run")
		return
	}

	fmt.Printf("\nPer-Issue Outcomes (%d items)\n", len(items))
	fmt.Printf("%-20s  %-15s  %-10s  %s\n", "Bead ID", "External ID", "Outcome", "Error")
	fmt.Printf("%-20s  %-15s  %-10s  %s\n", "--------------------", "---------------", "----------", "-----")
	for _, item := range items {
		extID := item.ExternalID
		if extID == "" {
			extID = "-"
		}
		errMsg := item.ErrorMessage
		if errMsg == "" {
			errMsg = ""
		}
		fmt.Printf("%-20s  %-15s  %-10s  %s\n", item.BeadID, extID, item.Outcome, errMsg)
	}
}

func getSyncHistoryDB() *sql.DB {
	if store == nil {
		return nil
	}
	accessor, ok := storage.UnwrapStore(store).(storage.RawDBAccessor)
	if !ok {
		return nil
	}
	return accessor.DB()
}

func parseDateOrRFC3339(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("expected YYYY-MM-DD or RFC3339 format")
}
