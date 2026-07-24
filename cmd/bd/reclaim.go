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

By default reclaim is global: every stale lease in the database is fair game.
The scope filters below narrow it, using the same label surface claiming is
scoped by (--label / --label-any / --exclude-label), plus --assignee and --id.
Scoping matters on a federated deployment: each replica's view of the other
machine's liveness is stale by up to one sync interval, so an unscoped reaper on
one machine can revert a unit that is alive on the machine that granted its
lease. Point each supervisor's reclaim at its own claim partition and that
cannot happen. Filters AND-combine and never widen the set: a reclaimed lease
must still be stale.

Examples:
  bd reclaim                       # default grace window (2× the lease TTL)
  bd reclaim --older-than 10m      # reclaim leases expired >10m ago
  bd reclaim --older-than 0s       # reclaim every currently-expired lease
  bd reclaim --label lane-a        # only this machine's claim partition
  bd reclaim --label-any lane-a,lane-b --exclude-label pinned
  bd reclaim --assignee zelda --assignee epona   # only these workers' leases
  bd reclaim --id wy-abc --id wy-def             # exactly these issues`,
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
	return filter, nil
}

func init() {
	reclaimCmd.Flags().Duration("older-than", 2*issueops.DefaultLeaseTTL,
		"Only reclaim leases that expired at least this long ago (grace window)")
	registerReclaimScopeFlags(reclaimCmd.Flags())
	rootCmd.AddCommand(reclaimCmd)
}
