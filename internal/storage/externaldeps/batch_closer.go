package externaldeps

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// BatchCloser preserves external policy on bd close, including its next claim.
func (s *Store) BatchCloser() (issueops.BatchCloser, error) {
	if s.rolesAreRemote() {
		return &remoteBatchCloser{policy: s}, nil
	}
	return &batchCloser{policy: s}, nil
}

type batchCloser struct{ policy *Store }

func (c *batchCloser) CloseBatch(ctx context.Context, request issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	if err := storageissueops.ValidateCloseBatchRequest(request); err != nil {
		return issueops.CloseBatchResult{}, err
	}
	if request.ClaimNext != nil {
		if _, err := workapi.BuildReadyFilter(*request.ClaimNext); err != nil {
			return issueops.CloseBatchResult{}, err
		}
	}
	var policy storage.BatchClosePolicy
	switch {
	case request.ClaimNext != nil:
		// The claim-after-close picks among every ready issue, so it needs the
		// workspace-wide blocker set.
		state, err := c.policy.loadBlockingState(ctx)
		if err != nil {
			return issueops.CloseBatchResult{}, err
		}
		policy = storage.NewBatchClosePolicy(state.refsByIssue)
	case !request.Force:
		// Only a claim reads blockers beyond the batch. Scoping the rest
		// keeps an unrelated blocked issue from demanding policy support
		// of a backend that lacks it — and on a remote store reads only the
		// batch's own edges rather than every edge in the workspace.
		ids := make([]string, 0, len(request.Items))
		for _, item := range request.Items {
			ids = append(ids, item.IssueID)
		}
		refs, err := c.policy.externalBlockersFor(ctx, ids)
		if err != nil {
			return issueops.CloseBatchResult{}, err
		}
		blockers := make(map[string][]string, len(request.Items))
		for _, item := range request.Items {
			blockers[item.IssueID] = refs[item.IssueID]
		}
		policy = storage.NewBatchClosePolicy(blockers)
	}
	inner, err := storage.BatchCloserWithPolicy(c.policy.inner, policy)
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}
	return inner.CloseBatch(ctx, request)
}

var _ issueops.BatchCloser = (*batchCloser)(nil)
