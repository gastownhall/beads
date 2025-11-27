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
	Use:   "status",
	Short: "Show issue database overview",
	Long: `Show a quick snapshot of the issue database state.

This command provides a summary of issue counts by state (open, in-progress,
blocked, closed), ready work, and recent activity over the last 24 hours from git history.

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

		// Build summary
		summary := &StatusSummary{
			TotalIssues:      stats.TotalIssues,
			OpenIssues:       stats.OpenIssues,
			InProgressIssues: stats.InProgressIssues,
			BlockedIssues:    stats.BlockedIssues,
			ClosedIssues:     stats.ClosedIssues,
			ReadyIssues:      stats.ReadyIssues,
		}

		// Get recent activity from git history (last 24 hours)
		var recentActivity *RecentActivitySummary
		recentActivity = getGitActivity(24)

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
			fmt.Printf("\nRecent Activity (last %d hours, from git history):\n", recentActivity.HoursTracked)
			fmt.Printf("  Commits:           %d\n", recentActivity.CommitCount)
			fmt.Printf("  Total Changes:     %d\n", recentActivity.TotalChanges)
			fmt.Printf("  Issues Created:    %d\n", recentActivity.IssuesCreated)
			fmt.Printf("  Issues Closed:     %d\n", recentActivity.IssuesClosed)
			fmt.Printf("  Issues Reopened:   %d\n", recentActivity.IssuesReopened)
			fmt.Printf("  Issues Updated:    %d\n", recentActivity.IssuesUpdated)
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

// getAssignedStatus returns status summary for issues assigned to a specific user
func getAssignedStatus(assignee string) *StatusSummary {
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
