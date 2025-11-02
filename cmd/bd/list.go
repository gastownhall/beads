package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// normalizeLabels trims whitespace, removes empty strings, and deduplicates labels
func normalizeLabels(ss []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
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
		jsonOutput, _ := cmd.Flags().GetBool("json")

		// Normalize labels: trim, dedupe, remove empty
		labels = normalizeLabels(labels)
	labelsAny = normalizeLabels(labelsAny)

		filter := types.IssueFilter{
		Limit: limit,
		}
		if status != "" && status != "all" {
		s := types.Status(status)
		filter.Status = &s
		}
		// Use Changed() to properly handle P0 (priority=0)
		if cmd.Flags().Changed("priority") {
		priority, _ := cmd.Flags().GetInt("priority")
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
	ids := normalizeLabels(strings.Split(idFilter, ","))
	if len(ids) > 0 {
	filter.IDs = ids
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
				priority, _ := cmd.Flags().GetInt("priority")
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

			var issues []*types.Issue
			if err := json.Unmarshal(resp.Data, &issues); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
				os.Exit(1)
			}

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
			return
		}

		// Direct mode
		ctx := context.Background()
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

		// Handle format flag
		if formatStr != "" {
			if err := outputFormattedList(ctx, store, issues, formatStr); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		if jsonOutput {
			// Populate labels for JSON output
			for _, issue := range issues {
				issue.Labels, _ = store.GetLabels(ctx, issue.ID)
			}

			// Get dependency counts in bulk (single query instead of N queries)
			issueIDs := make([]string, len(issues))
			for i, issue := range issues {
				issueIDs[i] = issue.ID
			}
			depCounts, _ := store.GetDependencyCounts(ctx, issueIDs)

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

		fmt.Printf("\nFound %d issues:\n\n", len(issues))
		for _, issue := range issues {
			// Load labels for display
			labels, _ := store.GetLabels(ctx, issue.ID)

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
	},
}

func init() {
	listCmd.Flags().StringP("status", "s", "", "Filter by status (open, in_progress, blocked, closed)")
	listCmd.Flags().IntP("priority", "p", 0, "Filter by priority (0-4: 0=critical, 1=high, 2=medium, 3=low, 4=backlog)")
	listCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	listCmd.Flags().StringP("type", "t", "", "Filter by type (bug, feature, task, epic, chore)")
	listCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL). Can combine with --label-any")
	listCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE). Can combine with --label")
	listCmd.Flags().String("title", "", "Filter by title text (case-insensitive substring match)")
	listCmd.Flags().String("id", "", "Filter by specific issue IDs (comma-separated, e.g., bd-1,bd-5,bd-10)")
	listCmd.Flags().IntP("limit", "n", 0, "Limit results")
	listCmd.Flags().String("format", "", "Output format: 'digraph' (for golang.org/x/tools/cmd/digraph), 'dot' (Graphviz), or Go template")
	listCmd.Flags().Bool("all", false, "Show all issues (default behavior; flag provided for CLI familiarity)")
	listCmd.Flags().Bool("json", false, "Output JSON format")
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
