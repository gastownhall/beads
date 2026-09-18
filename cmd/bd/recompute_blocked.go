package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
)

var (
	recomputeBlockedStatus bool
	recomputeBlockedFix    bool
)

var recomputeBlockedCmd = &cobra.Command{
	Use:     "recompute-blocked",
	GroupID: "maint",
	Short:   "Recompute is_blocked for all issues (repairs stale flags after a pull)",
	Long: `Recompute the denormalized is_blocked flag for every issue and wisp.

is_blocked is derived from the dependency graph and maintained automatically by
local writes and by a post-pull recompute scoped to what the merge changed. If
that scoped recompute is skipped — a recompute that failed after its merge
committed, or a conflicted pull resolved by hand — the flag can go stale, and a
later pull that merges nothing will not refresh it (bd-6dnrw.37). 'bd ready'
trusts the flag, so stale values silently hide ready work or surface blocked
work.

This command runs the full recompute unconditionally and commits the result.
It is idempotent: on a consistent database it changes nothing. Works in every
storage mode — embedded, server, and proxied-server (unlike 'bd doctor', which
is server-mode only).

--status checks a different drift instead: issues/wisps left at the manually-
set status='blocked' that the dependency graph no longer holds blocked — every
blocker closed, or none was ever recorded (be-ntbxt). Nothing else clears that
status, so 'bd close --suggest-next' can report an issue as newly unblocked
while it stays invisible to 'bd ready' forever. A row still held blocked by any
edge type is left alone. --status is report-only unless paired with --fix.

Examples:
  bd recompute-blocked                  # Repair stale is_blocked flags
  bd recompute-blocked --json           # Machine-parseable {"rows_corrected": N}
  bd recompute-blocked --status         # Report status=blocked drift, embedded mode included
  bd recompute-blocked --status --fix   # Repair it: drifted rows return to status=open`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		CheckReadonly("recompute-blocked")

		evt := metrics.NewCommandEvent("recompute-blocked")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ctx := rootCtx

		if recomputeBlockedStatus {
			if usesProxiedServer() {
				return HandleError("recompute-blocked --status: proxied-server mode not yet supported")
			}
			drifter, ok := storage.UnwrapStore(store).(storage.StatusBlockedDriftRecomputer)
			if !ok {
				return HandleError("storage backend does not support status=blocked drift check")
			}
			if recomputeBlockedFix {
				changed, err := drifter.FixStatusBlockedDrift(ctx)
				if err != nil {
					return HandleError("fix status=blocked drift: %v", err)
				}
				return renderStatusBlockedDrift(changed, true)
			}
			count, err := drifter.CountStatusBlockedDrift(ctx)
			if err != nil {
				return HandleError("count status=blocked drift: %v", err)
			}
			return renderStatusBlockedDrift(count, false)
		}

		if usesProxiedServer() {
			return runRecomputeBlockedProxiedServer(ctx)
		}

		recomputer, ok := storage.UnwrapStore(store).(storage.BlockedRecomputer)
		if !ok {
			return HandleError("storage backend does not support is_blocked recompute")
		}
		changed, err := recomputer.RecomputeAllBlocked(ctx)
		if err != nil {
			return HandleError("recompute is_blocked: %v", err)
		}
		return renderRecomputeBlocked(changed)
	},
}

// renderRecomputeBlocked writes the command's whole result, and is shared by
// every mode so the text and the JSON shape downstream callers read cannot
// drift between them. wh-bridge-sync only reads the exit code, but a human
// repairing a stale graph reads these two lines.
func renderRecomputeBlocked(changed int) error {
	if jsonOutput {
		return outputJSON(map[string]interface{}{"rows_corrected": changed})
	}
	if changed == 0 {
		fmt.Println("is_blocked already consistent — nothing to recompute.")
		return nil
	}
	fmt.Printf("Recomputed is_blocked: %d row(s) corrected.\n", changed)
	return nil
}

// renderStatusBlockedDrift writes the --status check's whole result. fixed
// distinguishes a report-only run (nothing mutated) from one that also ran
// --fix, so the same count means something different in each case.
func renderStatusBlockedDrift(count int, fixed bool) error {
	if jsonOutput {
		key := "status_blocked_drift"
		if fixed {
			key = "status_blocked_fixed"
		}
		return outputJSON(map[string]interface{}{key: count})
	}
	if count == 0 {
		fmt.Println("No status=blocked drift — every blocked issue still has an open blocker.")
		return nil
	}
	if !fixed {
		fmt.Printf("%d issue(s) marked status=blocked with no open blocker (drift). Run with --fix to repair.\n", count)
		return nil
	}
	fmt.Printf("Fixed status=blocked drift: %d issue(s) returned to status=open.\n", count)
	return nil
}

func init() {
	rootCmd.AddCommand(recomputeBlockedCmd)
	recomputeBlockedCmd.Flags().BoolVar(&recomputeBlockedStatus, "status", false,
		"Check status='blocked' issues/wisps for drift against the dependency graph (be-ntbxt), instead of recomputing is_blocked")
	recomputeBlockedCmd.Flags().BoolVar(&recomputeBlockedFix, "fix", false,
		"With --status, repair drifted rows back to status='open' (report-only without it)")
}
