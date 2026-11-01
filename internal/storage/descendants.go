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
// behind them.
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

// SearchDescendants runs the walk through the outermost layer of s that is a
// DescendantSearcher, checking each layer before unwrapping it, else node by
// node through s.SearchIssues. A decorator that offers the capability
// therefore gets the walk first (telemetry.InstrumentedStorage traces it, then
// hands it on to the layers beneath), and one that does not is bypassed for
// this one read; none of the decorators changes what SearchIssues returns.
func SearchDescendants(ctx context.Context, s DoltStorage, rootID string, filter types.IssueFilter) ([]*types.Issue, error) {
	layer := s
	for {
		if ds, ok := layer.(DescendantSearcher); ok {
			return ds.SearchDescendants(ctx, rootID, filter)
		}
		u, ok := layer.(Unwrapper)
		if !ok {
			break
		}
		layer = u.Unwrap()
	}
	return WalkDescendants(ctx, rootID, filter, func(ctx context.Context, f types.IssueFilter) ([]*types.Issue, error) {
		return s.SearchIssues(ctx, "", f)
	})
}
