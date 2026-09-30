package externaldeps

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/internal/workapi/storereader"
	"github.com/steveyegge/beads/internal/workapi/storereadycounter"
	"github.com/steveyegge/beads/internal/workapi/storereadylister"
	publicops "github.com/steveyegge/beads/issueops"
)

// The fake store hands out roles the way the Dolt stores do — the shared
// store-backed bodies over its own methods — so the policy's role wrappers
// delegate to something that honors ReadyRequest.ExcludeIDs exactly as a
// real backend role does (through the filter the shared builder produces).

func (f *fakeStore) IssueReader() (publicops.Reader, error) { return storereader.New(f) }

func (f *fakeStore) ReadyCounter() (publicops.ReadyCounter, error) {
	return storereadycounter.New(f)
}

func (f *fakeStore) ReadyLister() (publicops.ReadyLister, error) {
	return storereadylister.New(f)
}

func (f *fakeStore) ReadyClaimer() (publicops.ReadyClaimer, error) {
	return &fakeReadyClaimer{store: f}, nil
}

func (f *fakeStore) CountReadyWork(ctx context.Context, filter types.WorkFilter) (int, error) {
	filter.Limit, filter.Offset = 0, 0
	issues, err := f.GetReadyWork(ctx, filter)
	return len(issues), err
}

func (f *fakeStore) SearchIssuesWithCounts(ctx context.Context, _ string, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	rows := make([]*types.IssueWithCounts, 0, len(filter.IDs))
	for _, id := range filter.IDs {
		row, err := f.GetIssue(ctx, id)
		if err != nil {
			return nil, err
		}
		if row != nil {
			rows = append(rows, &types.IssueWithCounts{Issue: row})
		}
	}
	return rows, nil
}

// fakeReadyClaimer is a backend ReadyClaimer: wake, then select and claim
// through the store's own ClaimReadyIssue, then hydrate.
type fakeReadyClaimer struct {
	store storage.DoltStorage
	wake  func(context.Context)
}

func (c *fakeReadyClaimer) ClaimNext(ctx context.Context, req publicops.ClaimNextRequest) (publicops.ClaimNextResult, error) {
	filter, err := workapi.BuildReadyFilter(req.Filter)
	if err != nil {
		return publicops.ClaimNextResult{}, err
	}
	if c.wake != nil {
		c.wake(ctx)
	}
	claimed, err := c.store.ClaimReadyIssue(ctx, filter, req.Actor)
	if err != nil || claimed == nil {
		return publicops.ClaimNextResult{}, err
	}
	rows, err := c.store.SearchIssuesWithCounts(ctx, "", types.IssueFilter{IDs: []string{claimed.ID}})
	if err != nil {
		return publicops.ClaimNextResult{}, err
	}
	if len(rows) != 1 {
		return publicops.ClaimNextResult{}, fmt.Errorf("hydrate %s: got %d rows", claimed.ID, len(rows))
	}
	return publicops.ClaimNextResult{Claimed: rows[0]}, nil
}

// BatchCloser records what reached the backend's closer.
func (f *fakeStore) BatchCloser() (publicops.BatchCloser, error) {
	return &fakeBatchCloser{store: f}, nil
}

type fakeBatchCloser struct {
	store    *fakeStore
	requests []publicops.CloseBatchRequest
}

func (c *fakeBatchCloser) CloseBatch(_ context.Context, req publicops.CloseBatchRequest) (publicops.CloseBatchResult, error) {
	c.requests = append(c.requests, req)
	c.store.batchReqs = append(c.store.batchReqs, req)
	c.store.closed = append(c.store.closed, "batch")
	outcomes := make([]publicops.CloseOutcome, len(req.Items))
	for i, item := range req.Items {
		outcomes[i] = publicops.CloseOutcome{IssueID: item.IssueID}
	}
	return publicops.CloseBatchResult{Outcomes: outcomes}, nil
}
