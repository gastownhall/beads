package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/util"
	"github.com/steveyegge/beads/internal/validation"
)

// parseTimeFlag parses time strings in multiple formats
func parseTimeFlag(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		"2006-01-02",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	}

	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("unable to parse time %q (try formats: 2006-01-02, 2006-01-02T15:04:05, or RFC3339)", s)
}

// sortIssues sorts a slice of issues by the specified field and direction
func sortIssues(issues []*types.Issue, sortBy string, reverse bool) {
	if sortBy == "" {
		return
	}

	sort.Slice(issues, func(i, j int) bool {
		var less bool

		switch sortBy {
		case "priority":
			// Lower priority numbers come first (P0 > P1 > P2 > P3 > P4)
			less = issues[i].Priority < issues[j].Priority
		case "created":
			// Default: newest first (descending)
			less = issues[i].CreatedAt.After(issues[j].CreatedAt)
		case "updated":
			// Default: newest first (descending)
			less = issues[i].UpdatedAt.After(issues[j].UpdatedAt)
		case "closed":
			// Default: newest first (descending)
			// Handle nil ClosedAt values
			if issues[i].ClosedAt == nil && issues[j].ClosedAt == nil {
				less = false
			} else if issues[i].ClosedAt == nil {
				less = false // nil sorts last
			} else if issues[j].ClosedAt == nil {
				less = true // non-nil sorts before nil
			} else {
				less = issues[i].ClosedAt.After(*issues[j].ClosedAt)
			}
		case "status":
			less = issues[i].Status < issues[j].Status
		case "id":
			less = issues[i].ID < issues[j].ID
		case "title":
			less = strings.ToLower(issues[i].Title) < strings.ToLower(issues[j].Title)
		case "type":
			less = issues[i].IssueType < issues[j].IssueType
		case "assignee":
			less = issues[i].Assignee < issues[j].Assignee
		default:
			// Unknown sort field, no sorting
			less = false
		}

		if reverse {
			return !less
		}
		return less
	})
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List issues",
	Run: func(cmd *cobra.Command, args []string) {
		status, _ := cmd.Flags().GetString("status")
		assignee, _ := cmd.Flags().GetString("assignee")
		issueType, _ := cmd.Flags().GetString("type")
		limit, _ := cmd.Flags().GetInt("limit")
		formatStr, _ := cmd.Flags().GetString("format")
		labels, _ := cmd.Flags().GetStringSlice("label")
		labelsAny, _ := cmd.Flags().GetStringSlice("label-any")
		titleSearch, _ := cmd.Flags().GetString("title")
		idFilter, _ := cmd.Flags().GetString("id")
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
		
		// Use global jsonOutput set by PersistentPreRun

		// Normalize labels: trim, dedupe, remove empty
		labels = util.NormalizeLabels(labels)
	labelsAny = util.NormalizeLabels(labelsAny)

		filter := types.IssueFilter{
			Limit: limit,
		}
		if status != "" && status != "all" {
			s := types.Status(status)
			filter.Status = &s
		}
		// Use Changed() to properly handle P0 (priority=0)
		if cmd.Flags().Changed("priority") {
			priorityStr, _ := cmd.Flags().GetString("priority")
			priority, err := validation.ValidatePriority(priorityStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			filter.Priority = &priority
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
		if titleSearch != "" {
			filter.TitleSearch = titleSearch
		}
		if idFilter != "" {
			ids := util.NormalizeLabels(strings.Split(idFilter, ","))
			if len(ids) > 0 {
				filter.IDs = ids
			}
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
			listArgs := &rpc.ListArgs{
				Status:    status,
				IssueType: issueType,
				Assignee:  assignee,
				Limit:     limit,
			}
			if cmd.Flags().Changed("priority") {
				priorityStr, _ := cmd.Flags().GetString("priority")
				priority, err := validation.ValidatePriority(priorityStr)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				listArgs.Priority = &priority
			}
			if len(labels) > 0 {
				listArgs.Labels = labels
			}
			if len(labelsAny) > 0 {
				listArgs.LabelsAny = labelsAny
			}
			// Forward title search via Query field (searches title/description/id)
			if titleSearch != "" {
			 listArgs.Query = titleSearch
			}
			 if len(filter.IDs) > 0 {
			listArgs.IDs = filter.IDs
			 }
			
			// Pattern matching
			listArgs.TitleContains = titleContains
			listArgs.DescriptionContains = descContains
			listArgs.NotesContains = notesContains
			
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
			
			// Empty/null checks
			listArgs.EmptyDescription = filter.EmptyDescription
			listArgs.NoAssignee = filter.NoAssignee
			listArgs.NoLabels = filter.NoLabels
			
			// Priority range
			listArgs.PriorityMin = filter.PriorityMin
			listArgs.PriorityMax = filter.PriorityMax

			 resp, err := daemonClient.List(listArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			if jsonOutput {
				// For JSON output, preserve the full response with counts
				var issuesWithCounts []*types.IssueWithCounts
				if err := json.Unmarshal(resp.Data, &issuesWithCounts); err != nil {
					fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
					os.Exit(1)
				}
				outputJSON(issuesWithCounts)
				return
			}

			// Show upgrade notification if needed (bd-loka)
			maybeShowUpgradeNotification()

			var issues []*types.Issue
			if err := json.Unmarshal(resp.Data, &issues); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
				os.Exit(1)
			}

			// Apply sorting
			sortIssues(issues, sortBy, reverse)

			if longFormat {
				// Long format: multi-line with details
				fmt.Printf("\nFound %d issues:\n\n", len(issues))
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
			return
		}

		// Direct mode
		// ctx already created above for staleness check
		issues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
		}

	// If no issues found, check if git has issues and auto-import
	if len(issues) == 0 {
		if checkAndAutoImport(ctx, store) {
			// Re-run the query after import
			issues, err = store.SearchIssues(ctx, "", filter)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	}

		// Apply sorting
		sortIssues(issues, sortBy, reverse)

		// Handle format flag
		if formatStr != "" {
			if err := outputFormattedList(ctx, store, issues, formatStr); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		if jsonOutput {
			// Get labels and dependency counts in bulk (single query instead of N queries)
			issueIDs := make([]string, len(issues))
			for i, issue := range issues {
				issueIDs[i] = issue.ID
			}
			labelsMap, _ := store.GetLabelsForIssues(ctx, issueIDs)
			depCounts, _ := store.GetDependencyCounts(ctx, issueIDs)

			// Populate labels for JSON output
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

		// Show upgrade notification if needed (bd-loka)
		maybeShowUpgradeNotification()

		// Load labels in bulk for display
		issueIDs := make([]string, len(issues))
		for i, issue := range issues {
			issueIDs[i] = issue.ID
		}
		labelsMap, _ := store.GetLabelsForIssues(ctx, issueIDs)

		if longFormat {
			// Long format: multi-line with details
			fmt.Printf("\nFound %d issues:\n\n", len(issues))
			for _, issue := range issues {
				labels := labelsMap[issue.ID]

				fmt.Printf("%s [P%d] [%s] %s\n", issue.ID, issue.Priority, issue.IssueType, issue.Status)
				fmt.Printf("  %s\n", issue.Title)
				if issue.Assignee != "" {
					fmt.Printf("  Assignee: %s\n", issue.Assignee)
				}
				if len(labels) > 0 {
					fmt.Printf("  Labels: %v\n", labels)
				}
				fmt.Println()
			}
		} else {
			// Compact format: one line per issue
			for _, issue := range issues {
				labels := labelsMap[issue.ID]

				labelsStr := ""
				if len(labels) > 0 {
					labelsStr = fmt.Sprintf(" %v", labels)
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

		// Show tip after successful list (direct mode only)
		maybeShowTip(store)
	},
}

func init() {
	listCmd.Flags().StringP("status", "s", "", "Filter by status (open, in_progress, blocked, closed)")
	registerPriorityFlag(listCmd, "")
	listCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	listCmd.Flags().StringP("type", "t", "", "Filter by type (bug, feature, task, epic, chore)")
	listCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL). Can combine with --label-any")
	listCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE). Can combine with --label")
	listCmd.Flags().String("title", "", "Filter by title text (case-insensitive substring match)")
	listCmd.Flags().String("id", "", "Filter by specific issue IDs (comma-separated, e.g., bd-1,bd-5,bd-10)")
	listCmd.Flags().IntP("limit", "n", 0, "Limit results")
	listCmd.Flags().String("format", "", "Output format: 'digraph' (for golang.org/x/tools/cmd/digraph), 'dot' (Graphviz), or Go template")
	listCmd.Flags().Bool("all", false, "Show all issues (default behavior; flag provided for CLI familiarity)")
	listCmd.Flags().Bool("long", false, "Show detailed multi-line output for each issue")
	listCmd.Flags().String("sort", "", "Sort by field: priority, created, updated, closed, status, id, title, type, assignee")
	listCmd.Flags().BoolP("reverse", "r", false, "Reverse sort order")
	
	// Pattern matching
	listCmd.Flags().String("title-contains", "", "Filter by title substring (case-insensitive)")
	listCmd.Flags().String("desc-contains", "", "Filter by description substring (case-insensitive)")
	listCmd.Flags().String("notes-contains", "", "Filter by notes substring (case-insensitive)")
	
	// Date ranges
	listCmd.Flags().String("created-after", "", "Filter issues created after date (YYYY-MM-DD or RFC3339)")
	listCmd.Flags().String("created-before", "", "Filter issues created before date (YYYY-MM-DD or RFC3339)")
	listCmd.Flags().String("updated-after", "", "Filter issues updated after date (YYYY-MM-DD or RFC3339)")
	listCmd.Flags().String("updated-before", "", "Filter issues updated before date (YYYY-MM-DD or RFC3339)")
	listCmd.Flags().String("closed-after", "", "Filter issues closed after date (YYYY-MM-DD or RFC3339)")
	listCmd.Flags().String("closed-before", "", "Filter issues closed before date (YYYY-MM-DD or RFC3339)")
	
	// Empty/null checks
	listCmd.Flags().Bool("empty-description", false, "Filter issues with empty or missing description")
	listCmd.Flags().Bool("no-assignee", false, "Filter issues with no assignee")
	listCmd.Flags().Bool("no-labels", false, "Filter issues with no labels")
	
	// Priority ranges
	listCmd.Flags().String("priority-min", "", "Filter by minimum priority (inclusive, 0-4 or P0-P4)")
	listCmd.Flags().String("priority-max", "", "Filter by maximum priority (inclusive, 0-4 or P0-P4)")
	
	// Note: --json flag is defined as a persistent flag in main.go, not here
	rootCmd.AddCommand(listCmd)
}

// outputDotFormat outputs issues in Graphviz DOT format
func outputDotFormat(ctx context.Context, store storage.Storage, issues []*types.Issue) error {
	fmt.Println("digraph dependencies {")
	fmt.Println("  rankdir=TB;")
	fmt.Println("  node [shape=box, style=rounded];")
	fmt.Println()

	// Build map of all issues for quick lookup
	issueMap := make(map[string]*types.Issue)
	for _, issue := range issues {
		issueMap[issue.ID] = issue
	}

	// Output nodes with labels including ID, type, priority, and status
	for _, issue := range issues {
		// Build label with ID, type, priority, and title (using actual newlines)
		label := fmt.Sprintf("%s\n[%s P%d]\n%s\n(%s)",
			issue.ID,
			issue.IssueType,
			issue.Priority,
			issue.Title,
			issue.Status)

		// Color by status only - keep it simple
		fillColor := "white"
		fontColor := "black"

		switch issue.Status {
		case "closed":
			fillColor = "lightgray"
			fontColor = "dimgray"
		case "in_progress":
			fillColor = "lightyellow"
		case "blocked":
			fillColor = "lightcoral"
		}

		fmt.Printf("  %q [label=%q, style=\"rounded,filled\", fillcolor=%q, fontcolor=%q];\n",
			issue.ID, label, fillColor, fontColor)
	}
	fmt.Println()

	// Output edges with labels for dependency type
	for _, issue := range issues {
		deps, err := store.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			continue
		}
		for _, dep := range deps {
			// Only output edges where both nodes are in the filtered list
			if issueMap[dep.DependsOnID] != nil {
				// Color code by dependency type
				color := "black"
				style := "solid"
				switch dep.Type {
				case "blocks":
					color = "red"
					style = "bold"
				case "parent-child":
					color = "blue"
				case "discovered-from":
					color = "green"
					style = "dashed"
				case "related":
					color = "gray"
					style = "dashed"
				}
				fmt.Printf("  %q -> %q [label=%q, color=%s, style=%s];\n",
					issue.ID, dep.DependsOnID, dep.Type, color, style)
			}
		}
	}

	fmt.Println("}")
	return nil
}

// outputFormattedList outputs issues in a custom format (preset or Go template)
func outputFormattedList(ctx context.Context, store storage.Storage, issues []*types.Issue, formatStr string) error {
	// Handle special 'dot' format (Graphviz output)
	if formatStr == "dot" {
		return outputDotFormat(ctx, store, issues)
	}

	// Built-in format presets
	presets := map[string]string{
		"digraph": "{{.IssueID}} {{.DependsOnID}}",
	}

	// Check if it's a preset
	templateStr, isPreset := presets[formatStr]
	if !isPreset {
		templateStr = formatStr
	}

	// Parse template
	tmpl, err := template.New("format").Parse(templateStr)
	if err != nil {
		return fmt.Errorf("invalid format template: %w", err)
	}

	// Build map of all issues for quick lookup
	issueMap := make(map[string]bool)
	for _, issue := range issues {
		issueMap[issue.ID] = true
	}

	// For each issue, output its dependencies using the template
	for _, issue := range issues {
		deps, err := store.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			continue
		}
		for _, dep := range deps {
			// Only output edges where both nodes are in the filtered list
			if issueMap[dep.DependsOnID] {
				// Template data includes both issue and dependency info
				data := map[string]interface{}{
					"IssueID":     issue.ID,
					"DependsOnID": dep.DependsOnID,
					"Type":        dep.Type,
					"Issue":       issue,
					"Dependency":  dep,
				}

				var buf bytes.Buffer
				if err := tmpl.Execute(&buf, data); err != nil {
					return fmt.Errorf("template execution error: %w", err)
				}
				fmt.Println(buf.String())
			}
		}
	}

	return nil
}
