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
stops heartbeating loses its claim to 'bd reclaim'. Deployments whose
recovery authority lives elsewhere — an orchestrator with its own liveness
evidence — disarm automatic stamping so an un-renewed fleet is never one
stray reclaim away from mass-reverting live work. Explicit leases requested
AFTER disarming remain available and reclaimable; leases existing at disarm
time are cleared by the sweep regardless of how they were requested.`,
}

var leaseDisarmCmd = &cobra.Command{
	Use:   "disarm",
	Short: "Turn off automatic claim leases and clear the armed ones",
	Long: `Set lease.auto=off and NULL the lease columns on existing in_progress
rows — the flip and the first sweep share one transaction, and bounded
follow-up sweeps catch claims that were in flight during the flip. Nothing
is released:
status and assignee are untouched, and the ownership fence does not move.

After disarming, claims carry no lease unless one is explicitly requested,
heartbeats on unleased claims are rejected (exit non-zero, they never arm a
lease as a side effect), and 'bd reclaim' only ever touches explicitly
requested leases.

Re-arm with: bd config set ` + issueops.LeaseAutoConfigKey + ` on
(existing claims stay unleased until re-claimed or explicitly leased).`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("lease disarm")

		if usesProxiedServer() {
			return HandleErrorRespectJSON("bd lease disarm is not supported in proxied-server mode yet; use `bd config set %s off` for the flip (armed leases must then expire or be cleared manually)", issueops.LeaseAutoConfigKey)
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
		fmt.Printf("%s Automatic claim leases disarmed (%s=off); cleared %d in-flight lease(s)\n",
			ui.RenderPass("✓"), issueops.LeaseAutoConfigKey, n)
		return nil
	},
}

func init() {
	leaseCmd.AddCommand(leaseDisarmCmd)
	rootCmd.AddCommand(leaseCmd)
}
