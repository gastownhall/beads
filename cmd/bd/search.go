package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/util"
)

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search issues by text query",
	Long: `Search issues across title, description, and ID.

Examples:
  bd search "authentication bug"
  bd search "login" --status open
  bd search "database" --label backend --limit 10
  bd search --query "performance" --assignee alice
  bd search "bd-5q" # Search by partial ID`,
	Run: func(cmd *cobra.Command, args []string) {
		// Get query from args or --query flag
		queryFlag, _ := cmd.Flags().GetString("query")
		var query string
		if len(args) > 0 {
			query = strings.Join(args, " ")
		} else if queryFlag != "" {
			query = queryFlag
		}

		// If no query provided, show help
		if query == "" {
			fmt.Fprintf(os.Stderr, "Error: search query is required\n")
			// #nosec G104 -- cmd.Help() error intentionally ignored. We're already in an
			// error path (missing query) and will exit(1) regardless. Help() errors are
			// rare (I/O failures) and don't affect the outcome. See TestSearchCommand_HelpErrorHandling
			cmd.Help()
			os.Exit(1)
		}

		// Get filter flags
		status, _ := cmd.Flags().GetString("status")
		assignee, _ := cmd.Flags().GetString("assignee")
		issueType, _ := cmd.Flags().GetString("type")
		limit, _ := cmd.Flags().GetInt("limit")
		labels, _ := cmd.Flags().GetStringSlice("label")
		labelsAny, _ := cmd.Flags().GetStringSlice("label-any")
		longFormat, _ := cmd.Flags().GetBool("long")

		// Normalize labels
		labels = util.NormalizeLabels(labels)
		labelsAny = util.NormalizeLabels(labelsAny)

		// Build filter
		filter := types.IssueFilter{
			Limit: limit,
		}

		if status != "" && status != "all" {
			s := types.Status(status)
			filter.Status = &s
		}

		if assignee != "" {
			filter.Assignee = &assignee
		}

		if issueType != "" {
			t := types.IssueType(issueType)
			filter.IssueType = &t
		}

		if len(labels) > 0 {
			filter.Labels = labels
		}

		if len(labelsAny) > 0 {
			filter.LabelsAny = labelsAny
		}

		ctx := rootCtx

		// Check database freshness before reading (skip when using daemon)
		if daemonClient == nil {
			if err := ensureDatabaseFresh(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			listArgs := &rpc.ListArgs{
				Query:     query, // This will search title/description/id with OR logic
				Status:    status,
				IssueType: issueType,
				Assignee:  assignee,
				Limit:     limit,
			}

			if len(labels) > 0 {
				listArgs.Labels = labels
			}

			if len(labelsAny) > 0 {
				listArgs.LabelsAny = labelsAny
			}

			resp, err := daemonClient.List(listArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			if jsonOutput {
				var issuesWithCounts []*types.IssueWithCounts
				if err := json.Unmarshal(resp.Data, &issuesWithCounts); err != nil {
					fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
					os.Exit(1)
				}
				outputJSON(issuesWithCounts)
				return
			}

			var issues []*types.Issue
			if err := json.Unmarshal(resp.Data, &issues); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
				os.Exit(1)
			}

			outputSearchResults(issues, query, longFormat)
			return
		}

		// Direct mode - search using store
		// The query parameter in SearchIssues already searches across title, description, and id
		issues, err := store.SearchIssues(ctx, query, filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// If no issues found, check if git has issues and auto-import
		if len(issues) == 0 {
			if checkAndAutoImport(ctx, store) {
				// Re-run the search after import
				issues, err = store.SearchIssues(ctx, query, filter)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			}
		}

		if jsonOutput {
			// Get labels and dependency counts
			issueIDs := make([]string, len(issues))
			for i, issue := range issues {
				issueIDs[i] = issue.ID
			}
			labelsMap, _ := store.GetLabelsForIssues(ctx, issueIDs)
			depCounts, _ := store.GetDependencyCounts(ctx, issueIDs)

			// Populate labels
			for _, issue := range issues {
				issue.Labels = labelsMap[issue.ID]
			}

			// Build response with counts
			issuesWithCounts := make([]*types.IssueWithCounts, len(issues))
			for i, issue := range issues {
				counts := depCounts[issue.ID]
				if counts == nil {
					counts = &types.DependencyCounts{DependencyCount: 0, DependentCount: 0}
				}
				issuesWithCounts[i] = &types.IssueWithCounts{
					Issue:           issue,
					DependencyCount: counts.DependencyCount,
					DependentCount:  counts.DependentCount,
				}
			}
			outputJSON(issuesWithCounts)
			return
		}

		// Load labels for display
		issueIDs := make([]string, len(issues))
		for i, issue := range issues {
			issueIDs[i] = issue.ID
		}
		labelsMap, _ := store.GetLabelsForIssues(ctx, issueIDs)
		for _, issue := range issues {
			issue.Labels = labelsMap[issue.ID]
		}

		outputSearchResults(issues, query, longFormat)
	},
}

// outputSearchResults formats and displays search results
func outputSearchResults(issues []*types.Issue, query string, longFormat bool) {
	if len(issues) == 0 {
		fmt.Printf("No issues found matching '%s'\n", query)
		return
	}

	if longFormat {
		// Long format: multi-line with details
		fmt.Printf("\nFound %d issues matching '%s':\n\n", len(issues), query)
		for _, issue := range issues {
			fmt.Printf("%s [P%d] [%s] %s\n", issue.ID, issue.Priority, issue.IssueType, issue.Status)
			fmt.Printf("  %s\n", issue.Title)
			if issue.Assignee != "" {
				fmt.Printf("  Assignee: %s\n", issue.Assignee)
			}
			if len(issue.Labels) > 0 {
				fmt.Printf("  Labels: %v\n", issue.Labels)
			}
			fmt.Println()
		}
	} else {
		// Compact format: one line per issue
		fmt.Printf("Found %d issues matching '%s':\n", len(issues), query)
		for _, issue := range issues {
			labelsStr := ""
			if len(issue.Labels) > 0 {
				labelsStr = fmt.Sprintf(" %v", issue.Labels)
			}
			assigneeStr := ""
			if issue.Assignee != "" {
				assigneeStr = fmt.Sprintf(" @%s", issue.Assignee)
			}
			fmt.Printf("%s [P%d] [%s] %s%s%s - %s\n",
				issue.ID, issue.Priority, issue.IssueType, issue.Status,
				assigneeStr, labelsStr, issue.Title)
		}
	}
}

func init() {
	searchCmd.Flags().String("query", "", "Search query (alternative to positional argument)")
	searchCmd.Flags().StringP("status", "s", "", "Filter by status (open, in_progress, blocked, closed)")
	searchCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	searchCmd.Flags().StringP("type", "t", "", "Filter by type (bug, feature, task, epic, chore)")
	searchCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL)")
	searchCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE)")
	searchCmd.Flags().IntP("limit", "n", 50, "Limit results (default: 50)")
	searchCmd.Flags().Bool("long", false, "Show detailed multi-line output for each issue")

	rootCmd.AddCommand(searchCmd)
}
