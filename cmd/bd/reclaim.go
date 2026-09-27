package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
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

By default reclaim covers every stale lease THIS replica granted. The scope
filters below narrow it further, using the same label surface claiming is
scoped by (--label / --label-any / --exclude-label), plus --assignee and --id.
Filters AND-combine and never widen the set: a reclaimed lease must still be
stale.

Replicas and leases (federated deployments)
-------------------------------------------
A lease is only meaningful on the replica that granted it. Every other
replica's view of the holder's liveness is stale by up to one sync interval,
so a reaper elsewhere can revert a unit that is very much alive over there.
reclaim therefore records the granting replica on each lease and SKIPS a lease
another replica granted, summarizing what it declined on stderr (one line per
run; 'bd -v' expands it to the first 20 leases individually). Reap it where it
was granted; use --any-replica only when that replica is permanently gone (or
when this node was renamed and its own old leases now look foreign — an
ordinary heartbeat keeps a lease alive but does not re-home it to the node
heartbeating it). Prefer the narrow form '--any-replica --id <id>': bare
--any-replica reverts EVERY foreign stale lease, live peers included.

Two invariants the guard cannot enforce for you:

  grace window > sync interval, and lease TTL > sync interval.

A TTL or grace shorter than the cadence at which replicas exchange state is
meaningless across the bridge — the remote view is a full interval old by
construction. Raise the TTL/grace above the sync interval, never the reverse.
The guard is opt-in: set BEADS_NODE_ID, or run 'bd config set node_id <name>'
(which writes the per-machine ~/.config/bd/config.yaml — never commit a node_id
to the git-tracked .beads/config.yaml, or every clone reads the same name and
the guard goes armed-but-inert). One id per STORE, not per host: machines that
are clients of the same dolt sql-server are ONE replica and must share one value
or leave it unset. There is no hostname fallback — the hostname names the client
process's machine, not the store — so an unnamed deployment keeps the old,
unguarded behavior instead of stranding its own work.

Examples:
  bd reclaim                       # default grace window (2× the lease TTL)
  bd reclaim --older-than 10m      # reclaim leases expired >10m ago
  bd reclaim --older-than 0s       # reclaim every currently-expired lease
  bd reclaim --label lane-a        # only this machine's claim partition
  bd reclaim --label-any lane-a,lane-b --exclude-label pinned
  bd reclaim --assignee zelda --assignee epona   # only these workers' leases
  bd reclaim --id wy-abc --id wy-def             # exactly these issues
  bd reclaim --any-replica         # also reap leases granted by a departed replica`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
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

		filter, err := reclaimFilterFromFlags(cmd)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		CheckReadonly("reclaim")

		if usesProxiedServer() {
			return runReclaimProxiedServer(rootCtx, olderThan, filter)
		}

		ctx := rootCtx
		reclaimed, err := store.ReclaimExpiredLeases(ctx, olderThan, filter, actor)
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

		return renderReclaim(reclaimed, !filter.IsEmpty())
	},
}

func renderReclaim(reclaimed []types.ReclaimedLease, scoped bool) error {
	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"reclaimed": reclaimed,
			"count":     len(reclaimed),
			// Whether any scope filter was in effect, so a supervisor auditing
			// its own reclaim log can tell a scoped sweep from a global one.
			"scoped": scoped,
		})
	}
	if len(reclaimed) == 0 {
		if scoped {
			fmt.Printf("%s No stale leases to reclaim in the filtered scope\n", ui.RenderPass("✓"))
			return nil
		}
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
}

// registerReclaimScopeFlags declares the scope filters — the same label surface
// as the claim side (bd ready --claim), so a fleet can reclaim exactly the
// partition it claims from. Split out of init() so tests can exercise the real
// flag set on a fresh command instead of a hand-copied one.
func registerReclaimScopeFlags(fs *pflag.FlagSet) {
	fs.StringSlice("id", nil, "Only reclaim these issue IDs (repeatable)")
	fs.StringSliceP("assignee", "a", nil, "Only reclaim leases held by these assignees (repeatable)")
	fs.StringSliceP("label", "l", nil, "Only reclaim issues with ALL these labels (AND). Can combine with --label-any")
	fs.StringSlice("label-any", nil, "Only reclaim issues with AT LEAST ONE of these labels (OR). Can combine with --label")
	fs.StringSlice("exclude-label", nil, "Never reclaim issues carrying ANY of these labels")
	fs.Bool("any-replica", false,
		"Also reclaim leases granted by ANOTHER replica (unsafe unless that replica is gone; see 'Replicas and leases')")
}

// reclaimFilterFromFlags maps the scope flags onto a types.ReclaimFilter,
// rejecting a flag that was SUPPLIED but carries no usable value.
//
// The hard error is the point. `bd reclaim --label "$LANE"` with LANE unset
// parses to an empty slice, which would otherwise be indistinguishable from
// "no --label at all" — i.e. a supervisor's scoped sweep silently degrading
// into a global one that reaps every stale lease in a federated database. A
// scope flag that resolves to nothing is operator error, not a wildcard.
func reclaimFilterFromFlags(cmd *cobra.Command) (types.ReclaimFilter, error) {
	get := func(name string) ([]string, error) {
		values, err := cmd.Flags().GetStringSlice(name)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", name, err)
		}
		if !cmd.Flags().Changed(name) {
			return nil, nil
		}
		var kept []string
		for _, v := range values {
			if strings.TrimSpace(v) != "" {
				kept = append(kept, v)
			}
		}
		if len(kept) == 0 {
			return nil, fmt.Errorf("--%s was given no usable value (an empty scope flag would reclaim everything; drop the flag to sweep globally)", name)
		}
		return kept, nil
	}

	var filter types.ReclaimFilter
	var err error
	if filter.IDs, err = get("id"); err != nil {
		return types.ReclaimFilter{}, err
	}
	if filter.Assignees, err = get("assignee"); err != nil {
		return types.ReclaimFilter{}, err
	}
	if filter.Labels, err = get("label"); err != nil {
		return types.ReclaimFilter{}, err
	}
	if filter.LabelsAny, err = get("label-any"); err != nil {
		return types.ReclaimFilter{}, err
	}
	if filter.ExcludeLabels, err = get("exclude-label"); err != nil {
		return types.ReclaimFilter{}, err
	}
	// --any-replica is an override, not a scope: it WIDENS the set past the
	// granting-replica guard, so it deliberately skips the empty-value hard
	// error above (a bool flag has no empty-value hazard) and never counts
	// toward "scoped" in the reclaim report.
	if filter.AnyReplica, err = cmd.Flags().GetBool("any-replica"); err != nil {
		return types.ReclaimFilter{}, fmt.Errorf("--any-replica: %w", err)
	}
	return filter, nil
}

func init() {
	reclaimCmd.Flags().Duration("older-than", 2*issueops.DefaultLeaseTTL,
		"Only reclaim leases that expired at least this long ago (grace window)")
	registerReclaimScopeFlags(reclaimCmd.Flags())
	rootCmd.AddCommand(reclaimCmd)
}
