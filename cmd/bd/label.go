// Package main implements the bd CLI label management commands.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

var labelCmd = &cobra.Command{
	Use:   "label",
	Short: "Manage issue labels",
}

// executeLabelCommand executes a label operation and handles output
func executeLabelCommand(issueID, label, operation string, operationFunc func(context.Context, string, string, string) error) {
	ctx := context.Background()

	// Use daemon if available
	if daemonClient != nil {
		var err error
		if operation == "added" {
			_, err = daemonClient.AddLabel(&rpc.LabelAddArgs{
				ID:    issueID,
				Label: label,
			})
		} else {
			_, err = daemonClient.RemoveLabel(&rpc.LabelRemoveArgs{
				ID:    issueID,
				Label: label,
			})
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Direct mode
		if err := operationFunc(ctx, issueID, label, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()
	}

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
	Use:   "add [issue-id...] [label]",
	Short: "Add a label to one or more issues",
	Args:  cobra.MinimumNArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		// Last arg is the label, everything before is issue IDs
		label := args[len(args)-1]
		issueIDs := args[:len(args)-1]

		ctx := context.Background()
		results := []map[string]interface{}{}

		for _, issueID := range issueIDs {
			var err error
			if daemonClient != nil {
				_, err = daemonClient.AddLabel(&rpc.LabelAddArgs{
					ID:    issueID,
					Label: label,
				})
			} else {
				err = store.AddLabel(ctx, issueID, label, actor)
			}

			if err != nil {
				fmt.Fprintf(os.Stderr, "Error adding label to %s: %v\n", issueID, err)
				continue
			}

			if jsonOutput {
				results = append(results, map[string]interface{}{
					"status":   "added",
					"issue_id": issueID,
					"label":    label,
				})
			} else {
				green := color.New(color.FgGreen).SprintFunc()
				fmt.Printf("%s Added label '%s' to %s\n", green("✓"), label, issueID)
			}
		}

		if len(issueIDs) > 0 && daemonClient == nil {
			markDirtyAndScheduleFlush()
		}

		if jsonOutput && len(results) > 0 {
			outputJSON(results)
		}
	},
}

var labelRemoveCmd = &cobra.Command{
	Use:   "remove [issue-id...] [label]",
	Short: "Remove a label from one or more issues",
	Args:  cobra.MinimumNArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		// Last arg is the label, everything before is issue IDs
		label := args[len(args)-1]
		issueIDs := args[:len(args)-1]

		ctx := context.Background()
		results := []map[string]interface{}{}

		for _, issueID := range issueIDs {
			var err error
			if daemonClient != nil {
				_, err = daemonClient.RemoveLabel(&rpc.LabelRemoveArgs{
					ID:    issueID,
					Label: label,
				})
			} else {
				err = store.RemoveLabel(ctx, issueID, label, actor)
			}

			if err != nil {
				fmt.Fprintf(os.Stderr, "Error removing label from %s: %v\n", issueID, err)
				continue
			}

			if jsonOutput {
				results = append(results, map[string]interface{}{
					"status":   "removed",
					"issue_id": issueID,
					"label":    label,
				})
			} else {
				green := color.New(color.FgGreen).SprintFunc()
				fmt.Printf("%s Removed label '%s' from %s\n", green("✓"), label, issueID)
			}
		}

		if len(issueIDs) > 0 && daemonClient == nil {
			markDirtyAndScheduleFlush()
		}

		if jsonOutput && len(results) > 0 {
			outputJSON(results)
		}
	},
}

var labelListCmd = &cobra.Command{
	Use:   "list [issue-id]",
	Short: "List labels for an issue",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		issueID := args[0]

		ctx := context.Background()
		var labels []string

		// Use daemon if available
		if daemonClient != nil {
			resp, err := daemonClient.Show(&rpc.ShowArgs{ID: issueID})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			var issue types.Issue
			if err := json.Unmarshal(resp.Data, &issue); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
				os.Exit(1)
			}
			labels = issue.Labels
		} else {
			// Direct mode
			var err error
			labels, err = store.GetLabels(ctx, issueID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
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

		var issues []*types.Issue
		var err error

		// Use daemon if available
		if daemonClient != nil {
			resp, err := daemonClient.List(&rpc.ListArgs{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			if err := json.Unmarshal(resp.Data, &issues); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
				os.Exit(1)
			}
		} else {
			// Direct mode
			issues, err = store.SearchIssues(ctx, "", types.IssueFilter{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		// Collect unique labels with counts
		labelCounts := make(map[string]int)
		for _, issue := range issues {
			if daemonClient != nil {
				// Labels are already in the issue from daemon
				for _, label := range issue.Labels {
					labelCounts[label]++
				}
			} else {
				// Direct mode - need to fetch labels
				labels, err := store.GetLabels(ctx, issue.ID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error getting labels for %s: %v\n", issue.ID, err)
					os.Exit(1)
				}
				for _, label := range labels {
					labelCounts[label]++
				}
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
