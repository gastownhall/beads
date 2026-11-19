package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

// wispPurgeChunkSize bounds each deletion request a forced wisp GC sends
// (bd-d2zqqc, GH#4684; GH#6246). The role runs a request as one transaction,
// so a whole 5000-wisp selection in one request is one transaction that does
// all the work or none of it — and, on a server whose client read timeout is
// shorter than the in-transaction is_blocked recompute, one transaction that
// always times out and rolls back, never reclaiming anything. The same bound
// as the store's own deleteWispBatch.
const wispPurgeChunkSize = 200

// wispPurgeChunk is what one committed deletion request reported.
type wispPurgeChunk struct {
	deletedIDs        []string
	deleted           int
	dependencies      int
	labels            int
	events            int
	referencesUpdated int
	orphaned          []string
}

// wispPurgeOutcome sums the chunks a forced wisp GC committed. confirmed names
// the selected ids whose request committed; err is the failure that stopped
// the GC before the rest of the selection, nil when none did.
type wispPurgeOutcome struct {
	selected          int
	err               error
	confirmed         []string
	deletedIDs        []string
	deleted           int
	dependencies      int
	labels            int
	events            int
	referencesUpdated int
	orphaned          []string
}

// runWispPurgeChunks sends ids to deleteChunk in sequential chunks of
// wispPurgeChunkSize, in selection order, and stops at the first error. The
// chunks committed before it stay committed and stay counted: a later failure
// no longer takes every deletion of the run with it, and a rerun reselects
// whatever is still there. No chunk is retried here; retrying a transaction is
// the role's business.
func runWispPurgeChunks(ctx context.Context, ids []string, deleteChunk func(context.Context, []string) (wispPurgeChunk, error)) wispPurgeOutcome {
	out := wispPurgeOutcome{selected: len(ids), confirmed: []string{}, deletedIDs: []string{}, orphaned: []string{}}
	for chunk := range slices.Chunk(ids, wispPurgeChunkSize) {
		// A canceled command sends no further chunk, whether or not the
		// role it would reach checks the context itself.
		if err := ctx.Err(); err != nil {
			out.err = err
			break
		}
		res, err := deleteChunk(ctx, chunk)
		if err != nil {
			// Whatever the chunk reported beside its error is discarded:
			// an error at a commit boundary does not prove what, if
			// anything, was written (GH#6246), so only an acknowledged
			// result counts.
			out.err = err
			break
		}
		out.confirmed = append(out.confirmed, chunk...)
		out.deletedIDs = append(out.deletedIDs, res.deletedIDs...)
		out.deleted += res.deleted
		out.dependencies += res.dependencies
		out.labels += res.labels
		out.events += res.events
		out.referencesUpdated += res.referencesUpdated
		out.orphaned = append(out.orphaned, res.orphaned...)
	}
	// The role names one request's deletions in ascending id order without
	// duplicates, and so does the whole run, whatever order its chunks went
	// in: the selection is not id order, and a forced run's deleted_ids must
	// still equal the dry run's would_delete_ids for the same rows (GH#6301).
	slices.Sort(out.deletedIDs)
	out.deletedIDs = slices.Compact(out.deletedIDs)
	out.orphaned = survivingOrphans(out.orphaned, out.confirmed, out.deletedIDs)
	return out
}

// survivingOrphans is the orphan report of a chunked GC: each chunk names the
// survivors that depended on what it deleted, and a selected wisp whose
// dependency sat in an earlier chunk is one of them until its own chunk
// deletes it. Anything this run deleted leaves the report, and a row two
// chunks orphaned is named once, in ascending order as the role names them.
func survivingOrphans(orphaned []string, deleted ...[]string) []string {
	gone := make(map[string]bool)
	for _, ids := range deleted {
		for _, id := range ids {
			gone[id] = true
		}
	}
	out := []string{}
	for _, id := range orphaned {
		if !gone[id] {
			out = append(out, id)
			gone[id] = true
		}
	}
	slices.Sort(out)
	return out
}

// remaining is how many selected ids no committed chunk confirmed.
func (o wispPurgeOutcome) remaining() int {
	return o.selected - len(o.confirmed)
}

// wispPurgeRerunHint is how a stopped GC tells its caller to continue. It
// names no command line, so the caller keeps its own flags (--type, --json),
// and it does not promise the failed chunk wrote nothing: a rerun reselects.
const wispPurgeRerunHint = "Rerun the same command to reselect current data and delete the remaining wisps."

