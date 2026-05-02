package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ui"
)

var tagCmd = &cobra.Command{
	Use:     "tag <id> <label>",
	GroupID: "issues",
	Short:   "Add a label to an issue",
	Long: `Add a label to an issue.

Shorthand for 'bd update <id> --add-label <label>'.

Examples:
  bd tag bd-123 bug
  bd tag bd-123 needs-review`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("tag")

		id := args[0]
		label := args[1]

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

		if err := issueStore.AddLabel(ctx, result.ResolvedID, label, actor); err != nil {
			FatalErrorRespectJSON("adding label to %s: %v", id, err)
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
			fmt.Printf("%s Added label %q to %s\n", ui.RenderPass("✓"), label, formatFeedbackID(result.ResolvedID, title))
		}
	},
}

func init() {
	tagCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(tagCmd)
}
