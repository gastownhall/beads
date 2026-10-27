package storage

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// DescendantSearcher is an optional DoltStorage capability: the --parent tree
// walk (WalkDescendants) run inside one read transaction, so the whole tree
// is one consistent snapshot instead of one engine-open per node. It follows
// the ExpiredDeferWaker pattern: a narrow optional interface the concrete
// stores implement and callers probe through UnwrapStore, so decorators are
// bypassed for this one read rather than forced to forward it.
type DescendantSearcher interface {
	SearchDescendants(ctx context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error)
}

// WalkDescendants is the --parent tree walk: depth-first, one search per node
// with a value copy of filter whose ParentID and Limit (0) alone are
// re-pointed, first sighting wins, rootID itself not pre-seeded, results in
// first-sighting order. Cycles converge because a revisited node is already in
// the seen set. search is one node's children query — SearchIssuesInTx inside
// the caller's transaction, or SearchIssues for the per-node fallback.
func WalkDescendants(ctx context.Context, rootID string, filter types.IssueFilter,
	search func(context.Context, types.IssueFilter) ([]*types.Issue, error)) ([]*types.Issue, error) {
	var out []*types.Issue
	seen := make(map[string]bool)
	var visit func(node string) error
	visit = func(node string) error {
		nodeFilter := filter
		nodeFilter.ParentID = &node
		nodeFilter.Limit = 0 // unlimited per level to avoid truncating the tree walk
		children, err := search(ctx, nodeFilter)
		if err != nil {
			return err
		}
		for _, child := range children {
			if seen[child.ID] {
				continue
			}
			seen[child.ID] = true
			out = append(out, child)
			if err := visit(child.ID); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(rootID); err != nil {
		return nil, err
	}
	return out, nil
}

// SearchDescendants runs the --parent tree walk through the innermost
// store's DescendantSearcher when it has one — the whole tree inside one
// engine-open/read transaction — and node by node through s.SearchIssues
// otherwise. A store whose capability read fails does not fall back: the
// one-transaction read is also the correctness contract (a single
// snapshot for the whole tree), not just a fast path.
func SearchDescendants(ctx context.Context, s DoltStorage, rootID string, filter types.IssueFilter) ([]*types.Issue, error) {
	if ds, ok := UnwrapStore(s).(DescendantSearcher); ok {
		return ds.SearchDescendants(ctx, rootID, filter)
	}
	return WalkDescendants(ctx, rootID, filter, func(ctx context.Context, f types.IssueFilter) ([]*types.Issue, error) {
		return s.SearchIssues(ctx, "", f)
	})
}
