package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// StatusOutput represents the complete status output
type StatusOutput struct {
	Summary        *types.Statistics      `json:"summary"`
	RecentActivity *RecentActivitySummary `json:"recent_activity,omitempty"`
}

// RecentActivitySummary represents activity from git history
type RecentActivitySummary struct {
	HoursTracked   int `json:"hours_tracked"`
	CommitCount    int `json:"commit_count"`
	IssuesCreated  int `json:"issues_created"`
	IssuesClosed   int `json:"issues_closed"`
	IssuesUpdated  int `json:"issues_updated"`
	IssuesReopened int `json:"issues_reopened"`
	TotalChanges   int `json:"total_changes"`
}

var statusCmd = &cobra.Command{
	Use:     "status",
	GroupID: "views",
	Aliases: []string{"stats"},
	Short:   "Show issue database overview and statistics",
	Long: `Show a quick snapshot of the issue database state and statistics.

This command provides a summary of issue counts by state (open, in_progress,
blocked, closed), ready work, extended statistics (tombstones, pinned issues,
average lead time), and recent activity over the last 24 hours from git history.

Similar to how 'git status' shows working tree state, 'bd status' gives you
a quick overview of your issue database without needing multiple queries.

Use cases:
  - Quick project health check
  - Onboarding for new contributors
  - Integration with shell prompts or CI/CD
  - Daily standup reference

Examples:
  bd status                    # Show summary with activity
  bd status --no-activity      # Skip git activity (faster)
  bd status --json             # JSON format output
  bd status --assigned         # Show issues assigned to current user
  bd stats                     # Alias for bd status`,
	Run: func(cmd *cobra.Command, args []string) {
		showAll, _ := cmd.Flags().GetBool("all")
		showAssigned, _ := cmd.Flags().GetBool("assigned")
		noActivity, _ := cmd.Flags().GetBool("no-activity")
		jsonFormat, _ := cmd.Flags().GetBool("json")

		// Override global jsonOutput if --json flag is set
		if jsonFormat {
			jsonOutput = true
		}

		// Get statistics
		var stats *types.Statistics
		var err error

		// Check database freshness before reading (bd-2q6d, bd-c4rq)
		// Skip check when using daemon (daemon auto-imports on staleness)
		ctx := rootCtx
		if daemonClient == nil {
			if err := ensureDatabaseFresh(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

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
			ctx := rootCtx
			stats, err = store.GetStatistics(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		// Filter by assignee if requested (overrides stats with filtered counts)
		if showAssigned {
			stats = getAssignedStatistics(actor)
			if stats == nil {
				fmt.Fprintf(os.Stderr, "Error: failed to get assigned statistics\n")
				os.Exit(1)
			}
		}

		// Get recent activity from git history (last 24 hours) unless --no-activity
		var recentActivity *RecentActivitySummary
		if !noActivity {
			recentActivity = getGitActivity(24)
		}

		output := &StatusOutput{
			Summary:        stats,
			RecentActivity: recentActivity,
		}

		// JSON output
		if jsonOutput {
			outputJSON(output)
			return
		}

		// Human-readable colorized output using semantic ui package
		fmt.Printf("\n%s Issue Database Status\n\n", ui.RenderAccent("📊"))
		fmt.Printf("Summary:\n")
		fmt.Printf("  Total Issues:           %d\n", stats.TotalIssues)
		fmt.Printf("  Open:                   %s\n", ui.RenderPass(fmt.Sprintf("%d", stats.OpenIssues)))
		fmt.Printf("  In Progress:            %s\n", ui.RenderWarn(fmt.Sprintf("%d", stats.InProgressIssues)))
		fmt.Printf("  Blocked:                %s\n", ui.RenderFail(fmt.Sprintf("%d", stats.BlockedIssues)))
		fmt.Printf("  Closed:                 %d\n", stats.ClosedIssues)
		fmt.Printf("  Ready to Work:          %s\n", ui.RenderPass(fmt.Sprintf("%d", stats.ReadyIssues)))

		// Extended statistics (only show if non-zero)
		hasExtended := stats.TombstoneIssues > 0 || stats.PinnedIssues > 0 ||
			stats.EpicsEligibleForClosure > 0 || stats.AverageLeadTime > 0
		if hasExtended {
			fmt.Printf("\nExtended:\n")
			if stats.TombstoneIssues > 0 {
				fmt.Printf("  Deleted:                %d (tombstones)\n", stats.TombstoneIssues)
			}
			if stats.PinnedIssues > 0 {
				fmt.Printf("  Pinned:                 %d\n", stats.PinnedIssues)
			}
			if stats.EpicsEligibleForClosure > 0 {
				fmt.Printf("  Epics Ready to Close:   %s\n", ui.RenderPass(fmt.Sprintf("%d", stats.EpicsEligibleForClosure)))
			}
			if stats.AverageLeadTime > 0 {
				fmt.Printf("  Avg Lead Time:          %.1f hours\n", stats.AverageLeadTime)
			}
		}

		if recentActivity != nil {
			fmt.Printf("\nRecent Activity (last %d hours):\n", recentActivity.HoursTracked)
			fmt.Printf("  Commits:                %d\n", recentActivity.CommitCount)
			fmt.Printf("  Total Changes:          %d\n", recentActivity.TotalChanges)
			fmt.Printf("  Issues Created:         %d\n", recentActivity.IssuesCreated)
			fmt.Printf("  Issues Closed:          %d\n", recentActivity.IssuesClosed)
			fmt.Printf("  Issues Reopened:        %d\n", recentActivity.IssuesReopened)
			fmt.Printf("  Issues Updated:         %d\n", recentActivity.IssuesUpdated)
		}

		// Show hint for more details
		fmt.Printf("\nFor more details, use 'bd list' to see individual issues.\n")
		fmt.Println()

		// Suppress showAll flag (it's the default behavior, included for CLI familiarity)
		_ = showAll
	},
}

// getGitActivity calculates activity stats from git log of issues.jsonl
func getGitActivity(hours int) *RecentActivitySummary {
	activity := &RecentActivitySummary{
		HoursTracked: hours,
	}

	// Run git log to get patches for the last N hours
	since := fmt.Sprintf("%d hours ago", hours)
	cmd := exec.Command("git", "log", "--since="+since, "--numstat", "--pretty=format:%H", ".beads/issues.jsonl") // #nosec G204 -- bounded arguments for local git history inspection

	output, err := cmd.Output()
	if err != nil {
		// Git log failed (might not be a git repo or no commits)
		return nil
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	commitCount := 0

	for scanner.Scan() {
		line := scanner.Text()

		// Empty lines separate commits
		if line == "" {
			continue
		}

		// Commit hash line
		if !strings.Contains(line, "\t") {
			commitCount++
			continue
		}

		// numstat line format: "additions\tdeletions\tfilename"
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}

		// For JSONL files, each added line is a new/updated issue
		// We need to analyze the actual diff to understand what changed
	}

	// Get detailed diff to analyze changes
	cmd = exec.Command("git", "log", "--since="+since, "-p", ".beads/issues.jsonl") // #nosec G204 -- bounded arguments for local git history inspection
	output, err = cmd.Output()
	if err != nil {
		return nil
	}

	scanner = bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()

		// Look for added lines in diff (lines starting with +)
		if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") {
			continue
		}

		// Remove the + prefix
		jsonLine := strings.TrimPrefix(line, "+")

		// Skip empty lines
		if strings.TrimSpace(jsonLine) == "" {
			continue
		}

		// Try to parse as issue JSON
		var issue types.Issue
		if err := json.Unmarshal([]byte(jsonLine), &issue); err != nil {
			continue
		}

		activity.TotalChanges++

		// Analyze the change type based on timestamps and status
		// Created recently if created_at is close to now
		if time.Since(issue.CreatedAt) < time.Duration(hours)*time.Hour {
			activity.IssuesCreated++
		} else if issue.Status == types.StatusClosed && issue.ClosedAt != nil {
			// Closed recently if closed_at is close to now
			if time.Since(*issue.ClosedAt) < time.Duration(hours)*time.Hour {
				activity.IssuesClosed++
			} else {
				activity.IssuesUpdated++
			}
		} else if issue.Status != types.StatusClosed {
			// Check if this was a reopen (status changed from closed to open/in_progress)
			// We'd need to look at the removed line to know for sure, but for now
			// we'll just count it as an update
			activity.IssuesUpdated++
		}
	}

	activity.CommitCount = commitCount
	return activity
}

// getAssignedStatistics returns statistics for issues assigned to a specific user
func getAssignedStatistics(assignee string) *types.Statistics {
	if store == nil {
		return nil
	}

	ctx := rootCtx

	// Filter by assignee
	assigneePtr := assignee
	filter := types.IssueFilter{
		Assignee: &assigneePtr,
	}

	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil
	}

	stats := &types.Statistics{
		TotalIssues: len(issues),
	}

	// Count by status
	for _, issue := range issues {
		switch issue.Status {
		case types.StatusOpen:
			stats.OpenIssues++
		case types.StatusInProgress:
			stats.InProgressIssues++
		case types.StatusBlocked:
			stats.BlockedIssues++
		case types.StatusDeferred:
			stats.DeferredIssues++
		case types.StatusClosed:
			stats.ClosedIssues++
		}
	}

	// Get ready work count for this assignee
	readyFilter := types.WorkFilter{
		Assignee: &assigneePtr,
	}
	readyIssues, err := store.GetReadyWork(ctx, readyFilter)
	if err == nil {
		stats.ReadyIssues = len(readyIssues)
	}

	return stats
}

func init() {
	statusCmd.Flags().Bool("all", false, "Show all issues (default behavior)")
	statusCmd.Flags().Bool("assigned", false, "Show issues assigned to current user")
	statusCmd.Flags().Bool("no-activity", false, "Skip git activity tracking (faster)")
	// Note: --json flag is defined as a persistent flag in main.go, not here
	rootCmd.AddCommand(statusCmd)
}
