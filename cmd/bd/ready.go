package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

var readyCmd = &cobra.Command{
	Use:   "ready",
	Short: "Show ready work (no blockers)",
	Run: func(cmd *cobra.Command, args []string) {
		limit, _ := cmd.Flags().GetInt("limit")
		assignee, _ := cmd.Flags().GetString("assignee")

		filter := types.WorkFilter{
			Status: types.StatusOpen,
			Limit:  limit,
		}
		// Use Changed() to properly handle P0 (priority=0)
		if cmd.Flags().Changed("priority") {
			priority, _ := cmd.Flags().GetInt("priority")
			filter.Priority = &priority
		}
		if assignee != "" {
			filter.Assignee = &assignee
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			readyArgs := &rpc.ReadyArgs{
				Assignee: assignee,
				Limit:    limit,
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

			if len(issues) == 0 {
				yellow := color.New(color.FgYellow).SprintFunc()
				fmt.Printf("\n%s No ready work found (all issues have blocking dependencies)\n\n",
					yellow("✨"))
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
		ctx := context.Background()
		issues, err := store.GetReadyWork(ctx, filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			// Always output array, even if empty
			if issues == nil {
				issues = []*types.Issue{}
			}
			outputJSON(issues)
			return
		}

		if len(issues) == 0 {
			yellow := color.New(color.FgYellow).SprintFunc()
			fmt.Printf("\n%s No ready work found (all issues have blocking dependencies)\n\n",
				yellow("✨"))
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
	},
}

var blockedCmd = &cobra.Command{
	Use:   "blocked",
	Short: "Show blocked issues",
	Run: func(cmd *cobra.Command, args []string) {
		// If daemon is running but doesn't support this command, use direct storage
		if daemonClient != nil && store == nil {
			var err error
			store, err = sqlite.New(dbPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
				os.Exit(1)
			}
			defer store.Close()
		}

		ctx := context.Background()
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
			fmt.Printf("  Blocked by %d open dependencies: %v\n",
				issue.BlockedByCount, issue.BlockedBy)
			fmt.Println()
		}
	},
}

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show statistics",
	Run: func(cmd *cobra.Command, args []string) {
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
			if stats.AverageLeadTime > 0 {
				fmt.Printf("Avg Lead Time:     %.1f hours\n", stats.AverageLeadTime)
			}
			fmt.Println()
			return
		}

		// Direct mode
		ctx := context.Background()
		stats, err := store.GetStatistics(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
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
		fmt.Printf("Total Issues:           %d\n", stats.TotalIssues)
		fmt.Printf("Open:                   %s\n", green(fmt.Sprintf("%d", stats.OpenIssues)))
		fmt.Printf("In Progress:            %s\n", yellow(fmt.Sprintf("%d", stats.InProgressIssues)))
		fmt.Printf("Closed:                 %d\n", stats.ClosedIssues)
		fmt.Printf("Blocked:                %d\n", stats.BlockedIssues)
		fmt.Printf("Ready:                  %s\n", green(fmt.Sprintf("%d", stats.ReadyIssues)))
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

	rootCmd.AddCommand(readyCmd)
	rootCmd.AddCommand(blockedCmd)
	rootCmd.AddCommand(statsCmd)
}
