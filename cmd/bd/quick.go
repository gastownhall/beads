package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
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
  bd q "Task" | xargs bd show    # Pipe to other commands`,
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

		title := strings.Join(args, " ")

		priorityStr, _ := cmd.Flags().GetString("priority")
		issueType, _ := cmd.Flags().GetString("type")
		labels, _ := cmd.Flags().GetStringSlice("labels")

		priority, err := validation.ValidatePriority(priorityStr)
		if err != nil {
			return HandleError("%v", err)
		}

		issue := &types.Issue{
			Title:     title,
			Status:    types.StatusOpen,
			Priority:  priority,
			IssueType: types.IssueType(issueType).Normalize(),
			Labels:    mergeCreateLabels(labels, nil),
		}

		ctx := rootCtx
		if err := store.CreateIssue(ctx, issue, actor); err != nil {
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
	rootCmd.AddCommand(quickCmd)
}
