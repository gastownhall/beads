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
	if !request.Force || request.ClaimNext != nil {
		state, err := c.policy.loadBlockingState(ctx)
		if err != nil {
			return issueops.CloseBatchResult{}, err
		}
		blockers := state.refsByIssue
		if request.ClaimNext == nil {
			// Only a claim reads blockers beyond the batch. Scoping the rest
			// keeps an unrelated blocked issue from demanding policy support
			// of a backend that lacks it.
			blockers = make(map[string][]string, len(request.Items))
			for _, item := range request.Items {
				blockers[item.IssueID] = state.refsByIssue[item.IssueID]
			}
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
