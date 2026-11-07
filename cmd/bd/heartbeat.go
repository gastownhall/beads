package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/metrics"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
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

Leases live in an ephemeral, node-local table: a heartbeat that refreshes your
lease writes no Dolt commit and no history, so any cadence comfortably below
the TTL is fine. Only a re-arm (the first heartbeat of a claim that has no
lease, such as one set by a plain update) is recorded as a change to the issue
and committed, once. Leases are only enforceable on the node that granted them;
cross-machine claim visibility rides the issue's status and assignee, which do
commit.

The lease machinery is gated by heartbeat.enabled (on by default, and turned off
with heartbeat.enabled false in .beads/config.yaml or BD_HEARTBEAT_ENABLED=0).
With it off, a claim records assignee/status only and carries no lease: this
command still resolves the issue and checks that you hold the claim, then
reports the refresh as skipped, so an unconditional worker heartbeat loop keeps
exiting 0.

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

		// One role on every route: the store's own
		// Heartbeater accessor on the direct routes — dolt, embedded dolt and
		// a registered HTTP backend, where it dials issues.heartbeat and an
		// older server is refused before the dial naming the token — and the
		// proxied-server provider's on the proxied one. On every leg a refresh
		// writes ONLY the lease row (no revision remint, no history, no Dolt
		// commit); a re-arm is recorded as a mutation and committed once.
		//
		// heartbeat.enabled deliberately does NOT short-circuit above this
		// dispatch. On the proxied route the lease is armed and refreshed
		// inside the SERVER's transaction, where RenewLeaseInTx resolves
		// heartbeat.enabled from the server's own configuration; returning
		// early on this process's copy of the flag is how a client that
		// disagrees with the server stops refreshing a lease the server
		// armed, after which the server's bd reclaim reverts a live worker's
		// claim out from under it (gastownhall/beads#7506). Let the storage
		// layer decide; the flag near the bottom only picks the wording of
		// the success line.
		if usesProxiedServer() {
			return runHeartbeatProxied(ctx, id)
		}

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
		heartbeater, err := issueStore.Heartbeater()
		if err != nil {
			return HandleErrorRespectJSON("heartbeat %s: %v", result.ResolvedID, err)
		}
		if _, err := heartbeater.Heartbeat(ctx, issueops.HeartbeatRequest{
			Actor:   currentActor(),
			IssueID: result.ResolvedID,
		}); err != nil {
			return HandleErrorRespectJSON("heartbeat %s: %v", result.ResolvedID, err)
		}

		if err := commitPendingIfEmbedded(ctx, issueStore, currentActor(), doltAutoCommitParams{
			Command:  "heartbeat",
			IssueIDs: []string{result.ResolvedID},
		}); err != nil {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		SetLastTouchedID(result.ResolvedID)

		// Reached only after the storage layer resolved the issue and
		// confirmed the caller holds the claim, so "skipped" means "nothing
		// to refresh", never "did not look".
		if !storeops.HeartbeatEnabled(ctx) {
			return renderHeartbeatDisabled(result.ResolvedID)
		}

		return renderHeartbeatSuccess(result.ResolvedID, result.Issue.Title)
	},
}

// renderHeartbeatDisabled is bd heartbeat with heartbeat.enabled=false
// (gastownhall/beads#7506): exit 0, and the SAME --json shape as
// renderHeartbeatSuccess, so a worker loop that heartbeats on a timer keeps
// parsing its output. Only the human line differs, and it says why. Both
// routes call it only after the storage layer resolved the issue and
// confirmed the caller holds the claim, so "skipped" means "nothing to
// refresh", never "did not look".
func renderHeartbeatDisabled(id string) error {
	if jsonOutput {
		return outputJSON(map[string]string{
			"id":     id,
			"status": "heartbeat",
			"owner":  currentActor(),
		})
	}
	fmt.Printf("%s Heartbeat %s skipped (heartbeat.enabled=false)\n", ui.RenderSkipIcon(), formatFeedbackID(id, ""))
	return nil
}

// renderHeartbeatSuccess prints the success shape every storage route shares —
// the direct path and the proxied-server path (runHeartbeatProxied) call
// exactly this code, so the --json contract workers parse
// ({"id","status":"heartbeat","owner"}) cannot drift between modes.
func renderHeartbeatSuccess(id, title string) error {
	if jsonOutput {
		return outputJSON(map[string]string{
			"id":     id,
			"status": "heartbeat",
			"owner":  currentActor(),
		})
	}
	fmt.Printf("%s Heartbeat %s (lease refreshed)\n", ui.RenderPass("✓"), formatFeedbackID(id, title))
	return nil
}

func init() {
	heartbeatCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(heartbeatCmd)
}
