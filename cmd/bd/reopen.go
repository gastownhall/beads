package main
import (
	"encoding/json"
	"fmt"
	"os"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
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
		// Resolve partial IDs first
		var resolvedIDs []string
		if daemonClient != nil {
			for _, id := range args {
				resolveArgs := &rpc.ResolveIDArgs{ID: id}
				resp, err := daemonClient.ResolveID(resolveArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error resolving ID %s: %v\n", id, err)
					os.Exit(1)
				}
				var resolvedID string
				if err := json.Unmarshal(resp.Data, &resolvedID); err != nil {
					fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
					os.Exit(1)
				}
				resolvedIDs = append(resolvedIDs, resolvedID)
			}
		} else {
			var err error
			resolvedIDs, err = utils.ResolvePartialIDs(ctx, store, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
		reopenedIssues := []*types.Issue{}
		// If daemon is running, use RPC
		if daemonClient != nil {
			for _, id := range resolvedIDs {
				openStatus := string(types.StatusOpen)
				updateArgs := &rpc.UpdateArgs{
					ID:     id,
					Status: &openStatus,
				}
				resp, err := daemonClient.Update(updateArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error reopening %s: %v\n", id, err)
					continue
				}
				// Add reason as a comment if provided
				if reason != "" {
					commentArgs := &rpc.CommentAddArgs{
						ID:     id,
						Author: actor,
						Text:   reason,
					}
					if _, err := daemonClient.AddComment(commentArgs); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to add comment to %s: %v\n", id, err)
					}
				}
				if jsonOutput {
					var issue types.Issue
					if err := json.Unmarshal(resp.Data, &issue); err == nil {
						reopenedIssues = append(reopenedIssues, &issue)
					}
				} else {
					reasonMsg := ""
					if reason != "" {
						reasonMsg = ": " + reason
					}
					fmt.Printf("%s Reopened %s%s\n", ui.RenderAccent("↻"), id, reasonMsg)
				}
			}
			if jsonOutput && len(reopenedIssues) > 0 {
				outputJSON(reopenedIssues)
			}
			return
		}
		// Fall back to direct storage access
		if store == nil {
			fmt.Fprintln(os.Stderr, "Error: database not initialized")
			os.Exit(1)
		}
		for _, id := range args {
			fullID, err := utils.ResolvePartialID(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				continue
			}
			// UpdateIssue automatically clears closed_at when status changes from closed
			updates := map[string]interface{}{
				"status": string(types.StatusOpen),
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
		// Schedule auto-flush if any issues were reopened
		if len(args) > 0 {
			markDirtyAndScheduleFlush()
		}
		if jsonOutput && len(reopenedIssues) > 0 {
			outputJSON(reopenedIssues)
		}
	},
}
func init() {
	reopenCmd.Flags().StringP("reason", "r", "", "Reason for reopening")
	rootCmd.AddCommand(reopenCmd)
}
