// Package main implements the bd CLI label management commands.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var labelCmd = &cobra.Command{
	Use:     "label",
	GroupID: "issues",
	Short:   "Manage issue labels",
}

// processBatchLabelOperation wraps label add/remove for multiple issues in a
// single transaction for atomicity.
func processBatchLabelOperation(issueIDs []string, label string, operation string, jsonOut bool,
	txFunc func(context.Context, storage.Transaction, string, string, string) error) {
	ctx := rootCtx
	commitMsg := fmt.Sprintf("bd: label %s '%s' on %d issue(s)", operation, label, len(issueIDs))
	err := transact(ctx, store, commitMsg, func(tx storage.Transaction) error {
		for _, issueID := range issueIDs {
			if err := txFunc(ctx, tx, issueID, label, actor); err != nil {
				return fmt.Errorf("%s label '%s' on %s: %w", operation, label, issueID, err)
			}
		}
		return nil
	})
	if err != nil {
		FatalErrorRespectJSON("label %s: %v", operation, err)
	}
	if jsonOut {
		results := make([]map[string]interface{}, 0, len(issueIDs))
		for _, issueID := range issueIDs {
			results = append(results, map[string]interface{}{
				"status":   operation,
				"issue_id": issueID,
				"label":    label,
			})
		}
		outputJSON(results)
	} else {
		verb := "Added"
		prep := "to"
		if operation == "removed" {
			verb = "Removed"
			prep = "from"
		}
		for _, issueID := range issueIDs {
			fmt.Printf("%s %s label '%s' %s %s\n", ui.RenderPass("✓"), verb, label, prep, issueID)
		}
	}
}
func parseLabelArgs(args []string) (issueIDs []string, label string) {
	label = args[len(args)-1]
	issueIDs = args[:len(args)-1]
	return
}

//nolint:dupl // labelAddCmd and labelRemoveCmd are similar but serve different operations
var labelAddCmd = &cobra.Command{
	Use:   "add [issue-id...] [label]",
	Short: "Add a label to one or more issues",
	Args:  cobra.MinimumNArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("label add")
		// Use global jsonOutput set by PersistentPreRun
		issueIDs, label := parseLabelArgs(args)
		// Resolve partial IDs
		ctx := rootCtx
		resolvedIDs := make([]string, 0, len(issueIDs))
		for _, id := range issueIDs {
			var fullID string
			var err error
			fullID, err = utils.ResolvePartialID(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				continue
			}
			resolvedIDs = append(resolvedIDs, fullID)
		}
		issueIDs = resolvedIDs

		// Protect reserved label namespaces
		// provides:* labels can only be added via 'bd ship' command
		if strings.HasPrefix(label, "provides:") {
			FatalErrorRespectJSON("'provides:' labels are reserved for cross-project capabilities. Hint: use 'bd ship %s' instead", strings.TrimPrefix(label, "provides:"))
		}

		processBatchLabelOperation(issueIDs, label, "added", jsonOutput,
			func(ctx context.Context, tx storage.Transaction, issueID, lbl, act string) error {
				return tx.AddLabel(ctx, issueID, lbl, act)
			})
	},
}

//nolint:dupl // labelRemoveCmd and labelAddCmd are similar but serve different operations
var labelRemoveCmd = &cobra.Command{
	Use:   "remove [issue-id...] [label]",
	Short: "Remove a label from one or more issues",
	Args:  cobra.MinimumNArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("label remove")
		// Use global jsonOutput set by PersistentPreRun
		issueIDs, label := parseLabelArgs(args)
		// Resolve partial IDs
		ctx := rootCtx
		resolvedIDs := make([]string, 0, len(issueIDs))
		for _, id := range issueIDs {
			var fullID string
			var err error
			fullID, err = utils.ResolvePartialID(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				continue
			}
			resolvedIDs = append(resolvedIDs, fullID)
		}
		issueIDs = resolvedIDs
		processBatchLabelOperation(issueIDs, label, "removed", jsonOutput,
			func(ctx context.Context, tx storage.Transaction, issueID, lbl, act string) error {
				return tx.RemoveLabel(ctx, issueID, lbl, act)
			})
	},
}
var labelListCmd = &cobra.Command{
	Use:   "list [issue-id]",
	Short: "List labels for an issue",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		// Use global jsonOutput set by PersistentPreRun
		ctx := rootCtx
		// Resolve partial ID first
		var issueID string
		var err error
		issueID, err = utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			FatalErrorRespectJSON("resolving %s: %v", args[0], err)
		}
		var labels []string
		// Direct mode
		labels, err = store.GetLabels(ctx, issueID)
		if err != nil {
			FatalErrorRespectJSON("%v", err)
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
		fmt.Printf("\n%s Labels for %s:\n", ui.RenderAccent("🏷"), issueID)
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
		// Use global jsonOutput set by PersistentPreRun
		ctx := rootCtx
		var issues []*types.Issue
		var err error
		// Direct mode
		issues, err = store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			FatalErrorRespectJSON("%v", err)
		}
		// Collect unique labels with counts
		labelCounts := make(map[string]int)
		for _, issue := range issues {
			// Direct mode - need to fetch labels
			labels, err := store.GetLabels(ctx, issue.ID)
			if err != nil {
				FatalErrorRespectJSON("getting labels for %s: %v", issue.ID, err)
			}
			for _, label := range labels {
				labelCounts[label]++
			}
		}
		type labelInfo struct {
			Label string `json:"label"`
			Count int    `json:"count"`
		}
		if len(labelCounts) == 0 {
			if jsonOutput {
				outputJSON([]labelInfo{})
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
		fmt.Printf("\n%s All labels (%d unique):\n", ui.RenderAccent("🏷"), len(labels))
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
	// Issue ID completions
	labelAddCmd.ValidArgsFunction = issueIDCompletion
	labelRemoveCmd.ValidArgsFunction = issueIDCompletion
	labelListCmd.ValidArgsFunction = issueIDCompletion

	labelCmd.AddCommand(labelAddCmd)
	labelCmd.AddCommand(labelRemoveCmd)
	labelCmd.AddCommand(labelListCmd)
	labelCmd.AddCommand(labelListAllCmd)
	rootCmd.AddCommand(labelCmd)
}
