package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
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
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("create")

		title := strings.Join(args, " ")

		// Get optional flags
		priorityStr, _ := cmd.Flags().GetString("priority")
		issueType, _ := cmd.Flags().GetString("type")
		labels, _ := cmd.Flags().GetStringSlice("labels")

		// Parse priority
		priority, err := validation.ValidatePriority(priorityStr)
		if err != nil {
			FatalError("%v", err)
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			createArgs := &rpc.CreateArgs{
				Title:     title,
				Priority:  priority,
				IssueType: issueType,
				Labels:    labels,
			}

			resp, err := daemonClient.Create(createArgs)
			if err != nil {
				FatalError("%v", err)
			}

			var issue types.Issue
			if err := json.Unmarshal(resp.Data, &issue); err != nil {
				FatalError("parsing response: %v", err)
			}
			fmt.Println(issue.ID)
			return
		}

		// Direct mode
		issue := &types.Issue{
			Title:     title,
			Status:    types.StatusOpen,
			Priority:  priority,
			IssueType: types.IssueType(issueType),
		}

		ctx := rootCtx
		if err := store.CreateIssue(ctx, issue, actor); err != nil {
			FatalError("%v", err)
		}

		// Add labels if specified (silently ignore failures)
		for _, label := range labels {
			_ = store.AddLabel(ctx, issue.ID, label, actor)
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		// Output only the ID
		fmt.Println(issue.ID)
	},
}

func init() {
	quickCmd.Flags().StringP("priority", "p", "2", "Priority (0-4 or P0-P4)")
	quickCmd.Flags().StringP("type", "t", "task", "Issue type")
	quickCmd.Flags().StringSliceP("labels", "l", []string{}, "Labels")
	rootCmd.AddCommand(quickCmd)
}
