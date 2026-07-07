package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/ui"
)

var heartbeatCmd = &cobra.Command{
	Use:     "heartbeat <id>",
	Aliases: []string{"hb"},
	GroupID: "issues",
	Short:   "Refresh the lease on an issue you hold in_progress",
	Long: `Refresh the lease on an issue you currently hold in_progress.

A claim carries a lease that expires after a TTL. A worker keeps its claim alive
by heartbeating faster than the TTL; once it stops (because it died), the lease
goes stale and 'bd reclaim' reverts the issue to ready so another worker can pick
it up. Heartbeat pushes lease_expires_at forward and stamps heartbeat_at = now.

Only the current owner may heartbeat. If the lease has already been reclaimed or
the issue closed, heartbeat fails so the worker learns to stop.

Heartbeat writes a Dolt commit, so heartbeat well below the TTL but not so fast
it bloats history — cadence should be a small fraction of the TTL, not per-op.

Examples:
  bd heartbeat bd-123
  bd hb bd-123`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("heartbeat")

		evt := metrics.NewCommandEvent("heartbeat")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ctx := rootCtx
		id := args[0]

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
		if err := issueStore.HeartbeatIssue(ctx, result.ResolvedID, actor); err != nil {
			return HandleErrorRespectJSON("heartbeat %s: %v", result.ResolvedID, err)
		}

		if err := commitPendingIfEmbedded(ctx, issueStore, actor, doltAutoCommitParams{
			Command:  "heartbeat",
			IssueIDs: []string{result.ResolvedID},
		}); err != nil {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		SetLastTouchedID(result.ResolvedID)

		if jsonOutput {
			return outputJSON(map[string]string{
				"id":     result.ResolvedID,
				"status": "heartbeat",
				"owner":  actor,
			})
		}
		fmt.Printf("%s Heartbeat %s (lease refreshed)\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, result.Issue.Title))
		return nil
	},
}

func init() {
	heartbeatCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(heartbeatCmd)
}
