package storage

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// DescendantSearcher is an optional DoltStorage capability: the --parent tree
// walk (WalkDescendants) run inside one read transaction, so the whole tree is
// one snapshot and, on the embedded backend, one engine open rather than one
// per tree node (GH#3415). It is not a DoltStorage method so the http client
// store and the decorators need not implement it; SearchDescendants finds it
// behind them the way the ExpiredDeferWaker lookup does.
type DescendantSearcher interface {
	SearchDescendants(ctx context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error)
}

// WalkDescendants is the --parent tree walk: depth-first, one search per node
// with a value copy of filter whose ParentID and Limit (0, so no level is
// truncated) alone are re-pointed. The first sighting of an issue wins and is
// the only one walked, so a cycle ends; rootID itself is not pre-seeded, so a
// cycle back to it reports it once. Results are in first-sighting order.
func WalkDescendants(ctx context.Context, rootID string, filter types.IssueFilter,
	search func(context.Context, types.IssueFilter) ([]*types.Issue, error)) ([]*types.Issue, error) {
	seen := make(map[string]bool)
	var found []*types.Issue
	var walk func(parentID string) error
	walk = func(parentID string) error {
		level := filter
		level.ParentID = &parentID
		level.Limit = 0
		children, err := search(ctx, level)
		if err != nil {
			return err
		}
		for _, child := range children {
			if seen[child.ID] {
				continue
			}
			seen[child.ID] = true
			found = append(found, child)
			if err := walk(child.ID); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(rootID); err != nil {
		return nil, err
	}
	return found, nil
}

// SearchDescendants runs the walk through the innermost store's
// DescendantSearcher when it has one, else node by node through s.SearchIssues.
// The capability path bypasses the decorators for this one read, as the
// ExpiredDeferWaker lookup does; none of them changes what SearchIssues
// returns.
func SearchDescendants(ctx context.Context, s DoltStorage, rootID string, filter types.IssueFilter) ([]*types.Issue, error) {
	if ds, ok := UnwrapStore(s).(DescendantSearcher); ok {
		return ds.SearchDescendants(ctx, rootID, filter)
	}
	return WalkDescendants(ctx, rootID, filter, func(ctx context.Context, f types.IssueFilter) ([]*types.Issue, error) {
		return s.SearchIssues(ctx, "", f)
	})
}
