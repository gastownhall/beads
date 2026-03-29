package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var reopenCmd = &cobra.Command{
	Use:     "reopen [id...]",
	GroupID: "issues",
	Short:   "Reopen one or more closed issues",
	Long: `Reopen closed issues by setting status to 'open' and clearing the closed_at timestamp.
This is more explicit than 'bd update --status open' and emits a Reopened event.`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("reopen")
		reason, _ := cmd.Flags().GetString("reason")
		ctx := rootCtx

		reopenedIssues := []*types.Issue{}
		hasError := false
		if store == nil {
			FatalErrorWithHint("database not initialized",
				diagHint())
		}
		for _, id := range args {
			// Resolve with prefix routing (supports cross-rig reopens like `bd reopen xe-5ls`)
			result, err := resolveAndGetIssueWithRouting(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				hasError = true
				continue
			}
			fullID := result.ResolvedID
			issueStore := result.Store
			issue := result.Issue

			// Skip if already open — avoid false "Reopened" message
			if issue.Status == types.StatusOpen {
				fmt.Fprintf(os.Stderr, "%s is already open\n", fullID)
				result.Close()
				continue
			}
			if err := issueStore.ReopenIssue(ctx, fullID, reason, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error reopening %s: %v\n", fullID, err)
				hasError = true
				result.Close()
				continue
			}
			if jsonOutput {
				updated, _ := issueStore.GetIssue(ctx, fullID)
				if updated != nil {
					reopenedIssues = append(reopenedIssues, updated)
				}
			} else {
				reasonMsg := ""
				if reason != "" {
					reasonMsg = ": " + reason
				}
				fmt.Printf("%s Reopened %s%s\n", ui.RenderAccent("↻"), fullID, reasonMsg)
			}
			result.Close()
		}

		// Embedded mode: flush Dolt commit.
		if isEmbeddedMode() && store != nil {
			if _, err := store.CommitPending(ctx, actor); err != nil {
				FatalErrorRespectJSON("failed to commit: %v", err)
			}
		}

		if jsonOutput && len(reopenedIssues) > 0 {
			outputJSON(reopenedIssues)
		}

		if hasError {
			os.Exit(1)
		}
	},
}

func init() {
	reopenCmd.Flags().StringP("reason", "r", "", "Reason for reopening")
	reopenCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(reopenCmd)
}
