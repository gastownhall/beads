package storage

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// IssuesWithConfigSearcher is an optional DoltStorage capability: SearchIssues
// and the GetConfig of one key run inside one read transaction, so a caller
// that narrows the issues by a workspace setting narrows them by the value the
// same snapshot held. The retention doors outside the Sweeper role (`bd gc`'s
// decay, `bd admin cleanup` and the stale-closed `bd doctor --fix`) select
// through it with types.retained. It is not a DoltStorage method so the http
// client store and the decorators need not implement it;
// SearchIssuesWithConfig finds it behind them.
type IssuesWithConfigSearcher interface {
	SearchIssuesWithConfig(ctx context.Context, query string, filter types.IssueFilter, key string) ([]*types.Issue, string, error)
}

// SearchIssuesWithConfig runs the search and the config read through the
// outermost layer of s that is an IssuesWithConfigSearcher, checking each
// layer before unwrapping it, else as s.SearchIssues and then s.GetConfig: two
// reads, on a store with no read transaction to share them (the http client,
// whose wire has none). A decorator that does not offer the capability is
// bypassed for this one read; none of the decorators changes what either read
// returns.
func SearchIssuesWithConfig(ctx context.Context, s DoltStorage, query string, filter types.IssueFilter, key string) ([]*types.Issue, string, error) {
	layer := s
	for {
		if searcher, ok := layer.(IssuesWithConfigSearcher); ok {
			return searcher.SearchIssuesWithConfig(ctx, query, filter, key)
		}
		u, ok := layer.(Unwrapper)
		if !ok {
			break
		}
		layer = u.Unwrap()
	}
	issues, err := s.SearchIssues(ctx, query, filter)
	if err != nil {
		return nil, "", err
	}
	value, err := s.GetConfig(ctx, key)
	if err != nil {
		return nil, "", err
	}
	return issues, value, nil
}