// purgeWispIDsDirect deletes ids, which this store's own scan just selected,
// through its deletion role. Both direct GC routes (age and --closed) reach
// it. The ids go to the role as they are: the front door's per-id resolution
// (deleteBatch) would re-read each one to learn what the scan already said.
func purgeWispIDsDirect(ctx context.Context, ids []string) (wispPurgeOutcome, error) {
	deleter, err := store.Deleter()
	if err != nil {
		return wispPurgeOutcome{}, err
	}
	opsCtx, err := issueOpsContext(ctx)
	if err != nil {
		return wispPurgeOutcome{}, err
	}
	requestActor := currentActor()
	return runWispPurgeChunks(opsCtx, ids, func(ctx context.Context, chunk []string) (wispPurgeChunk, error) {
		res, err := deleter.Delete(ctx, issueops.DeleteRequest{
			Actor: requestActor,
			IDs:   chunk,
			Force: true,
		})
		if err != nil {
			return wispPurgeChunk{}, err
		}
		// The bookkeeping deleteBatch does for a successful request, done per
		// committed chunk: see the comment there (GH#5896).
		commandDidWrite.Store(true)
		commandDeletedIssueIDs.add(chunk...)
		return wispPurgeChunk{
			deletedIDs:        res.DeletedIDs,
			deleted:           res.Deleted,
			dependencies:      res.Dependencies,
			labels:            res.Labels,
			events:            res.Events,
			referencesUpdated: res.ReferencesUpdated,
			orphaned:          res.Orphaned,
		}, nil
	}), nil
}

// renderWispPurgeOutcome prints a forced wisp GC's result with the keys each
// route has always printed, plus candidates_capped (scanCapped: the scan
// came back full, so more may remain); withDeletedIDs adds the direct route's
// deleted_ids. A GC a failure stopped prints the same summary of what its
// committed chunks deleted, then the cause and the rerun hint, and returns an
// already-reported exit: under --json all of it is one stdout object
// (remaining_count, error and hint beside the success keys), so a caller that
// reads one JSON value still gets the progress. On failure the stderr object
// the GH#6246 design sketched was folded into that one stdout object on
// purpose: the closed route's published, regression-pinned shape is exactly
// that, and two GC modes answering a partial failure differently is worse
// than either alone.
func renderWispPurgeOutcome(out wispPurgeOutcome, scanCapped, withDeletedIDs bool) error {
	if jsonOutput {
		answer := map[string]interface{}{
			"deleted":              out.confirmed,
			"deleted_count":        out.deleted,
			"dependencies_removed": out.dependencies,
			"labels_removed":       out.labels,
			"events_removed":       out.events,
			"references_updated":   out.referencesUpdated,
			"orphaned_issues":      out.orphaned,
			"candidates_capped":    scanCapped,
		}
		if withDeletedIDs {
			answer["deleted_ids"] = out.deletedIDs
		}
		if out.err != nil {
			answer["remaining_count"] = out.remaining()
			answer["error"] = out.err.Error()
			answer["hint"] = wispPurgeRerunHint
		}
		if err := outputJSON(answer); err != nil {
			return err
		}
		if out.err != nil {
			return SilentExit()
		}
		return nil
	}
	mark := ui.RenderPass("✓")
	if out.err != nil {
		mark = ui.RenderWarn("⚠")
	}
	fmt.Printf("%s Deleted %d issue(s)\n", mark, out.deleted)
	fmt.Printf("  Removed %d dependency link(s)\n", out.dependencies)
	fmt.Printf("  Removed %d label(s)\n", out.labels)
	fmt.Printf("  Removed %d event(s)\n", out.events)
	fmt.Printf("  Updated text references in %d issue(s)\n", out.referencesUpdated)
	if len(out.orphaned) > 0 {
		fmt.Printf("  %s Orphaned %d issue(s): %s\n",
			ui.RenderWarn("⚠"), len(out.orphaned), strings.Join(out.orphaned, ", "))
	}
	if scanCapped {
		fmt.Printf("\nNote: %s.\n", wispPurgeCappedHint)
	}
	if out.err != nil {
		// The counts above are what committed chunks confirmed. The failed
		// chunk is not claimed either way: an error at a commit boundary does
		// not prove nothing was written, which is why the hint reselects.
		return HandleErrorWithHint(fmt.Sprintf("wisp gc stopped after deleting %d of %d selected wisp(s); %d not confirmed deleted: %v",
			len(out.confirmed), out.selected, out.remaining(), out.err), wispPurgeRerunHint)
	}
	return nil
}
