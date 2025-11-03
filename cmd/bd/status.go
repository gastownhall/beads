package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

// StatusOutput represents the complete status output
type StatusOutput struct {
	Summary        *StatusSummary         `json:"summary"`
	RecentActivity *RecentActivitySummary `json:"recent_activity,omitempty"`
}

// StatusSummary represents counts by state
type StatusSummary struct {
	TotalIssues      int `json:"total_issues"`
	OpenIssues       int `json:"open_issues"`
	InProgressIssues int `json:"in_progress_issues"`
	BlockedIssues    int `json:"blocked_issues"`
	ClosedIssues     int `json:"closed_issues"`
	ReadyIssues      int `json:"ready_issues"`
}

// RecentActivitySummary represents activity over the last 7 days
type RecentActivitySummary struct {
	DaysTracked   int `json:"days_tracked"`
	IssuesCreated int `json:"issues_created"`
	IssuesClosed  int `json:"issues_closed"`
	IssuesUpdated int `json:"issues_updated"`
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show issue database overview",
	Long: `Show a quick snapshot of the issue database state.

This command provides a summary of issue counts by state (open, in-progress,
blocked, closed), ready work, and recent activity over the last 7 days.

Similar to how 'git status' shows working tree state, 'bd status' gives you
a quick overview of your issue database without needing multiple queries.

Use cases:
  - Quick project health check
  - Onboarding for new contributors
  - Integration with shell prompts or CI/CD
  - Daily standup reference

Examples:
  bd status                    # Show summary
  bd status --json             # JSON format output
  bd status --assigned         # Show issues assigned to current user
  bd status --all              # Show all issues (same as default)`,
	Run: func(cmd *cobra.Command, args []string) {
		showAll, _ := cmd.Flags().GetBool("all")
		showAssigned, _ := cmd.Flags().GetBool("assigned")
		jsonFormat, _ := cmd.Flags().GetBool("json")

		// Override global jsonOutput if --json flag is set
		if jsonFormat {
			jsonOutput = true
		}

		// Get statistics
		var stats *types.Statistics
		var err error

		// If daemon is running, use RPC
		if daemonClient != nil {
			resp, rpcErr := daemonClient.Stats()
			if rpcErr != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", rpcErr)
				os.Exit(1)
			}

			if err := json.Unmarshal(resp.Data, &stats); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
				os.Exit(1)
			}
		} else {
			// Direct mode
			ctx := context.Background()
			stats, err = store.GetStatistics(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		// Build summary
		summary := &StatusSummary{
			TotalIssues:      stats.TotalIssues,
			OpenIssues:       stats.OpenIssues,
			InProgressIssues: stats.InProgressIssues,
			BlockedIssues:    stats.BlockedIssues,
			ClosedIssues:     stats.ClosedIssues,
			ReadyIssues:      stats.ReadyIssues,
		}

		// Get recent activity (last 7 days)
		var recentActivity *RecentActivitySummary
		if daemonClient != nil {
			// TODO(bd-28db): Add RPC support for recent activity
			// For now, skip recent activity in daemon mode
			recentActivity = nil
		} else {
			ctx := context.Background()
			var assigneeFilter *string
			if showAssigned {
				assigneeFilter = &actor
			}
			recentActivity = getRecentActivity(ctx, 7, assigneeFilter)
		}

		// Filter by assignee if requested
		if showAssigned {
			// Get filtered statistics for assigned issues
			summary = getAssignedStatus(actor)
		}

		output := &StatusOutput{
			Summary:        summary,
			RecentActivity: recentActivity,
		}

		// JSON output
		if jsonOutput {
			outputJSON(output)
			return
		}

		// Human-readable output
		fmt.Println("\nIssue Database Status")
		fmt.Println("=====================")
		fmt.Printf("\nSummary:\n")
		fmt.Printf("  Total Issues:      %d\n", summary.TotalIssues)
		fmt.Printf("  Open:              %d\n", summary.OpenIssues)
		fmt.Printf("  In Progress:       %d\n", summary.InProgressIssues)
		fmt.Printf("  Blocked:           %d\n", summary.BlockedIssues)
		fmt.Printf("  Closed:            %d\n", summary.ClosedIssues)
		fmt.Printf("  Ready to Work:     %d\n", summary.ReadyIssues)

		if recentActivity != nil {
			fmt.Printf("\nRecent Activity (last %d days):\n", recentActivity.DaysTracked)
			fmt.Printf("  Issues Created:    %d\n", recentActivity.IssuesCreated)
			fmt.Printf("  Issues Closed:     %d\n", recentActivity.IssuesClosed)
			fmt.Printf("  Issues Updated:    %d\n", recentActivity.IssuesUpdated)
		}

		// Show hint for more details
		fmt.Printf("\nFor more details, use 'bd list' to see individual issues.\n")
		fmt.Println()

		// Suppress showAll flag (it's the default behavior, included for CLI familiarity)
		_ = showAll
	},
}

// getRecentActivity calculates activity stats for the last N days
// If assignee is provided, only count issues assigned to that user
func getRecentActivity(ctx context.Context, days int, assignee *string) *RecentActivitySummary {
	if store == nil {
		return nil
	}

	// Calculate the cutoff time
	cutoff := time.Now().AddDate(0, 0, -days)

	// Get all issues to check creation/update times
	filter := types.IssueFilter{
		Assignee: assignee,
	}
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil
	}

	activity := &RecentActivitySummary{
		DaysTracked: days,
	}

	for _, issue := range issues {
		// Check if created recently
		if issue.CreatedAt.After(cutoff) {
			activity.IssuesCreated++
		}

		// Check if closed recently
		if issue.Status == types.StatusClosed && issue.UpdatedAt.After(cutoff) {
			// Verify it was actually closed recently (not just updated)
			// For now, we'll count any closed issue updated recently
			activity.IssuesClosed++
		}

		// Check if updated recently (but not created recently)
		if issue.UpdatedAt.After(cutoff) && !issue.CreatedAt.After(cutoff) {
			activity.IssuesUpdated++
		}
	}

	return activity
}

// getAssignedStatus returns status summary for issues assigned to a specific user
func getAssignedStatus(assignee string) *StatusSummary {
	if store == nil {
		return nil
	}

	ctx := context.Background()

	// Filter by assignee
	assigneePtr := assignee
	filter := types.IssueFilter{
		Assignee: &assigneePtr,
	}

	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil
	}

	summary := &StatusSummary{
		TotalIssues: len(issues),
	}

	// Count by status
	for _, issue := range issues {
		switch issue.Status {
		case types.StatusOpen:
			summary.OpenIssues++
		case types.StatusInProgress:
			summary.InProgressIssues++
		case types.StatusBlocked:
			summary.BlockedIssues++
		case types.StatusClosed:
			summary.ClosedIssues++
		}
	}

	// Get ready work count for this assignee
	readyFilter := types.WorkFilter{
		Assignee: &assigneePtr,
	}
	readyIssues, err := store.GetReadyWork(ctx, readyFilter)
	if err == nil {
		summary.ReadyIssues = len(readyIssues)
	}

	return summary
}

func init() {
	statusCmd.Flags().Bool("all", false, "Show all issues (default behavior)")
	statusCmd.Flags().Bool("assigned", false, "Show issues assigned to current user")
	// Note: --json flag is defined as a persistent flag in main.go, not here
	rootCmd.AddCommand(statusCmd)
}
