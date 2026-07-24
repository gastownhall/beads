package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var reopenCmd = &cobra.Command{
	Use:     "reopen [id...]",
	GroupID: "issues",
	Short:   "Reopen one or more closed issues",
	Long: `Reopen closed issues by setting status to 'open' and clearing the closed_at timestamp.
This is more explicit than 'bd update --status open' and emits a Reopened event.`,
	Args:          cobra.MinimumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("reopen")

		evt := metrics.NewCommandEvent("reopen")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runReopenProxiedServer(cmd, rootCtx, args)
		}

		reason, _ := cmd.Flags().GetString("reason")
		ctx := rootCtx

		reopenedIssues := []*types.Issue{}
		hasError := false
		mutatedStores := map[storage.DoltStorage][]string{}
		pendingCloseResults := []*RoutedResult{}
		if store == nil {
			return HandleErrorWithHint("database not initialized", diagHint())
		}
		for _, id := range args {
			// Resolve with prefix routing (supports cross-rig reopens like `bd reopen xe-5ls`)
			result, err := resolveAndGetIssueForMutation(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				hasError = true
				continue
			}
			fullID := result.ResolvedID
			issueStore := result.Store
			issue := result.Issue

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
			mutatedStores[issueStore] = append(mutatedStores[issueStore], fullID)
			pendingCloseResults = append(pendingCloseResults, result)
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
		}

		for s, ids := range mutatedStores {
			if err := commitPendingIfEmbedded(ctx, s, actor, doltAutoCommitParams{
				Command:  "reopen",
				IssueIDs: ids,
			}); err != nil {
				for _, result := range pendingCloseResults {
					result.Close()
				}
				return HandleErrorRespectJSON("failed to commit: %v", err)
			}
		}
		for _, result := range pendingCloseResults {
			result.Close()
		}

		if jsonOutput && len(reopenedIssues) > 0 {
			if jerr := outputJSON(reopenedIssues); jerr != nil {
				return jerr
			}
		}

		if hasError {
			return SilentExit()
		}
		return nil
	},
}

func init() {
	reopenCmd.Flags().StringP("reason", "r", "", "Reason for reopening")
	reopenCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(reopenCmd)
}
