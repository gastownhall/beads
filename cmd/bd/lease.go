package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/ui"
)

var leaseCmd = &cobra.Command{
	Use:     "lease",
	GroupID: "issues",
	Short:   "Manage automatic claim leases",
	Long: `Manage the store's automatic claim-lease behavior.

By default every claim stamps a lease (` + "`lease.auto` on" + `): a worker that
stops heartbeating loses its claim to 'bd reclaim'. Deployments whose recovery
authority lives elsewhere — an orchestrator that already holds its own liveness
evidence — can disarm automatic stamping, so an un-renewed fleet is never one
stray reclaim away from mass-reverting live work.`,
}

var leaseDisarmCmd = &cobra.Command{
	Use:   "disarm",
	Short: "Turn off automatic claim leases and clear the armed ones",
	Long: `Set ` + issueops.LeaseAutoConfigKey + `=off and clear the lease rows of the claims
already holding one. The flip and the first sweep share one transaction, and
bounded follow-up sweeps catch claims that were in flight during the flip.

Nothing is released: status, assignee and the claim fence (the claim_fence
value in 'bd show --json', the token '--if-fence' guards on) are untouched.
Only the leases go, so nothing in_progress is left for 'bd reclaim' to revert.

After disarming, claims carry no lease, 'bd heartbeat' on an unleased claim is
rejected rather than quietly arming one, and 'bd reclaim' finds nothing to
reap. This is a one-shot sweep, not a standing rejection: an import that
carries a live lease from another replica still restores it.

Re-arm with: bd config set ` + issueops.LeaseAutoConfigKey + ` on
(existing claims stay unleased until they are re-claimed).`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("lease disarm")

		if usesProxiedServer() {
			return HandleErrorWithHintRespectJSON(
				"lease disarm is not supported in proxied-server mode",
				fmt.Sprintf("run 'bd config set %s off' for the flip; leases already armed then have to expire or be cleared by hand",
					issueops.LeaseAutoConfigKey))
		}

		evt := metrics.NewCommandEvent("lease-disarm")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if store == nil {
			return HandleErrorWithHint("database not initialized", diagHint())
		}

		n, err := store.DisarmAutoLeases(rootCtx)
		if err != nil {
			return HandleErrorRespectJSON("lease disarm: %v", err)
		}
		if err := commitPendingIfEmbedded(rootCtx, store, actor, doltAutoCommitParams{
			Command: "lease disarm",
		}); err != nil {
			return HandleErrorRespectJSON("lease disarm commit: %v", err)
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"lease_auto": "off",
				"disarmed":   n,
			})
		}
		fmt.Printf("%s Automatic claim leases disarmed (%s=off); cleared %d lease(s)\n",
			ui.RenderPass("✓"), issueops.LeaseAutoConfigKey, n)
		return nil
	},
}

func init() {
	leaseCmd.AddCommand(leaseDisarmCmd)
	rootCmd.AddCommand(leaseCmd)
}
