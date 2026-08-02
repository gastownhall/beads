package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
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
		opsCtx, err := issueOpsContext(ctx)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

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
			ops, err := writeOps(issueStore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reopening %s: %v\n", fullID, err)
				hasError = true
				result.Close()
				continue
			}
			reopened, err := ops.Reopen(opsCtx, issueops.ReopenRequest{
				Actor:   actor,
				IssueID: fullID,
				Reason:  reason,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reopening %s: %v\n", fullID, err)
				hasError = true
				result.Close()
				continue
			}
			if !reopened.Changed {
				// RULING R4: only literal closed and configured done statuses
				// reopen. Anything else was never closed, so there is nothing to
				// report as reopened, nothing to commit, and no hook to fire —
				// the same "nothing to do" shape as the already-open skip above.
				fmt.Fprintf(os.Stderr, "%s is not closed (status: %s); nothing to do\n", fullID, reopenStatusOf(reopened.Issue, issue))
				result.Close()
				continue
			}
			mutatedStores[issueStore] = append(mutatedStores[issueStore], fullID)
			pendingCloseResults = append(pendingCloseResults, result)
			if jsonOutput {
				// The operation's own post-state snapshot replaces the re-read.
				// Dependency records are dropped because `bd reopen` has never
				// printed them.
				if updated := reopened.Issue; updated != nil {
					updated.Dependencies = nil
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

// reopenStatusOf reports the status a no-op reopen left in place, preferring
// the operation's post-state snapshot over the pre-read it was based on.
func reopenStatusOf(post, pre *types.Issue) types.Status {
	if post != nil {
		return post.Status
	}
	if pre != nil {
		return pre.Status
	}
	return ""
}

func init() {
	reopenCmd.Flags().StringP("reason", "r", "", "Reason for reopening")
	reopenCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(reopenCmd)
}
