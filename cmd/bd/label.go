// Package main implements the bd CLI label management commands.
package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
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

func processBatchLabelOperation(issueIDs []string, labels []string, operation string, jsonOut bool,
	txFunc func(context.Context, storage.Transaction, string, string, string) error) error {
	ctx := rootCtx
	labelDesc := strings.Join(labels, "', '")
	commitMsg := fmt.Sprintf("bd: label %s '%s' on %d issue(s)", operation, labelDesc, len(issueIDs))
	err := transactHonoringAutoCommit(ctx, store, commitMsg, func(tx storage.Transaction) error {
		for _, issueID := range issueIDs {
			for _, label := range labels {
				if err := txFunc(ctx, tx, issueID, label, actor); err != nil {
					return fmt.Errorf("%s label '%s' on %s: %w", operation, label, issueID, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return HandleErrorRespectJSON("label %s: %v", operation, err)
	}
	commandDidWrite.Store(true)
	if jsonOut {
		results := make([]map[string]interface{}, 0, len(issueIDs)*len(labels))
		for _, issueID := range issueIDs {
			for _, label := range labels {
				results = append(results, map[string]interface{}{
					"status":   operation,
					"issue_id": issueID,
					"label":    label,
				})
			}
		}
		return outputJSON(results)
	}
	verb := "Added"
	prep := "to"
	if operation == "removed" {
		verb = "Removed"
		prep = "from"
	}
	noun := "label"
	if len(labels) > 1 {
		noun = "labels"
	}
	for _, issueID := range issueIDs {
		fmt.Printf("%s %s %s '%s' %s %s\n", ui.RenderPass("✓"), verb, noun, labelDesc, prep, issueID)
	}
	return nil
}

// parseLabelArgs splits positional args into issue IDs and labels. The final
// arg is the label spec; commas separate multiple labels ("label1,label2").
func parseLabelArgs(args []string) (issueIDs []string, labels []string) {
	labels = splitLabelArg(args[len(args)-1])
	issueIDs = args[:len(args)-1]
	return
}

// splitLabelArg splits a comma-separated label argument into individual
// labels, trimming whitespace and dropping empty entries. Matches the
// comma-separated convention of bd create --labels.
func splitLabelArg(arg string) []string {
	parts := strings.Split(arg, ",")
	labels := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			labels = append(labels, part)
		}
	}
	return labels
}

// resolveLabelIssueIDs resolves every issue-ID positional arg, failing hard on
// the first arg that doesn't resolve. Labels come last on the command line, so
// when several ID-position args fail the caller almost certainly passed labels
// space-separated ("bd label add bd-123 a b c"); the error hints at the
// comma-separated form instead of silently skipping the bad args (bd-vu5kv).
func resolveLabelIssueIDs(ctx context.Context, subcommand string, issueIDs []string) ([]string, error) {
	resolved := make([]string, 0, len(issueIDs))
	for _, id := range issueIDs {
		fullID, err := utils.ResolvePartialID(ctx, store, id)
		if err != nil {
			if len(issueIDs) > 1 {
				return nil, fmt.Errorf("resolving issue ID %q: %w (to %s multiple labels, pass one comma-separated argument: bd label %s <issue-id> label1,label2)",
					id, err, subcommand, subcommand)
			}
			return nil, fmt.Errorf("resolving issue ID %q: %w", id, err)
		}
		resolved = append(resolved, fullID)
	}
	return resolved, nil
}

//nolint:dupl // labelAddCmd and labelRemoveCmd are similar but serve different operations
var labelAddCmd = &cobra.Command{
	Use:           "add [issue-id...] [label[,label...]]",
	Short:         "Add one or more labels to one or more issues",
	Long:          "Add labels to issues. Issue IDs come first; the final argument is the label. Pass multiple labels comma-separated: bd label add bd-123 label1,label2",
	Args:          cobra.MinimumNArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("label add")

		evt := metrics.NewCommandEvent("label-add")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runLabelAddProxiedServer(rootCtx, args)
		}

		issueIDs, labels := parseLabelArgs(args)
		if len(labels) == 0 {
			return HandleErrorRespectJSON("label cannot be empty")
		}
		ctx := rootCtx
		issueIDs, err := resolveLabelIssueIDs(ctx, "add", issueIDs)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		for _, label := range labels {
			if strings.HasPrefix(label, "provides:") {
				return HandleErrorRespectJSON("'provides:' labels are reserved for cross-project capabilities. Hint: use 'bd ship %s' instead", strings.TrimPrefix(label, "provides:"))
			}
		}

		return processBatchLabelOperation(issueIDs, labels, "added", jsonOutput,
			func(ctx context.Context, tx storage.Transaction, issueID, lbl, act string) error {
				return tx.AddLabel(ctx, issueID, lbl, act)
			})
	},
}

//nolint:dupl // labelRemoveCmd and labelAddCmd are similar but serve different operations
var labelRemoveCmd = &cobra.Command{
	Use:           "remove [issue-id...] [label[,label...]]",
	Short:         "Remove one or more labels from one or more issues",
	Long:          "Remove labels from issues. Issue IDs come first; the final argument is the label. Pass multiple labels comma-separated: bd label remove bd-123 label1,label2",
	Args:          cobra.MinimumNArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("label remove")

		evt := metrics.NewCommandEvent("label-remove")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runLabelRemoveProxiedServer(rootCtx, args)
		}

		issueIDs, labels := parseLabelArgs(args)
		if len(labels) == 0 {
			return HandleErrorRespectJSON("label cannot be empty")
		}
		ctx := rootCtx
		issueIDs, err := resolveLabelIssueIDs(ctx, "remove", issueIDs)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		return processBatchLabelOperation(issueIDs, labels, "removed", jsonOutput,
			func(ctx context.Context, tx storage.Transaction, issueID, lbl, act string) error {
				return tx.RemoveLabel(ctx, issueID, lbl, act)
			})
	},
}
var labelListCmd = &cobra.Command{
	Use:           "list [issue-id]",
	Short:         "List labels for an issue",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("label-list")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runLabelListProxiedServer(rootCtx, args)
		}

		ctx := rootCtx
		var issueID string
		var err error
		issueID, err = utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			return HandleErrorRespectJSON("resolving %s: %v", args[0], err)
		}
		var labels []string
		labels, err = store.GetLabels(ctx, issueID)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		if jsonOutput {
			if labels == nil {
				labels = []string{}
			}
			return outputJSON(labels)
		}
		if len(labels) == 0 {
			fmt.Printf("\n%s has no labels\n", issueID)
			return nil
		}
		fmt.Printf("\n%s Labels for %s:\n", ui.RenderAccent("🏷"), issueID)
		for _, label := range labels {
			fmt.Printf("  - %s\n", label)
		}
		fmt.Println()
		return nil
	},
}
var labelListAllCmd = &cobra.Command{
	Use:           "list-all",
	Short:         "List all unique labels in the database",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("label-list-all")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runLabelListAllProxiedServer(rootCtx)
		}

		ctx := rootCtx
		var issues []*types.Issue
		var err error
		issues, err = store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		labelCounts := make(map[string]int)
		for _, issue := range issues {
			labels, err := store.GetLabels(ctx, issue.ID)
			if err != nil {
				return HandleErrorRespectJSON("getting labels for %s: %v", issue.ID, err)
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
				return outputJSON([]labelInfo{})
			}
			fmt.Println("\nNo labels found in database")
			return nil
		}
		labels := make([]string, 0, len(labelCounts))
		for label := range labelCounts {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		if jsonOutput {
			result := make([]labelInfo, 0, len(labels))
			for _, label := range labels {
				result = append(result, labelInfo{
					Label: label,
					Count: labelCounts[label],
				})
			}
			return outputJSON(result)
		}
		fmt.Printf("\n%s All labels (%d unique):\n", ui.RenderAccent("🏷"), len(labels))
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
		return nil
	},
}

