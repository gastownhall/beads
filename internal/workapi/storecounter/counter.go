// Package storecounter holds the store-backed implementation of
// issueops.Counter: one shared body that every store-shaped backend's Counter
// accessor hands back.
//
// It is a package of its own for the reason internal/workapi/storereader is —
// see that package's doc. Down here the only importers are the two Dolt store
// packages, and the cmd-bd-role-constructors depguard rule in .golangci.yml
// makes a front door importing it a lint failure rather than a review comment.
package storecounter

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// New returns the issue-count surface backed by a store handle. *DoltStore and
// *EmbeddedDoltStore answer identically because the difference between them is
// below storage.DoltStorage, not above it.
func New(store storage.DoltStorage) (issueops.Counter, error) {
	if store == nil {
		return nil, &issueops.ErrUnsupported{Op: "storecounter.New", Backend: "nil"}
	}
	return &storeCounter{store: store}, nil
}

type storeCounter struct{ store storage.DoltStorage }

var _ issueops.Counter = (*storeCounter)(nil)

func (c *storeCounter) Count(ctx context.Context, req issueops.CountRequest) (issueops.CountResult, error) {
	filter, err := c.filter(ctx, req)
	if err != nil {
		return issueops.CountResult{}, err
	}
	total, err := c.store.CountIssues(ctx, "", filter)
	if err != nil {
		return issueops.CountResult{}, err
	}
	return issueops.CountResult{Total: total}, nil
}

func (c *storeCounter) CountByGroup(ctx context.Context, req issueops.CountByGroupRequest) (issueops.CountByGroupResult, error) {
	// The dimension is validated BEFORE the filter is built and before either
	// query runs, so a misspelled group costs no round trip and cannot half-run.
	group, err := workapi.ValidateCountGroup(req.GroupBy)
	if err != nil {
		return issueops.CountByGroupResult{}, err
	}
	filter, err := c.filter(ctx, req.Filter)
	if err != nil {
		return issueops.CountByGroupResult{}, err
	}
	groups, err := c.store.CountIssuesByGroup(ctx, filter, group)
	if err != nil {
		return issueops.CountByGroupResult{}, err
	}
	// The scalar total, not the sum of the buckets: label buckets overlap, so
	// summing them counts a multi-label issue once per label. This is the two
	// queries CountByGroupResult.Total warns are not one snapshot on this
	// backend — the store seam offers no transaction spanning them.
	total, err := c.store.CountIssues(ctx, "", filter)
	if err != nil {
		return issueops.CountByGroupResult{}, err
	}
	if groups == nil {
		groups = map[string]int{}
	}
	return issueops.CountByGroupResult{Groups: groups, Total: total}, nil
}

// filter builds the storage filter, loading the workspace configuration only
// when the request can read it.
//
// TWO THINGS ARE DELIBERATE. The load is SKIPPED unless IncludeInfra is set,
// because that flag is the only thing in a count request the configuration
// reaches. And when it does run it runs PER CALL rather than once at
// construction: the infra vocabulary is workspace state a caller can change
// between two counts, and a counter that cached it would answer for the older
// workspace.
func (c *storeCounter) filter(ctx context.Context, req issueops.CountRequest) (types.IssueFilter, error) {
	var cfg workapi.ListConfig
	if req.IncludeInfra {
		loaded, err := workapi.LoadStoreListConfig(ctx, c.store)
		if err != nil {
			return types.IssueFilter{}, err
		}
		cfg = loaded
	}
	return workapi.BuildCountFilter(req, cfg)
}
