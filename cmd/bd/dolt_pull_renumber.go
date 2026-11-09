package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/steveyegge/beads/internal/storage"
)

// printChildRenumberHint runs after a failed pull (bd-28sb25, GH#4796): when
// the failure is local hierarchical children colliding with remote ones, it
// shows the renames `--renumber-children` would make. Best effort and
// read-only: a backend without a planner, or a plan that fails, adds nothing
// to the pull's own error.
func printChildRenumberHint(ctx context.Context, st storage.DoltStorage, remote string) {
	planner, ok := storage.UnwrapStore(st).(storage.ChildRenumberPlanner)
	if !ok {
		return
	}
	report, err := planner.PlanChildRenumbers(ctx, remote)
	if err != nil || report == nil {
		return
	}
	writeChildRenumberHint(os.Stderr, report, remote)
}

// writeChildRenumberHint writes the proposed renames and the flag to apply
// them; nothing when there are no colliding children.
func writeChildRenumberHint(w io.Writer, report *storage.ChildRenumberReport, remote string) {
	if len(report.Renames) == 0 {
		return
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Local child issues collide with different remote children under the same ids.")
	fmt.Fprintln(w, "Renumbering the local ones would keep the remote ids and move yours:")
	for _, r := range report.Renames {
		fmt.Fprintf(w, "  %s -> %s\n", r.From, r.To)
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "To apply this (the plan is re-checked when it runs):")
	if remote != "" {
		fmt.Fprintf(w, "  bd dolt pull %s --renumber-children\n", remote)
	} else {
		fmt.Fprintln(w, "  bd dolt pull --renumber-children")
	}
}
