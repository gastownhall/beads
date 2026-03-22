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
		// Use global jsonOutput set by PersistentPreRun
		ctx := rootCtx
		reopenedIssues := []*types.Issue{}
		hadError := false
		// Direct storage access
		if store == nil {
			FatalErrorWithHint("database not initialized",
				"run 'bd doctor' to diagnose, or 'bd init' to create a new database")
		}
		for _, id := range args {
			result, err := resolveAndGetIssueWithRouting(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				hadError = true
				continue
			}
			fullID := result.ResolvedID
			issueStore := result.Store
			issue := result.Issue
			// Skip if already open — avoid false "Reopened" message
			if issue.Status == types.StatusOpen {
				result.Close()
				fmt.Fprintf(os.Stderr, "%s is already open\n", fullID)
				continue
			}
			// UpdateIssue automatically clears closed_at when status changes from closed.
			// Also clear defer_until so the issue appears in bd ready immediately.
			// Without this, a deferred issue that was closed and reopened stays
			// hidden from bd ready despite being "open".
			updates := map[string]interface{}{
				"status":      string(types.StatusOpen),
				"defer_until": nil,
			}
			if err := issueStore.UpdateIssue(ctx, fullID, updates, actor); err != nil {
				result.Close()
				fmt.Fprintf(os.Stderr, "Error reopening %s: %v\n", fullID, err)
				hadError = true
				continue
			}
			// Add reason as a comment if provided
			if reason != "" {
				if err := issueStore.AddComment(ctx, fullID, actor, reason); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to add comment to %s: %v\n", fullID, err)
				}
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
		if isEmbeddedDolt && store != nil {
			if _, err := store.CommitPending(ctx, actor); err != nil {
				FatalErrorRespectJSON("failed to commit: %v", err)
			}
		}

		if jsonOutput && len(reopenedIssues) > 0 {
			outputJSON(reopenedIssues)
		}

		if hadError {
			os.Exit(1)
		}
	},
}

func init() {
	reopenCmd.Flags().StringP("reason", "r", "", "Reason for reopening")
	reopenCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(reopenCmd)
}
