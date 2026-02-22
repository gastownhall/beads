package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
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
  bd search "security" --priority-min 0 --priority-max 2
  bd search "bug" --created-after 2025-01-01
  bd search "refactor" --updated-after 2025-01-01 --priority-min 1
  bd search "bug" --sort priority
  bd search "task" --sort created --reverse
  bd search "api" --desc-contains "endpoint"
  bd search "cleanup" --no-assignee --no-labels`,
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
			if err := cmd.Help(); err != nil {
				fmt.Fprintf(os.Stderr, "Error displaying help: %v\n", err)
			}
			FatalError("search query is required")
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

		// Date range flags
		createdAfter, _ := cmd.Flags().GetString("created-after")
		createdBefore, _ := cmd.Flags().GetString("created-before")
		updatedAfter, _ := cmd.Flags().GetString("updated-after")
		updatedBefore, _ := cmd.Flags().GetString("updated-before")
		closedAfter, _ := cmd.Flags().GetString("closed-after")
		closedBefore, _ := cmd.Flags().GetString("closed-before")

		// Priority range flags
		priorityMinStr, _ := cmd.Flags().GetString("priority-min")
		priorityMaxStr, _ := cmd.Flags().GetString("priority-max")

		// Pattern matching flags
		descContains, _ := cmd.Flags().GetString("desc-contains")
		notesContains, _ := cmd.Flags().GetString("notes-contains")

		// Empty/null check flags
		emptyDesc, _ := cmd.Flags().GetBool("empty-description")
		noAssignee, _ := cmd.Flags().GetBool("no-assignee")
		noLabels, _ := cmd.Flags().GetBool("no-labels")

		// Normalize labels
		labels = utils.NormalizeLabels(labels)
		labelsAny = utils.NormalizeLabels(labelsAny)

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

		// Pattern matching
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
				FatalError("parsing --created-after: %v", err)
			}
			filter.CreatedAfter = &t
		}
		if createdBefore != "" {
			t, err := parseTimeFlag(createdBefore)
			if err != nil {
				FatalError("parsing --created-before: %v", err)
			}
			filter.CreatedBefore = &t
		}
		if updatedAfter != "" {
			t, err := parseTimeFlag(updatedAfter)
			if err != nil {
				FatalError("parsing --updated-after: %v", err)
			}
			filter.UpdatedAfter = &t
		}
		if updatedBefore != "" {
			t, err := parseTimeFlag(updatedBefore)
			if err != nil {
				FatalError("parsing --updated-before: %v", err)
			}
			filter.UpdatedBefore = &t
		}
		if closedAfter != "" {
			t, err := parseTimeFlag(closedAfter)
			if err != nil {
				FatalError("parsing --closed-after: %v", err)
			}
			filter.ClosedAfter = &t
		}
		if closedBefore != "" {
			t, err := parseTimeFlag(closedBefore)
			if err != nil {
				FatalError("parsing --closed-before: %v", err)
			}
			filter.ClosedBefore = &t
		}

		// Priority ranges
		if cmd.Flags().Changed("priority-min") {
			priorityMin, err := validation.ValidatePriority(priorityMinStr)
			if err != nil {
				FatalError("parsing --priority-min: %v", err)
			}
			filter.PriorityMin = &priorityMin
		}
		if cmd.Flags().Changed("priority-max") {
			priorityMax, err := validation.ValidatePriority(priorityMaxStr)
			if err != nil {
				FatalError("parsing --priority-max: %v", err)
			}
			filter.PriorityMax = &priorityMax
		}

		// Metadata filters (GH#1406)
		metadataFieldFlags, _ := cmd.Flags().GetStringArray("metadata-field")
		if len(metadataFieldFlags) > 0 {
			filter.MetadataFields = make(map[string]string, len(metadataFieldFlags))
			for _, mf := range metadataFieldFlags {
				k, v, ok := strings.Cut(mf, "=")
				if !ok || k == "" {
					fmt.Fprintf(os.Stderr, "Error: invalid --metadata-field: expected key=value, got %q\n", mf)
					os.Exit(1)
				}
				if err := storage.ValidateMetadataKey(k); err != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid --metadata-field key: %v\n", err)
					os.Exit(1)
				}
				filter.MetadataFields[k] = v
			}
		}
		hasMetadataKey, _ := cmd.Flags().GetString("has-metadata-key")
		if hasMetadataKey != "" {
			if err := storage.ValidateMetadataKey(hasMetadataKey); err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --has-metadata-key: %v\n", err)
				os.Exit(1)
			}
			filter.HasMetadataKey = hasMetadataKey
		}

		ctx := rootCtx

		// Direct mode - search using store
		// The query parameter in SearchIssues already searches across title, description, and id
		issues, err := store.SearchIssues(ctx, query, filter)
		if err != nil {
			FatalError("%v", err)
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
			commentCounts, err := store.GetCommentCounts(ctx, issueIDs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to get comment counts: %v\n", err)
				commentCounts = make(map[string]int)
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
					CommentCount:    commentCounts[issue.ID],
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
	searchCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	searchCmd.Flags().StringP("type", "t", "", "Filter by type (bug, feature, task, epic, chore, decision, merge-request, molecule, gate)")
	searchCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL)")
	searchCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE)")
	searchCmd.Flags().IntP("limit", "n", 50, "Limit results (default: 50)")
	searchCmd.Flags().Bool("long", false, "Show detailed multi-line output for each issue")
	searchCmd.Flags().String("sort", "", "Sort by field: priority, created, updated, closed, status, id, title, type, assignee")
	searchCmd.Flags().BoolP("reverse", "r", false, "Reverse sort order")

	// Date range flags
	searchCmd.Flags().String("created-after", "", "Filter issues created after date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("created-before", "", "Filter issues created before date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("updated-after", "", "Filter issues updated after date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("updated-before", "", "Filter issues updated before date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("closed-after", "", "Filter issues closed after date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("closed-before", "", "Filter issues closed before date (YYYY-MM-DD or RFC3339)")

	// Priority range flags
	searchCmd.Flags().String("priority-min", "", "Filter by minimum priority (inclusive, 0-4 or P0-P4)")
	searchCmd.Flags().String("priority-max", "", "Filter by maximum priority (inclusive, 0-4 or P0-P4)")

	// Pattern matching flags
	searchCmd.Flags().String("desc-contains", "", "Filter by description substring (case-insensitive)")
	searchCmd.Flags().String("notes-contains", "", "Filter by notes substring (case-insensitive)")

	// Empty/null check flags
	searchCmd.Flags().Bool("empty-description", false, "Filter issues with empty or missing description")
	searchCmd.Flags().Bool("no-assignee", false, "Filter issues with no assignee")
	searchCmd.Flags().Bool("no-labels", false, "Filter issues with no labels")

	// Metadata filtering (GH#1406)
	searchCmd.Flags().StringArray("metadata-field", nil, "Filter by metadata field (key=value, repeatable)")
	searchCmd.Flags().String("has-metadata-key", "", "Filter issues that have this metadata key set")

	rootCmd.AddCommand(searchCmd)
}
