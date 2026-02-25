package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
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
		// Resolve partial IDs
		_, err := utils.ResolvePartialIDs(ctx, store, args)
		if err != nil {
			FatalError("%v", err)
		}
		reopenedIssues := []*types.Issue{}
		// Direct storage access
		if store == nil {
			FatalErrorWithHint("database not initialized",
				"run 'bd init' to create a database")
		}
		for _, id := range args {
			fullID, err := utils.ResolvePartialID(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				continue
			}
			// Skip if already open — avoid false "Reopened" message
			issue, err := store.GetIssue(ctx, fullID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting %s: %v\n", fullID, err)
				continue
			}
			if issue.Status == types.StatusOpen {
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
			if err := store.UpdateIssue(ctx, fullID, updates, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error reopening %s: %v\n", fullID, err)
				continue
			}
			// Add reason as a comment if provided
			if reason != "" {
				if err := store.AddComment(ctx, fullID, actor, reason); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to add comment to %s: %v\n", fullID, err)
				}
			}
			if jsonOutput {
				issue, _ := store.GetIssue(ctx, fullID)
				if issue != nil {
					reopenedIssues = append(reopenedIssues, issue)
				}
			} else {
				reasonMsg := ""
				if reason != "" {
					reasonMsg = ": " + reason
				}
				fmt.Printf("%s Reopened %s%s\n", ui.RenderAccent("↻"), fullID, reasonMsg)
			}
		}
		if jsonOutput && len(reopenedIssues) > 0 {
			outputJSON(reopenedIssues)
		}
	},
}

func init() {
	reopenCmd.Flags().StringP("reason", "r", "", "Reason for reopening")
	reopenCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(reopenCmd)
}
