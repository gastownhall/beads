package main

// Bounded execution for the forced closed-wisp purge (GH#4684).
//
// `bd mol wisp gc --closed --force` selects up to 5000 closed wisps and used
// to hand the WHOLE set to one deletion request — one transaction whose
// failure or interruption rolled back (or timed out with) every row, on a
// command whose HQ-sized data is exactly the shape that outgrows a single
// transaction. The bounded deleteWispBatch precedent in
// internal/storage/dolt (200 ids per transaction) was unreachable from both
// CLI routes.
//
// This file owns the command-local chunking both routes share. It is
// deliberately thin: selection, protection filtering and previews stay in
// their routes, and each route keeps its own deletion callback — the direct
// route one Deleter.Delete request per chunk, the proxied route one
// RunTxResult per chunk — so the per-route deletion contracts (cascade off,
// orphaning, is_blocked recompute, text-reference rewrite, retry ownership)
// are unchanged, just applied 200 rows at a time.

import (
	"context"
	"fmt"
	"slices"

	"github.com/steveyegge/beads/internal/ui"
)

// wispClosedGCChunkSize is the per-chunk deletion bound: the number of ids
// one deletion request (direct route) or one unit of work (proxied route)
// carries. Matches the storage layer's bounded deleteWispBatch precedent.
const wispClosedGCChunkSize = 200

// wispClosedGCChunk is one chunk's confirmed deletion result, normalized from
// the direct route's issueops.DeleteResult or the proxied route's
// domain.DeleteIssuesResult.
type wispClosedGCChunk struct {
	Deleted           int
	Dependencies      int
	Labels            int
	Events            int
	ReferencesUpdated int
	Orphaned          []string
	DeletedIDs        []string
}

// wispClosedGCProgress aggregates what the chunks that SUCCEEDED confirmed.
// Every field counts confirmed work only: a failed chunk contributes nothing.
type wispClosedGCProgress struct {
	ConfirmedIDs      []string
	Dependencies      int
	Labels            int
	Events            int
	ReferencesUpdated int
}

// apply folds one confirmed chunk into the aggregate.
func (p *wispClosedGCProgress) apply(chunk wispClosedGCChunk) {
	p.ConfirmedIDs = append(p.ConfirmedIDs, chunk.DeletedIDs...)
	p.Dependencies += chunk.Dependencies
	p.Labels += chunk.Labels
	p.Events += chunk.Events
	p.ReferencesUpdated += chunk.ReferencesUpdated
}

// successFields builds the per-route JSON keys both closed-GC outcomes
// already published (deleted, deleted_count, dependencies_removed,
// labels_removed, events_removed, references_updated), valued from
// confirmed chunk progress. A route adds its own extras (the direct route's
// deleted_ids).
func (p wispClosedGCProgress) successFields() map[string]interface{} {
	return map[string]interface{}{
		"deleted":              slices.Clone(p.ConfirmedIDs),
		"deleted_count":        len(p.ConfirmedIDs),
		"dependencies_removed": p.Dependencies,
		"labels_removed":       p.Labels,
		"events_removed":       p.Events,
		"references_updated":   p.ReferencesUpdated,
	}
}

// runWispClosedGCChunks deletes ids sequentially in chunks of at most
// wispClosedGCChunkSize, in selection order, through deleteChunk.
//
// A chunk error stops the run: no later chunk is sent, no shrink-and-retry
// is attempted, and the confirmed progress is returned beside the original
// cause for the caller to report.
func runWispClosedGCChunks(ctx context.Context, ids []string, deleteChunk func(ctx context.Context, chunk []string) (wispClosedGCChunk, error)) (wispClosedGCProgress, error) {
	var progress wispClosedGCProgress
	for start := 0; start < len(ids); start += wispClosedGCChunkSize {
		end := min(start+wispClosedGCChunkSize, len(ids))
		chunk, err := deleteChunk(ctx, ids[start:end])
		if err != nil {
			return progress, err
		}
		progress.apply(chunk)
	}
	return progress, nil
}

// printWispClosedGCConfirmed renders the confirmed progress in the text
// shape both routes' success output already had.
func printWispClosedGCConfirmed(p wispClosedGCProgress) {
	fmt.Printf("%s Deleted %d issue(s)\n", ui.RenderPass("✓"), len(p.ConfirmedIDs))
	fmt.Printf("  Removed %d dependency link(s)\n", p.Dependencies)
	fmt.Printf("  Removed %d label(s)\n", p.Labels)
	fmt.Printf("  Removed %d event(s)\n", p.Events)
	fmt.Printf("  Updated text references in %d issue(s)\n", p.ReferencesUpdated)
}
