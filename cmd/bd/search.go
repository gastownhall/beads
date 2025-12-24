package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/util"
	"github.com/steveyegge/beads/internal/validation"
)

var searchCmd = &cobra.Command{
	Use:     "search [query]",
	GroupID: "issues",
	Short:   "Search issues by text query",
	Long: `Search issues across title, description, and ID.

Examples:
  bd search "authentication bug"
  bd search "login" --status open
  bd search "database" --label backend --limit 10
  bd search --query "performance" --assignee alice
  bd search "bd-5q" # Search by partial ID
  bd search "security" --priority 1                        # Exact priority match
  bd search "security" --priority-min 0 --priority-max 2   # Priority range
  bd search "bug" --created-after 2025-01-01
  bd search "refactor" --updated-after 2025-01-01 --priority-min 1
  bd search "bug" --desc-contains "authentication"         # Search in description
  bd search "" --empty-description                         # Issues without description
  bd search "" --no-assignee                               # Unassigned issues
  bd search "" --no-labels                                 # Issues without labels
  bd search "bug" --sort priority
  bd search "task" --sort created --reverse`,
	Run: func(cmd *cobra.Command, args []string) {
		// Get query from args or --query flag
		queryFlag, _ := cmd.Flags().GetString("query")
		var query string
		if len(args) > 0 {
			query = strings.Join(args, " ")
		} else if queryFlag != "" {
			query = queryFlag
		}

		// Check if any filter flags are set (allows empty query with filters)
		hasFilters := cmd.Flags().Changed("status") ||
			cmd.Flags().Changed("priority") ||
			cmd.Flags().Changed("assignee") ||
			cmd.Flags().Changed("type") ||
			cmd.Flags().Changed("label") ||
			cmd.Flags().Changed("label-any") ||
			cmd.Flags().Changed("created-after") ||
			cmd.Flags().Changed("created-before") ||
			cmd.Flags().Changed("updated-after") ||
			cmd.Flags().Changed("updated-before") ||
			cmd.Flags().Changed("closed-after") ||
			cmd.Flags().Changed("closed-before") ||
			cmd.Flags().Changed("priority-min") ||
			cmd.Flags().Changed("priority-max") ||
			cmd.Flags().Changed("title-contains") ||
			cmd.Flags().Changed("desc-contains") ||
			cmd.Flags().Changed("notes-contains") ||
			cmd.Flags().Changed("empty-description") ||
			cmd.Flags().Changed("no-assignee") ||
			cmd.Flags().Changed("no-labels")

		// If no query and no filters provided, show help
		if query == "" && !hasFilters {
			fmt.Fprintf(os.Stderr, "Error: search query or filter is required\n")
			if err := cmd.Help(); err != nil {
				fmt.Fprintf(os.Stderr, "Error displaying help: %v\n", err)
			}
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
		sortBy, _ := cmd.Flags().GetString("sort")
		reverse, _ := cmd.Flags().GetBool("reverse")

		// Pattern matching flags
		titleContains, _ := cmd.Flags().GetString("title-contains")
		descContains, _ := cmd.Flags().GetString("desc-contains")
		notesContains, _ := cmd.Flags().GetString("notes-contains")

		// Date range flags
		createdAfter, _ := cmd.Flags().GetString("created-after")
		createdBefore, _ := cmd.Flags().GetString("created-before")
		updatedAfter, _ := cmd.Flags().GetString("updated-after")
		updatedBefore, _ := cmd.Flags().GetString("updated-before")
		closedAfter, _ := cmd.Flags().GetString("closed-after")
		closedBefore, _ := cmd.Flags().GetString("closed-before")

		// Empty/null check flags
		emptyDesc, _ := cmd.Flags().GetBool("empty-description")
		noAssignee, _ := cmd.Flags().GetBool("no-assignee")
		noLabels, _ := cmd.Flags().GetBool("no-labels")

		// Priority range flags
		priorityMinStr, _ := cmd.Flags().GetString("priority-min")
		priorityMaxStr, _ := cmd.Flags().GetString("priority-max")

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

		// Exact priority match (use Changed() to properly handle P0)
		if cmd.Flags().Changed("priority") {
			priorityStr, _ := cmd.Flags().GetString("priority")
			priority, err := validation.ValidatePriority(priorityStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			filter.Priority = &priority
		}

		// Pattern matching
		if titleContains != "" {
			filter.TitleContains = titleContains
		}
		if descContains != "" {
			filter.DescriptionContains = descContains
		}
		if notesContains != "" {
			filter.NotesContains = notesContains
		}

		// Empty/null checks
		if emptyDesc {
			filter.EmptyDescription = true
		}
		if noAssignee {
			filter.NoAssignee = true
		}
		if noLabels {
			filter.NoLabels = true
		}

		// Date ranges
		if createdAfter != "" {
			t, err := parseTimeFlag(createdAfter)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --created-after: %v\n", err)
				os.Exit(1)
			}
			filter.CreatedAfter = &t
		}
		if createdBefore != "" {
			t, err := parseTimeFlag(createdBefore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --created-before: %v\n", err)
				os.Exit(1)
			}
			filter.CreatedBefore = &t
		}
		if updatedAfter != "" {
			t, err := parseTimeFlag(updatedAfter)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --updated-after: %v\n", err)
				os.Exit(1)
			}
			filter.UpdatedAfter = &t
		}
		if updatedBefore != "" {
			t, err := parseTimeFlag(updatedBefore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --updated-before: %v\n", err)
				os.Exit(1)
			}
			filter.UpdatedBefore = &t
		}
		if closedAfter != "" {
			t, err := parseTimeFlag(closedAfter)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --closed-after: %v\n", err)
				os.Exit(1)
			}
			filter.ClosedAfter = &t
		}
		if closedBefore != "" {
			t, err := parseTimeFlag(closedBefore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --closed-before: %v\n", err)
				os.Exit(1)
			}
			filter.ClosedBefore = &t
		}

		// Priority ranges
		if cmd.Flags().Changed("priority-min") {
			priorityMin, err := validation.ValidatePriority(priorityMinStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --priority-min: %v\n", err)
				os.Exit(1)
			}
			filter.PriorityMin = &priorityMin
		}
		if cmd.Flags().Changed("priority-max") {
			priorityMax, err := validation.ValidatePriority(priorityMaxStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --priority-max: %v\n", err)
				os.Exit(1)
			}
			filter.PriorityMax = &priorityMax
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

			// Exact priority match
			if filter.Priority != nil {
				listArgs.Priority = filter.Priority
			}

			// Pattern matching
			listArgs.TitleContains = titleContains
			listArgs.DescriptionContains = descContains
			listArgs.NotesContains = notesContains

			// Empty/null checks
			listArgs.EmptyDescription = filter.EmptyDescription
			listArgs.NoAssignee = filter.NoAssignee
			listArgs.NoLabels = filter.NoLabels

			// Date ranges
			if filter.CreatedAfter != nil {
				listArgs.CreatedAfter = filter.CreatedAfter.Format(time.RFC3339)
			}
			if filter.CreatedBefore != nil {
				listArgs.CreatedBefore = filter.CreatedBefore.Format(time.RFC3339)
			}
			if filter.UpdatedAfter != nil {
				listArgs.UpdatedAfter = filter.UpdatedAfter.Format(time.RFC3339)
			}
			if filter.UpdatedBefore != nil {
				listArgs.UpdatedBefore = filter.UpdatedBefore.Format(time.RFC3339)
			}
			if filter.ClosedAfter != nil {
				listArgs.ClosedAfter = filter.ClosedAfter.Format(time.RFC3339)
			}
			if filter.ClosedBefore != nil {
				listArgs.ClosedBefore = filter.ClosedBefore.Format(time.RFC3339)
			}

			// Priority range
			listArgs.PriorityMin = filter.PriorityMin
			listArgs.PriorityMax = filter.PriorityMax

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

			// Apply sorting
			sortIssues(issues, sortBy, reverse)

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

		// Apply sorting
		sortIssues(issues, sortBy, reverse)

		if jsonOutput {
			// Get labels and dependency counts
			issueIDs := make([]string, len(issues))
			for i, issue := range issues {
				issueIDs[i] = issue.ID
			}
			labelsMap, err := store.GetLabelsForIssues(ctx, issueIDs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to get labels: %v\n", err)
				labelsMap = make(map[string][]string)
			}
			depCounts, err := store.GetDependencyCounts(ctx, issueIDs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to get dependency counts: %v\n", err)
				depCounts = make(map[string]*types.DependencyCounts)
			}

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
	searchCmd.Flags().StringP("status", "s", "", "Filter by status (open, in_progress, blocked, deferred, closed)")
	registerPriorityFlag(searchCmd, "")
	searchCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	searchCmd.Flags().StringP("type", "t", "", "Filter by type (bug, feature, task, epic, chore, merge-request, molecule, gate)")
	searchCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL)")
	searchCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE)")
	searchCmd.Flags().IntP("limit", "n", 50, "Limit results (default: 50)")
	searchCmd.Flags().Bool("long", false, "Show detailed multi-line output for each issue")
	searchCmd.Flags().String("sort", "", "Sort by field: priority, created, updated, closed, status, id, title, type, assignee")
	searchCmd.Flags().BoolP("reverse", "r", false, "Reverse sort order")

	// Pattern matching flags
	searchCmd.Flags().String("title-contains", "", "Filter by title substring (case-insensitive)")
	searchCmd.Flags().String("desc-contains", "", "Filter by description substring (case-insensitive)")
	searchCmd.Flags().String("notes-contains", "", "Filter by notes substring (case-insensitive)")

	// Date range flags
	searchCmd.Flags().String("created-after", "", "Filter issues created after date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("created-before", "", "Filter issues created before date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("updated-after", "", "Filter issues updated after date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("updated-before", "", "Filter issues updated before date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("closed-after", "", "Filter issues closed after date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("closed-before", "", "Filter issues closed before date (YYYY-MM-DD or RFC3339)")

	// Empty/null check flags
	searchCmd.Flags().Bool("empty-description", false, "Filter issues with empty or missing description")
	searchCmd.Flags().Bool("no-assignee", false, "Filter issues with no assignee")
	searchCmd.Flags().Bool("no-labels", false, "Filter issues with no labels")

	// Priority range flags
	searchCmd.Flags().String("priority-min", "", "Filter by minimum priority (inclusive, 0-4 or P0-P4)")
	searchCmd.Flags().String("priority-max", "", "Filter by maximum priority (inclusive, 0-4 or P0-P4)")

	rootCmd.AddCommand(searchCmd)
}
