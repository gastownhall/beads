package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/ui"
)

var reclaimCmd = &cobra.Command{
	Use:     "reclaim",
	GroupID: "issues",
	Short:   "Revert stale-lease in_progress issues back to ready (dead-worker recovery)",
	Long: `Revert in_progress issues whose lease has gone stale back to ready.

When a worker claims an issue it takes a lease that expires after a TTL, kept
alive by 'bd heartbeat'. A worker that dies stops heartbeating, so its lease
expires and its issue would otherwise stay in_progress forever. reclaim is the
reaper: it finds in_progress issues whose lease expired more than --older-than
ago, clears the assignee, and sets them back to open so another worker can
claim them. The previous owner's stale lease is recorded as a recovery event.

--older-than is a grace window past lease expiry: only leases that expired at
least this long ago are reclaimed, so a worker briefly paused (GC, clock skew)
is not robbed of live work. Run it from a supervisor on a timer with a window
of roughly 2× the claim TTL.

Examples:
  bd reclaim                       # default grace window (2× the lease TTL)
  bd reclaim --older-than 10m      # reclaim leases expired >10m ago
  bd reclaim --older-than 0s       # reclaim every currently-expired lease`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("reclaim")

		evt := metrics.NewCommandEvent("reclaim")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		olderThan, _ := cmd.Flags().GetDuration("older-than")
		if olderThan < 0 {
			return HandleErrorRespectJSON("--older-than must not be negative")
		}

		ctx := rootCtx
		reclaimed, err := store.ReclaimExpiredLeases(ctx, olderThan, actor)
		if err != nil {
			return HandleErrorRespectJSON("reclaim: %v", err)
		}

		ids := make([]string, 0, len(reclaimed))
		for _, r := range reclaimed {
			ids = append(ids, r.ID)
		}
		if err := commitPendingIfEmbedded(ctx, store, actor, doltAutoCommitParams{
			Command:  "reclaim",
			IssueIDs: ids,
		}); err != nil {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"reclaimed": reclaimed,
				"count":     len(reclaimed),
			})
		}
		if len(reclaimed) == 0 {
			fmt.Printf("%s No stale leases to reclaim\n", ui.RenderPass("✓"))
			return nil
		}
		fmt.Printf("%s Reclaimed %d stale-lease issue(s):\n", ui.RenderPass("✓"), len(reclaimed))
		for _, r := range reclaimed {
			owner := r.PreviousOwner
			if owner == "" {
				owner = "(unassigned)"
			}
			fmt.Printf("  %s (was held by %s)\n", r.ID, owner)
		}
		return nil
	},
}

func init() {
	reclaimCmd.Flags().Duration("older-than", 2*issueops.DefaultLeaseTTL,
		"Only reclaim leases that expired at least this long ago (grace window)")
	rootCmd.AddCommand(reclaimCmd)
}
