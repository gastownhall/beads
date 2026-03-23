package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/hooks"
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
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("assign")

		id := args[0]
		assignee := args[1]

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
			"assignee": assignee,
		}
		if err := issueStore.UpdateIssue(ctx, result.ResolvedID, updates, actor); err != nil {
			FatalErrorRespectJSON("updating %s: %v", id, err)
		}

		// Flush Dolt commit for embedded mode
		if isEmbeddedDolt && store != nil {
			if _, err := store.CommitPending(ctx, actor); err != nil {
				FatalErrorRespectJSON("failed to commit: %v", err)
			}
		}

		// Run update hook
		updatedIssue, _ := issueStore.GetIssue(ctx, result.ResolvedID)
		if updatedIssue != nil && hookRunner != nil {
			hookRunner.Run(hooks.EventUpdate, updatedIssue)
		}

		SetLastTouchedID(result.ResolvedID)

		title := ""
		if updatedIssue != nil {
			title = updatedIssue.Title
		}
		if jsonOutput {
			if updatedIssue != nil {
				outputJSON(updatedIssue)
			}
		} else {
			if assignee == "" {
				fmt.Printf("%s Unassigned %s\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, title))
			} else {
				fmt.Printf("%s Assigned %s to %s\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, title), assignee)
			}
		}
	},
}

func init() {
	assignCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(assignCmd)
}
