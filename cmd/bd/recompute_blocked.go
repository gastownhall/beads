package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
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
It is idempotent: on a consistent database it changes nothing. Works in both
embedded and server mode (unlike 'bd doctor', which is server-mode only).

Examples:
  bd recompute-blocked          # Repair stale is_blocked flags
  bd recompute-blocked --json   # Machine-parseable {"rows_corrected": N}`,
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

		recomputer, ok := storage.UnwrapStore(store).(storage.BlockedRecomputer)
		if !ok {
			return HandleError("storage backend does not support is_blocked recompute")
		}
		changed, err := recomputer.RecomputeAllBlocked(ctx)
		if err != nil {
			return HandleError("recompute is_blocked: %v", err)
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{"rows_corrected": changed})
		}
		if changed == 0 {
			fmt.Println("is_blocked already consistent — nothing to recompute.")
			return nil
		}
		fmt.Printf("Recomputed is_blocked: %d row(s) corrected.\n", changed)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(recomputeBlockedCmd)
}
