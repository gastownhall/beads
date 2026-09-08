package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/ui"
)

var assignCmd = &cobra.Command{
	Use:     "assign <id> <name>",
	GroupID: "issues",
	Short:   "Assign an issue to someone",
	Long: `Assign an issue to someone.

Shorthand for 'bd update <id> --assignee <name>'.

Examples:
  bd assign bd-123 alice
  bd assign bd-123 ""      # unassign`,
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("assign")

		id := args[0]
		assignee := args[1]

		evt := metrics.NewCommandEvent("assign")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

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
			"assignee": assignee,
		}
		if err := issueStore.UpdateIssue(ctx, result.ResolvedID, updates, actor); err != nil {
			return HandleErrorRespectJSON("updating %s: %v", id, err)
		}

		if err := commitPendingIfEmbedded(ctx, issueStore, actor, doltAutoCommitParams{
			Command:  "assign",
			IssueIDs: []string{result.ResolvedID},
		}); err != nil {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		SetLastTouchedID(result.ResolvedID)

		updatedIssue, _ := issueStore.GetIssue(ctx, result.ResolvedID)
		title := ""
		if updatedIssue != nil {
			title = updatedIssue.Title
		}
		if jsonOutput {
			if updatedIssue != nil {
				if err := outputJSON(updatedIssue); err != nil {
					return err
				}
			}
		} else {
			if assignee == "" {
				fmt.Printf("%s Unassigned %s\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, title))
			} else {
				fmt.Printf("%s Assigned %s to %s\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, title), assignee)
			}
		}
		return nil
	},
}

func init() {
	assignCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(assignCmd)
}
