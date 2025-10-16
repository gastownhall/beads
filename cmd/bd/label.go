// Package main implements the bd CLI label management commands.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

var labelCmd = &cobra.Command{
	Use:   "label",
	Short: "Manage issue labels",
}

// executeLabelCommand executes a label operation and handles output
func executeLabelCommand(issueID, label, operation string, operationFunc func(context.Context, string, string, string) error) {
	ctx := context.Background()
	if err := operationFunc(ctx, issueID, label, actor); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Schedule auto-flush
	markDirtyAndScheduleFlush()

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status":   operation,
			"issue_id": issueID,
			"label":    label,
		})
		return
	}

	green := color.New(color.FgGreen).SprintFunc()
	// Capitalize first letter manually (strings.Title is deprecated)
	capitalizedOp := strings.ToUpper(operation[:1]) + operation[1:]
	fmt.Printf("%s %s label '%s' to %s\n", green("✓"), capitalizedOp, label, issueID)
}

var labelAddCmd = &cobra.Command{
	Use:   "add [issue-id] [label]",
	Short: "Add a label to an issue",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		executeLabelCommand(args[0], args[1], "added", store.AddLabel)
	},
}

var labelRemoveCmd = &cobra.Command{
	Use:   "remove [issue-id] [label]",
	Short: "Remove a label from an issue",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		executeLabelCommand(args[0], args[1], "removed", store.RemoveLabel)
	},
}

var labelListCmd = &cobra.Command{
	Use:   "list [issue-id]",
	Short: "List labels for an issue",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		issueID := args[0]

		ctx := context.Background()
		labels, err := store.GetLabels(ctx, issueID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			// Always output array, even if empty
			if labels == nil {
				labels = []string{}
			}
			outputJSON(labels)
			return
		}

		if len(labels) == 0 {
			fmt.Printf("\n%s has no labels\n", issueID)
			return
		}

		cyan := color.New(color.FgCyan).SprintFunc()
		fmt.Printf("\n%s Labels for %s:\n", cyan("🏷"), issueID)
		for _, label := range labels {
			fmt.Printf("  - %s\n", label)
		}
		fmt.Println()
	},
}

var labelListAllCmd = &cobra.Command{
	Use:   "list-all",
	Short: "List all unique labels in the database",
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		// Get all issues to collect labels
		issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Collect unique labels with counts
		labelCounts := make(map[string]int)
		for _, issue := range issues {
			labels, err := store.GetLabels(ctx, issue.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting labels for %s: %v\n", issue.ID, err)
				os.Exit(1)
			}
			for _, label := range labels {
				labelCounts[label]++
			}
		}

		if len(labelCounts) == 0 {
			if jsonOutput {
				outputJSON([]string{})
			} else {
				fmt.Println("\nNo labels found in database")
			}
			return
		}

		// Sort labels alphabetically
		labels := make([]string, 0, len(labelCounts))
		for label := range labelCounts {
			labels = append(labels, label)
		}
		sort.Strings(labels)

		if jsonOutput {
			// Output as array of {label, count} objects
			type labelInfo struct {
				Label string `json:"label"`
				Count int    `json:"count"`
			}
			result := make([]labelInfo, 0, len(labels))
			for _, label := range labels {
				result = append(result, labelInfo{
					Label: label,
					Count: labelCounts[label],
				})
			}
			outputJSON(result)
			return
		}

		cyan := color.New(color.FgCyan).SprintFunc()
		fmt.Printf("\n%s All labels (%d unique):\n", cyan("🏷"), len(labels))

		// Find longest label for alignment
		maxLen := 0
		for _, label := range labels {
			if len(label) > maxLen {
				maxLen = len(label)
			}
		}

		for _, label := range labels {
			padding := strings.Repeat(" ", maxLen-len(label))
			fmt.Printf("  %s%s  (%d issues)\n", label, padding, labelCounts[label])
		}
		fmt.Println()
	},
}

func init() {
	labelCmd.AddCommand(labelAddCmd)
	labelCmd.AddCommand(labelRemoveCmd)
	labelCmd.AddCommand(labelListCmd)
	labelCmd.AddCommand(labelListAllCmd)
	rootCmd.AddCommand(labelCmd)
}
