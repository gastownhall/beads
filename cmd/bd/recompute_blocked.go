package main

import (
	"errors"
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
It is idempotent: on a consistent database it changes nothing. Works in every
storage mode — embedded, server, and proxied-server (unlike 'bd doctor', which
is server-mode only).

On a workspace connected to a bd serve backend ('bd connect') it is a no-op
that exits 0: the server maintains is_blocked on every write, and the column
lives in the server's database, so the repair is run there, by the server's
operator. --json then reports {"rows_corrected": 0, "maintained_by": "server"}.

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

		if usesProxiedServer() {
			return runRecomputeBlockedProxiedServer(ctx)
		}

		// One library entry for every backend (storage.RecomputeBlocked): a
		// local store recomputes its own column; a remote backend answers that
		// its server maintains the column, and recomputes nothing.
		result, err := storage.RecomputeBlocked(ctx, store)
		if err != nil {
			var unsup *storage.ErrUnsupported
			if errors.As(err, &unsup) {
				return HandleError("storage backend does not support is_blocked recompute")
			}
			return HandleError("recompute is_blocked: %v", err)
		}
		if result.MaintainedBy == storage.BlockedMaintainedByServer {
			return renderRecomputeBlockedServerMaintained()
		}
		return renderRecomputeBlocked(result.RowsCorrected)
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

// renderRecomputeBlockedServerMaintained renders a remote backend's answer
// (storage.BlockedMaintainedByServer): nothing was recomputed here, because
// the server derives the column and it lives in the server's database — see
// storage.RecomputeBlocked for why that is a no-op rather than a wire
// operation.
//
// It still answers rows_corrected (0) because callers parse it (gc's
// BdStore.RecomputeBlocked requires the member, and wh-bridge-sync reads the
// exit code), and it carries `maintained_by: "server"` so a caller that cares
// can tell "nothing was stale" from "nothing was checked here".
func renderRecomputeBlockedServerMaintained() error {
	if jsonOutput {
		return outputJSON(map[string]interface{}{"rows_corrected": 0, "maintained_by": storage.BlockedMaintainedByServer})
	}
	fmt.Println("is_blocked is maintained by the bd serve backend on every write; nothing to recompute from this client.")
	fmt.Println("To repair a stale flag on the server's database, run bd recompute-blocked in the server's workspace.")
	return nil
}

func init() {
	rootCmd.AddCommand(recomputeBlockedCmd)
}
