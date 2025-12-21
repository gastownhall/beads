package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/util"
)
var readyCmd = &cobra.Command{
	Use:   "ready",
	Short: "Show ready work (no blockers, open or in_progress)",
	Run: func(cmd *cobra.Command, args []string) {
		limit, _ := cmd.Flags().GetInt("limit")
		assignee, _ := cmd.Flags().GetString("assignee")
		unassigned, _ := cmd.Flags().GetBool("unassigned")
		sortPolicy, _ := cmd.Flags().GetString("sort")
		labels, _ := cmd.Flags().GetStringSlice("label")
		labelsAny, _ := cmd.Flags().GetStringSlice("label-any")
		issueType, _ := cmd.Flags().GetString("type")
		// Use global jsonOutput set by PersistentPreRun (respects config.yaml + env vars)

		// Normalize labels: trim, dedupe, remove empty
		labels = util.NormalizeLabels(labels)
		labelsAny = util.NormalizeLabels(labelsAny)

		// Apply directory-aware label scoping if no labels explicitly provided (GH#541)
		if len(labels) == 0 && len(labelsAny) == 0 {
			if dirLabels := config.GetDirectoryLabels(); len(dirLabels) > 0 {
				labelsAny = dirLabels
			}
		}

		filter := types.WorkFilter{
			// Leave Status empty to get both 'open' and 'in_progress' (bd-165)
			Type:       issueType,
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
				Type:       issueType,
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
				if hasOpenIssues {
					fmt.Printf("\n%s No ready work found (all issues have blocking dependencies)\n\n",
						ui.RenderWarn("✨"))
				} else {
					fmt.Printf("\n%s No open issues\n\n", ui.RenderPass("✨"))
				}
				return
			}
			fmt.Printf("\n%s Ready work (%d issues with no blockers):\n\n", ui.RenderAccent("📋"), len(issues))
			for i, issue := range issues {
				fmt.Printf("%d. [%s] [%s] %s: %s\n", i+1,
					ui.RenderPriority(issue.Priority),
					ui.RenderType(string(issue.IssueType)),
					ui.RenderID(issue.ID), issue.Title)
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
				fmt.Printf("\n%s No ready work found (all issues have blocking dependencies)\n\n",
					ui.RenderWarn("✨"))
			} else {
				fmt.Printf("\n%s No open issues\n\n", ui.RenderPass("✨"))
			}
			// Show tip even when no ready work found
			maybeShowTip(store)
			return
		}
		fmt.Printf("\n%s Ready work (%d issues with no blockers):\n\n", ui.RenderAccent("📋"), len(issues))
		for i, issue := range issues {
			fmt.Printf("%d. [%s] [%s] %s: %s\n", i+1,
				ui.RenderPriority(issue.Priority),
				ui.RenderType(string(issue.IssueType)),
				ui.RenderID(issue.ID), issue.Title)
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
			fmt.Printf("\n%s No blocked issues\n\n", ui.RenderPass("✨"))
			return
		}
		fmt.Printf("\n%s Blocked issues (%d):\n\n", ui.RenderFail("🚫"), len(blocked))
		for _, issue := range blocked {
			fmt.Printf("[%s] %s: %s\n",
				ui.RenderPriority(issue.Priority),
				ui.RenderID(issue.ID), issue.Title)
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

func init() {
	readyCmd.Flags().IntP("limit", "n", 10, "Maximum issues to show")
	readyCmd.Flags().IntP("priority", "p", 0, "Filter by priority")
	readyCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	readyCmd.Flags().BoolP("unassigned", "u", false, "Show only unassigned issues")
	readyCmd.Flags().StringP("sort", "s", "hybrid", "Sort policy: hybrid (default), priority, oldest")
	readyCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL). Can combine with --label-any")
	readyCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE). Can combine with --label")
	readyCmd.Flags().StringP("type", "t", "", "Filter by issue type (task, bug, feature, epic, merge-request)")
	rootCmd.AddCommand(readyCmd)
	rootCmd.AddCommand(blockedCmd)
}