var labelPropagateCmd = &cobra.Command{
	Use:           "propagate [parent-id] [label]",
	Short:         "Propagate a label from a parent issue to all its children",
	Long:          "Push a label from a parent down to all direct children that don't already have it. Useful for applying branch: labels across an epic's subtasks.",
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("label propagate")

		evt := metrics.NewCommandEvent("label-propagate")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runLabelPropagateProxiedServer(rootCtx, args)
		}

		ctx := rootCtx

		parentID, err := utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			return HandleErrorRespectJSON("resolving parent %s: %v", args[0], err)
		}
		label := strings.TrimSpace(args[1])
		if label == "" {
			return HandleErrorRespectJSON("label cannot be empty")
		}

		if strings.HasPrefix(label, "provides:") {
			return HandleErrorRespectJSON("'provides:' labels are reserved for cross-project capabilities. Hint: use 'bd ship %s' instead", strings.TrimPrefix(label, "provides:"))
		}

		children, err := store.SearchIssues(ctx, "", types.IssueFilter{ParentID: &parentID})
		if err != nil {
			return HandleErrorRespectJSON("searching children of %s: %v", parentID, err)
		}

		if len(children) == 0 {
			if jsonOutput {
				return outputJSON([]map[string]interface{}{})
			}
			fmt.Printf("No children found for %s\n", parentID)
			return nil
		}

		commitMsg := fmt.Sprintf("bd: propagate label '%s' from %s to %d children", label, parentID, len(children))
		err = transactHonoringAutoCommit(ctx, store, commitMsg, func(tx storage.Transaction) error {
			for _, child := range children {
				if err := tx.AddLabel(ctx, child.ID, label, actor); err != nil {
					return fmt.Errorf("add label '%s' on %s: %w", label, child.ID, err)
				}
			}
			return nil
		})
		if err != nil {
			return HandleErrorRespectJSON("label propagate: %v", err)
		}

		if jsonOutput {
			results := make([]map[string]interface{}, 0, len(children))
			for _, child := range children {
				results = append(results, map[string]interface{}{
					"status":   "propagated",
					"issue_id": child.ID,
					"label":    label,
				})
			}
			return outputJSON(results)
		}
		for _, child := range children {
			fmt.Printf("%s Propagated label '%s' to %s\n", ui.RenderPass("✓"), label, child.ID)
		}
		return nil
	},
}

func init() {
	// Issue ID completions
	labelAddCmd.ValidArgsFunction = issueIDCompletion
	labelRemoveCmd.ValidArgsFunction = issueIDCompletion
	labelListCmd.ValidArgsFunction = issueIDCompletion
	labelPropagateCmd.ValidArgsFunction = issueIDCompletion

	labelCmd.AddCommand(labelAddCmd)
	labelCmd.AddCommand(labelRemoveCmd)
	labelCmd.AddCommand(labelListCmd)
	labelCmd.AddCommand(labelListAllCmd)
	labelCmd.AddCommand(labelPropagateCmd)
	rootCmd.AddCommand(labelCmd)
}
