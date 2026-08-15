package issueops

import (
	"context"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

// ValidateBlockingRequest applies the one request rule every BlockingAnnotator
// implementation shares: an empty id entry is refused rather than answered
// with a nameless annotation. An empty ID SLICE asks about nothing and gets
// nothing back rather than being an error.
func ValidateBlockingRequest(request publicops.BlockingRequest) error {
	for i, id := range request.IDs {
		if id == "" {
			return fmt.Errorf("%w: annotate blocking id %d is empty", storage.ErrValidation, i)
		}
	}
	return nil
}

// FinishBlockingAnnotation assembles the per-id answer from the three maps every
// implementation produces.
//
// It is one function rather than two copies for the reason FinishEdgeRead is:
// the entry-per-id shape, the pinned order and the collapse of repeats are the
// whole observable contract of this role, and two implementations applying them
// separately will eventually disagree. Both bodies below it answer with maps
// whose slice values arrive in query order and can carry the same id twice —
// once from each dependency tier.
//
// The maps are read, never written: the store body's caller owns them.
func FinishBlockingAnnotation(
	anchors []string,
	blockedBy map[string][]string,
	blocks map[string][]string,
	parent map[string]string,
) publicops.BlockingResult {
	out := publicops.BlockingResult{Items: make([]publicops.IssueBlocking, 0, len(anchors))}
	for _, id := range anchors {
		out.Items = append(out.Items, publicops.IssueBlocking{
			ID:        id,
			BlockedBy: sortedDistinctIDs(blockedBy[id]),
			Blocks:    sortedDistinctIDs(blocks[id]),
			Parent:    parent[id],
		})
	}
	return out
}

// sortedDistinctIDs is the pinned order: ascending, repeats collapsed, never
// nil. It copies rather than sorting in place, because the slices it is handed
// belong to the maps a body just built and a caller may still read them.
func sortedDistinctIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ExecuteBlockingAnnotation returns each id's blocking decoration in tx. It is
// the store-backed body behind the BlockingAnnotator accessor; the unit-of-work
// provider has its own, which reaches the same three maps through its use case.
//
// The outbound read, the inbound read and the status lookups that decide which
// edges are live all run in ONE transaction, which is what lets the answer mean
// what it says: a blocker read as open by one statement and closed by the next
// would produce a row reported both blocked and blocking in a single response.
func ExecuteBlockingAnnotation(ctx context.Context, tx DBTX, request publicops.BlockingRequest) (publicops.BlockingResult, error) {
	anchors := EdgeReadAnchors(request.IDs)
	if len(anchors) == 0 {
		return publicops.BlockingResult{Items: []publicops.IssueBlocking{}}, nil
	}
	blockedBy, blocks, parent, err := GetBlockingInfoForIssuesInTx(ctx, tx, anchors)
	if err != nil {
		return publicops.BlockingResult{}, err
	}
	return FinishBlockingAnnotation(anchors, blockedBy, blocks, parent), nil
}
