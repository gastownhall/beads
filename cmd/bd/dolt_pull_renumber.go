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

// runPullRenumberChildren is `bd dolt pull --renumber-children`: the pull
// renames this clone's colliding children before it merges, so the remote's
// identities win and the local issues survive under fresh ids.
func runPullRenumberChildren(ctx context.Context, st storage.DoltStorage, remote string) error {
	puller, ok := storage.UnwrapStore(st).(storage.ChildRenumberingPuller)
	if !ok {
		return HandleError("storage backend %T does not support --renumber-children", storage.UnwrapStore(st))
	}
	if remote != "" {
		fmt.Printf("Pulling from Dolt remote %q, renumbering colliding children...\n", remote)
	} else {
		fmt.Println("Pulling from Dolt remote, renumbering colliding children...")
	}
	report, err := puller.PullRenumberChildren(ctx, remote, getActor())
	if report != nil {
		writeChildRenumberResult(os.Stdout, report)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		if report != nil && report.RenameCommit != "" {
			fmt.Fprintf(os.Stderr, "\nThe renames are committed (%s) and kept; resolve the error and pull again.\n", report.RenameCommit)
		}
		return SilentExit()
	}
	fmt.Println("Pull complete.")
	return nil
}

// writeChildRenumberResult reports the renames a renumbering pull made.
func writeChildRenumberResult(w io.Writer, report *storage.ChildRenumberReport) {
	if len(report.Renames) == 0 {
		fmt.Fprintln(w, "No colliding children to renumber.")
		return
	}
	fmt.Fprintf(w, "Renumbered local children (commit %s):\n", report.RenameCommit)
	for _, r := range report.Renames {
		fmt.Fprintf(w, "  %s -> %s\n", r.From, r.To)
	}
}
