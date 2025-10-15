package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"text/template"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List issues",
	Run: func(cmd *cobra.Command, args []string) {
		status, _ := cmd.Flags().GetString("status")
		assignee, _ := cmd.Flags().GetString("assignee")
		issueType, _ := cmd.Flags().GetString("type")
		limit, _ := cmd.Flags().GetInt("limit")
		formatStr, _ := cmd.Flags().GetString("format")

		filter := types.IssueFilter{
			Limit: limit,
		}
		if status != "" {
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

		ctx := context.Background()
		issues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
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
			outputJSON(issues)
			return
		}

		fmt.Printf("\nFound %d issues:\n\n", len(issues))
		for _, issue := range issues {
			fmt.Printf("%s [P%d] [%s] %s\n", issue.ID, issue.Priority, issue.IssueType, issue.Status)
			fmt.Printf("  %s\n", issue.Title)
			if issue.Assignee != "" {
				fmt.Printf("  Assignee: %s\n", issue.Assignee)
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
	listCmd.Flags().IntP("limit", "n", 0, "Limit results")
	listCmd.Flags().String("format", "", "Output format: 'digraph' (for golang.org/x/tools/cmd/digraph), 'dot' (Graphviz), or Go template")
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

	// Output nodes with labels
	for _, issue := range issues {
		// Escape quotes in title
		title := issue.Title
		title = fmt.Sprintf("%q", title) // Go's %q handles escaping
		fmt.Printf("  %q [label=%s];\n", issue.ID, title)
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
