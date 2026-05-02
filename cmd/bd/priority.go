package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/validation"
)

var priorityCmd = &cobra.Command{
	Use:     "priority <id> <n>",
	GroupID: "issues",
	Short:   "Set the priority of an issue",
	Long: `Set the priority of an issue.

Shorthand for 'bd update <id> --priority <n>'.

Priority levels:
  0 - Critical (security, data loss, broken builds)
  1 - High (major features, important bugs)
  2 - Medium (default)
  3 - Low (polish, optimization)
  4 - Backlog (future ideas)

Examples:
  bd priority bd-123 0    # Critical
  bd priority bd-123 2    # Medium`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("priority")

		id := args[0]
		priorityStr := args[1]

		priority, err := validation.ValidatePriority(priorityStr)
		if err != nil {
			FatalErrorRespectJSON("%v", err)
		}

		ctx := rootCtx

		result, err := resolveAndGetIssueWithRouting(ctx, store, id)
		if err != nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("resolving %s: %v", id, err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("issue %s not found", id)
		}
		defer result.Close()

		issueStore := result.Store

		if err := validateIssueUpdatable(id, result.Issue); err != nil {
			FatalErrorRespectJSON("%s", err)
		}

		updates := map[string]interface{}{
			"priority": priority,
		}
		if err := issueStore.UpdateIssue(ctx, result.ResolvedID, updates, actor); err != nil {
			FatalErrorRespectJSON("updating %s: %v", id, err)
		}

		commandDidWrite.Store(true)

		SetLastTouchedID(result.ResolvedID)

		// Re-fetch for display
		updatedIssue, _ := issueStore.GetIssue(ctx, result.ResolvedID)
		title := ""
		if updatedIssue != nil {
			title = updatedIssue.Title
		}
		if jsonOutput {
			if updatedIssue != nil {
				outputJSON(updatedIssue)
			}
		} else {
			fmt.Printf("%s Set priority of %s to P%d\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, title), priority)
		}
	},
}

func init() {
	priorityCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(priorityCmd)
}
