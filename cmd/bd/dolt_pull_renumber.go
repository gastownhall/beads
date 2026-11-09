package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

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
	if jsonOutput {
		report, err := puller.PullRenumberChildren(ctx, remote, getActor())
		if report == nil && err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		if werr := writeChildRenumberJSON(os.Stdout, report, err); werr != nil {
			return werr
		}
		if err != nil {
			return SilentExit()
		}
		return nil
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
		if report != nil {
			kept := report.PriorRenameCommits
			if report.RenameCommit != "" {
				kept = append([]string{report.RenameCommit}, kept...)
			}
			if len(kept) > 0 {
				fmt.Fprintf(os.Stderr, "\nThe renames are committed (%s) and kept; resolve the error and pull again.\n", strings.Join(kept, ", "))
			}
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
	if len(report.Mentions) == 0 {
		return
	}
	fmt.Fprintln(w, "Text naming a renamed id, left as written:")
	for _, m := range report.Mentions {
		if m.Field == "comment" {
			fmt.Fprintf(w, "  %s comment %s: %s (now %s)\n", m.IssueID, m.CommentID, m.ID, m.RenamedTo)
		} else {
			fmt.Fprintf(w, "  %s %s: %s (now %s)\n", m.IssueID, m.Field, m.ID, m.RenamedTo)
		}
	}
}

// writeChildRenumberJSON writes a renumbering pull's result, and the pull's
// error when it failed after reporting, as one JSON object.
func writeChildRenumberJSON(w io.Writer, report *storage.ChildRenumberReport, pullErr error) error {
	result := struct {
		*storage.ChildRenumberReport
		Error string `json:"error,omitempty"`
	}{ChildRenumberReport: report}
	if pullErr != nil {
		result.Error = pullErr.Error()
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(wrapWithSchemaVersion(result)); err != nil {
		return fmt.Errorf("encoding JSON: %v", err)
	}
	return nil
}
