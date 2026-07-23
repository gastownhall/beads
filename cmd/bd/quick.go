package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/validation"
)

var quickCmd = &cobra.Command{
	Use:     "q [title]",
	GroupID: "issues",
	Short:   "Quick capture: create issue and output only ID",
	Long: `Quick capture creates an issue and outputs only the issue ID.
Designed for scripting and AI agent integration.

Example:
  bd q "Fix login bug"           # Outputs: bd-a1b2
  ISSUE=$(bd q "New feature")    # Capture ID in variable
  bd q "Task" | xargs bd show    # Pipe to other commands
  bd q "Subtask" --parent=bd-a1b2  # Hierarchical child (outputs: bd-a1b2.1)`,
	Args:          cobra.MinimumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Create the event before the readonly guard so the operation label
		// matches this command ("q", not "create") and the readonly exit path
		// still flushes queued metrics via CheckReadonly's CloseAndFlush.
		evt := metrics.NewCommandEvent("q")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		CheckReadonly("q")

		if usesProxiedServer() {
			return runQuickProxiedServer(cmd, rootCtx, args)
		}

		title := strings.Join(args, " ")

		priorityStr, _ := cmd.Flags().GetString("priority")
		issueType, _ := cmd.Flags().GetString("type")
		labels, _ := cmd.Flags().GetStringSlice("labels")
		parentID, _ := cmd.Flags().GetString("parent")

		priority, err := validation.ValidatePriority(priorityStr)
		if err != nil {
			return HandleError("%v", err)
		}

		ctx := rootCtx

		// Mirrors bd create's parent handling: validate the parent exists,
		// inherit its labels, and reserve a hierarchical child ID.
		var inheritedLabels []string
		if parentID != "" {
			if _, err := store.GetIssue(ctx, parentID); err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					return HandleError("parent issue %s not found", parentID)
				}
				return HandleError("failed to check parent issue: %v", err)
			}
			inheritedLabels, _ = store.GetLabels(ctx, parentID)
		}

		issue := &types.Issue{
			Title:     title,
			Status:    types.StatusOpen,
			Priority:  priority,
			IssueType: types.IssueType(issueType).Normalize(),
			Labels:    mergeCreateLabels(labels, inheritedLabels),
		}

		if parentID != "" {
			childID, err := store.GetNextChildID(ctx, parentID)
			if err != nil {
				return HandleError("%v", err)
			}
			issue.ID = childID
			ctx = storage.WithReservedChildCounter(ctx, parentID, childID)
		}

		// The issue and its parent-child edge commit in one transaction; a
		// failed edge rolls back the create instead of leaving a dep-less
		// child behind (same contract as bd create).
		if err := createIssueWithDeps(ctx, store, issue, actor, createDepEdges{parentID: parentID}); err != nil {
			return HandleError("%v", err)
		}

		commandDidWrite.Store(true)

		fmt.Println(issue.ID)
		return nil
	},
}

func init() {
	quickCmd.Flags().StringP("priority", "p", "2", "Priority (0-4 or P0-P4)")
	quickCmd.Flags().StringP("type", "t", "task", "Issue type")
	quickCmd.Flags().StringSliceP("labels", "l", []string{}, "Labels")
	quickCmd.Flags().String("parent", "", "Parent issue ID for hierarchical child (e.g., 'bd-a3f8e9')")
	rootCmd.AddCommand(quickCmd)
}
