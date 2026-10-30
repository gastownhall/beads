package issueops

import (
	"context"
	"fmt"
	"slices"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	publicops "github.com/steveyegge/beads/issueops"
)

// BlockedFilter is the WorkFilter a BlockedRequest compiles to: the filter
// `bd blocked` has always handed GetBlockedIssues, built from the same four
// flags. Labels are normalized as every other label filter normalizes them
// (trimmed, blanks and repeats dropped): these clauses match a label EXACTLY,
// so an untrimmed `--label 'a, b'` value would otherwise match nothing.
//
// It never writes through the request's slices (utils.NormalizeLabels
// returns a new one), and it sets no page: the listing has none.
func BlockedFilter(req publicops.BlockedRequest) types.WorkFilter {
	var filter types.WorkFilter
	if req.ParentID != "" {
		parentID := req.ParentID
		filter.ParentID = &parentID
	}
	filter.Labels = utils.NormalizeLabels(req.Labels)
	filter.LabelsAny = utils.NormalizeLabels(req.LabelsAny)
	filter.ExcludeLabels = utils.NormalizeLabels(req.ExcludeLabels)
	return filter
}

// BlockedResultOf finishes a blocked listing for the role: never nil, and in
// the role's total order (priority ascending, created_at descending, id). A
// row's blocker slice is never nil either, so it encodes as [] like the
// count beside it says.
//
// The order is restated rather than trusted because the store body breaks no
// tie between two rows of equal priority created in the same instant (it
// sorts rows assembled from a map); the id makes the listing one answer on
// every leg. The external-dependency decorator already finishes with the
// same three keys, so this is a no-op over it.
func BlockedResultOf(items []*types.BlockedIssue) publicops.BlockedResult {
	out := make([]*types.BlockedIssue, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		if item.BlockedBy == nil {
			clone := *item
			clone.BlockedBy = []string{}
			item = &clone
		}
		out = append(out, item)
	}
	slices.SortStableFunc(out, func(a, b *types.BlockedIssue) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			if a.CreatedAt.After(b.CreatedAt) {
				return -1
			}
			return 1
		}
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return publicops.BlockedResult{Items: out}
}

// ExecuteListBlockedInTx is the store-backed body behind
// issueops.BlockedLister: GetBlockedIssuesInTx over the request's filter,
// finished by BlockedResultOf, inside the caller's read transaction.
func ExecuteListBlockedInTx(ctx context.Context, tx DBTX, req publicops.BlockedRequest) (publicops.BlockedResult, error) {
	items, err := GetBlockedIssuesInTx(ctx, tx, BlockedFilter(req))
	if err != nil {
		return publicops.BlockedResult{}, err
	}
	return BlockedResultOf(items), nil
}

// BlockedIssuesReader is the legacy blocked read a BlockedLister can be built
// over: storage.DoltStorage's GetBlockedIssues, and the external-dependency
// decorator's override of it.
type BlockedIssuesReader interface {
	GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error)
}

// NewBlockedListerOver returns the BlockedLister that answers through
// reader's GetBlockedIssues. It is how a decorator that overrides that read
// (externaldeps.Store) hands out the role with its own decoration, so the
// role and the legacy read cannot answer differently.
func NewBlockedListerOver(reader BlockedIssuesReader) (publicops.BlockedLister, error) {
	if reader == nil {
		return nil, fmt.Errorf("new blocked lister: reader must not be nil")
	}
	return blockedListerOver{reader: reader}, nil
}

type blockedListerOver struct{ reader BlockedIssuesReader }

func (l blockedListerOver) ListBlocked(ctx context.Context, req publicops.BlockedRequest) (publicops.BlockedResult, error) {
	items, err := l.reader.GetBlockedIssues(ctx, BlockedFilter(req))
	if err != nil {
		return publicops.BlockedResult{}, err
	}
	return BlockedResultOf(items), nil
}
