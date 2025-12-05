package main
import (
	"encoding/json"
	"fmt"
	"os"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/util"
)
var readyCmd = &cobra.Command{
	Use:   "ready",
	Short: "Show ready work (no blockers, open or in-progress)",
	Run: func(cmd *cobra.Command, args []string) {
		limit, _ := cmd.Flags().GetInt("limit")
		assignee, _ := cmd.Flags().GetString("assignee")
		unassigned, _ := cmd.Flags().GetBool("unassigned")
		sortPolicy, _ := cmd.Flags().GetString("sort")
		labels, _ := cmd.Flags().GetStringSlice("label")
		labelsAny, _ := cmd.Flags().GetStringSlice("label-any")
		// Use global jsonOutput set by PersistentPreRun (respects config.yaml + env vars)

		// Normalize labels: trim, dedupe, remove empty
		labels = util.NormalizeLabels(labels)
		labelsAny = util.NormalizeLabels(labelsAny)

		filter := types.WorkFilter{
			// Leave Status empty to get both 'open' and 'in_progress' (bd-165)
			Limit:      limit,
			Unassigned: unassigned,
			SortPolicy: types.SortPolicy(sortPolicy),
			Labels:     labels,
			LabelsAny:  labelsAny,
		}
		// Use Changed() to properly handle P0 (priority=0)
		if cmd.Flags().Changed("priority") {
			priority, _ := cmd.Flags().GetInt("priority")
			filter.Priority = &priority
		}
		if assignee != "" && !unassigned {
			filter.Assignee = &assignee
		}
		// Validate sort policy
		if !filter.SortPolicy.IsValid() {
			fmt.Fprintf(os.Stderr, "Error: invalid sort policy '%s'. Valid values: hybrid, priority, oldest\n", sortPolicy)
			os.Exit(1)
		}
		// If daemon is running, use RPC
		if daemonClient != nil {
			readyArgs := &rpc.ReadyArgs{
				Assignee:   assignee,
				Unassigned: unassigned,
				Limit:      limit,
				SortPolicy: sortPolicy,
				Labels:     labels,
				LabelsAny:  labelsAny,
			}
			if cmd.Flags().Changed("priority") {
				priority, _ := cmd.Flags().GetInt("priority")
				readyArgs.Priority = &priority
			}
			resp, err := daemonClient.Ready(readyArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			var issues []*types.Issue
			if err := json.Unmarshal(resp.Data, &issues); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
				os.Exit(1)
			}
			if jsonOutput {
				if issues == nil {
					issues = []*types.Issue{}
				}
				outputJSON(issues)
				return
			}

			// Show upgrade notification if needed (bd-loka)
			maybeShowUpgradeNotification()

			if len(issues) == 0 {
				// Check if there are any open issues at all (bd-r4n)
				statsResp, statsErr := daemonClient.Stats()
				hasOpenIssues := false
				if statsErr == nil {
					var stats types.Statistics
					if json.Unmarshal(statsResp.Data, &stats) == nil {
						hasOpenIssues = stats.OpenIssues > 0 || stats.InProgressIssues > 0
					}
				}
				yellow := color.New(color.FgYellow).SprintFunc()
				if hasOpenIssues {
					fmt.Printf("\n%s No ready work found (all issues have blocking dependencies)\n\n",
						yellow("✨"))
				} else {
					green := color.New(color.FgGreen).SprintFunc()
					fmt.Printf("\n%s No open issues\n\n", green("✨"))
				}
				return
			}
			cyan := color.New(color.FgCyan).SprintFunc()
			fmt.Printf("\n%s Ready work (%d issues with no blockers):\n\n", cyan("📋"), len(issues))
			for i, issue := range issues {
				fmt.Printf("%d. [P%d] %s: %s\n", i+1, issue.Priority, issue.ID, issue.Title)
				if issue.EstimatedMinutes != nil {
					fmt.Printf("   Estimate: %d min\n", *issue.EstimatedMinutes)
				}
				if issue.Assignee != "" {
					fmt.Printf("   Assignee: %s\n", issue.Assignee)
				}
			}
			fmt.Println()
			return
		}
		// Direct mode
		ctx := rootCtx

		// Check database freshness before reading (bd-2q6d, bd-c4rq)
		// Skip check when using daemon (daemon auto-imports on staleness)
		if daemonClient == nil {
			if err := ensureDatabaseFresh(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		issues, err := store.GetReadyWork(ctx, filter)
		if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
		}
	// If no ready work found, check if git has issues and auto-import
	if len(issues) == 0 {
		if checkAndAutoImport(ctx, store) {
			// Re-run the query after import
			issues, err = store.GetReadyWork(ctx, filter)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	}
		if jsonOutput {
			// Always output array, even if empty
			if issues == nil {
				issues = []*types.Issue{}
			}
			outputJSON(issues)
			return
		}
		// Show upgrade notification if needed (bd-loka)
		maybeShowUpgradeNotification()

		if len(issues) == 0 {
			// Check if there are any open issues at all (bd-r4n)
			hasOpenIssues := false
			if stats, statsErr := store.GetStatistics(ctx); statsErr == nil {
				hasOpenIssues = stats.OpenIssues > 0 || stats.InProgressIssues > 0
			}
			if hasOpenIssues {
				yellow := color.New(color.FgYellow).SprintFunc()
				fmt.Printf("\n%s No ready work found (all issues have blocking dependencies)\n\n",
					yellow("✨"))
			} else {
				green := color.New(color.FgGreen).SprintFunc()
				fmt.Printf("\n%s No open issues\n\n", green("✨"))
			}
			// Show tip even when no ready work found
			maybeShowTip(store)
			return
		}
		cyan := color.New(color.FgCyan).SprintFunc()
		fmt.Printf("\n%s Ready work (%d issues with no blockers):\n\n", cyan("📋"), len(issues))
		for i, issue := range issues {
			fmt.Printf("%d. [P%d] %s: %s\n", i+1, issue.Priority, issue.ID, issue.Title)
			if issue.EstimatedMinutes != nil {
				fmt.Printf("   Estimate: %d min\n", *issue.EstimatedMinutes)
			}
			if issue.Assignee != "" {
				fmt.Printf("   Assignee: %s\n", issue.Assignee)
			}
		}
		fmt.Println()

		// Show tip after successful ready (direct mode only)
		maybeShowTip(store)
	},
}
var blockedCmd = &cobra.Command{
	Use:   "blocked",
	Short: "Show blocked issues",
	Run: func(cmd *cobra.Command, args []string) {
		// Use global jsonOutput set by PersistentPreRun (respects config.yaml + env vars)
		// If daemon is running but doesn't support this command, use direct storage
		ctx := rootCtx
		if daemonClient != nil && store == nil {
			var err error
			store, err = sqlite.New(ctx, dbPath)
			if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
			os.Exit(1)
			}
			defer func() { _ = store.Close() }()
			}
		blocked, err := store.GetBlockedIssues(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if jsonOutput {
			// Always output array, even if empty
			if blocked == nil {
				blocked = []*types.BlockedIssue{}
			}
			outputJSON(blocked)
			return
		}
		if len(blocked) == 0 {
			green := color.New(color.FgGreen).SprintFunc()
			fmt.Printf("\n%s No blocked issues\n\n", green("✨"))
			return
		}
		red := color.New(color.FgRed).SprintFunc()
		fmt.Printf("\n%s Blocked issues (%d):\n\n", red("🚫"), len(blocked))
		for _, issue := range blocked {
			fmt.Printf("[P%d] %s: %s\n", issue.Priority, issue.ID, issue.Title)
			blockedBy := issue.BlockedBy
			if blockedBy == nil {
				blockedBy = []string{}
			}
			fmt.Printf("  Blocked by %d open dependencies: %v\n",
				issue.BlockedByCount, blockedBy)
			fmt.Println()
		}
	},
}
var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show statistics",
	Run: func(cmd *cobra.Command, args []string) {
		// Use global jsonOutput set by PersistentPreRun (respects config.yaml + env vars)
		// If daemon is running, use RPC
		if daemonClient != nil {
			resp, err := daemonClient.Stats()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			var stats types.Statistics
			if err := json.Unmarshal(resp.Data, &stats); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
				os.Exit(1)
			}
			if jsonOutput {
				outputJSON(stats)
				return
			}
			cyan := color.New(color.FgCyan).SprintFunc()
			green := color.New(color.FgGreen).SprintFunc()
			yellow := color.New(color.FgYellow).SprintFunc()
			fmt.Printf("\n%s Beads Statistics:\n\n", cyan("📊"))
			fmt.Printf("Total Issues:      %d\n", stats.TotalIssues)
			fmt.Printf("Open:              %s\n", green(fmt.Sprintf("%d", stats.OpenIssues)))
			fmt.Printf("In Progress:       %s\n", yellow(fmt.Sprintf("%d", stats.InProgressIssues)))
			fmt.Printf("Closed:            %d\n", stats.ClosedIssues)
			fmt.Printf("Blocked:           %d\n", stats.BlockedIssues)
			fmt.Printf("Ready:             %s\n", green(fmt.Sprintf("%d", stats.ReadyIssues)))
			if stats.TombstoneIssues > 0 {
				fmt.Printf("Deleted:           %d (tombstones)\n", stats.TombstoneIssues)
			}
			if stats.AverageLeadTime > 0 {
				fmt.Printf("Avg Lead Time:     %.1f hours\n", stats.AverageLeadTime)
			}
			fmt.Println()
			return
		}
		// Direct mode
		ctx := rootCtx
		stats, err := store.GetStatistics(ctx)
		if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
		}
	// If no issues found, check if git has issues and auto-import
	if stats.TotalIssues == 0 {
		if checkAndAutoImport(ctx, store) {
			// Re-run the stats after import
			stats, err = store.GetStatistics(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	}
		if jsonOutput {
			outputJSON(stats)
			return
		}
		cyan := color.New(color.FgCyan).SprintFunc()
		green := color.New(color.FgGreen).SprintFunc()
		yellow := color.New(color.FgYellow).SprintFunc()
		fmt.Printf("\n%s Beads Statistics:\n\n", cyan("📊"))
		fmt.Printf("Total Issues:           %d\n", stats.TotalIssues)
		fmt.Printf("Open:                   %s\n", green(fmt.Sprintf("%d", stats.OpenIssues)))
		fmt.Printf("In Progress:            %s\n", yellow(fmt.Sprintf("%d", stats.InProgressIssues)))
		fmt.Printf("Closed:                 %d\n", stats.ClosedIssues)
		fmt.Printf("Blocked:                %d\n", stats.BlockedIssues)
		fmt.Printf("Ready:                  %s\n", green(fmt.Sprintf("%d", stats.ReadyIssues)))
		if stats.TombstoneIssues > 0 {
			fmt.Printf("Deleted:                %d (tombstones)\n", stats.TombstoneIssues)
		}
		if stats.EpicsEligibleForClosure > 0 {
			fmt.Printf("Epics Ready to Close:   %s\n", green(fmt.Sprintf("%d", stats.EpicsEligibleForClosure)))
		}
		if stats.AverageLeadTime > 0 {
			fmt.Printf("Avg Lead Time:          %.1f hours\n", stats.AverageLeadTime)
		}
		fmt.Println()
	},
}
func init() {
	readyCmd.Flags().IntP("limit", "n", 10, "Maximum issues to show")
	readyCmd.Flags().IntP("priority", "p", 0, "Filter by priority")
	readyCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	readyCmd.Flags().BoolP("unassigned", "u", false, "Show only unassigned issues")
	readyCmd.Flags().StringP("sort", "s", "hybrid", "Sort policy: hybrid (default), priority, oldest")
	readyCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL). Can combine with --label-any")
	readyCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE). Can combine with --label")
	rootCmd.AddCommand(readyCmd)
	rootCmd.AddCommand(blockedCmd)
	rootCmd.AddCommand(statsCmd)
}
