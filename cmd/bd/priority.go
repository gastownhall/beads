package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
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
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("priority")

		evt := metrics.NewCommandEvent("priority")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runPriorityProxiedServer(rootCtx, args)
		}

		id := args[0]
		priorityStr := args[1]

		priority, err := validation.ValidatePriority(priorityStr)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		ctx := rootCtx

		result, err := resolveAndGetIssueForMutation(ctx, store, id)
		if err != nil {
			if result != nil {
				result.Close()
			}
			return HandleErrorRespectJSON("resolving %s: %v", id, err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			return HandleErrorRespectJSON("issue %s not found", id)
		}
		defer result.Close()

		issueStore := result.Store

		if err := validateIssueUpdatable(id, result.Issue); err != nil {
			return HandleErrorRespectJSON("%s", err)
		}

		updates := map[string]interface{}{
			"priority": priority,
		}
		if err := issueStore.UpdateIssue(ctx, result.ResolvedID, updates, currentActor()); err != nil {
			return HandleErrorRespectJSON("updating %s: %v", id, err)
		}
		if err := commitPendingIfEmbedded(ctx, issueStore, currentActor(), doltAutoCommitParams{
			Command:  "priority",
			IssueIDs: []string{result.ResolvedID},
		}); err != nil {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		SetLastTouchedID(result.ResolvedID)

		if jsonOutput {
			updatedIssue, _ := issueStore.GetIssue(ctx, result.ResolvedID)
			if updatedIssue != nil {
				return outputJSON(updatedIssue)
			}
			return nil
		}
		// The title is as of the resolution read, a transaction before the
		// write: a rename another process commits in between prints the old
		// title. It only labels the confirmation, so no read after the write.
		fmt.Printf("%s Set priority of %s to P%d\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, result.Issue.Title), priority)
		return nil
	},
}

func init() {
	priorityCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(priorityCmd)
}
