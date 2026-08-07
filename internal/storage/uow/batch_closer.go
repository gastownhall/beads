package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// BatchCloserSource is the capability accessor a unit-of-work provider offers
// for the batch-close role, the sibling of IssueLifecycleSource and
// ReadyClaimerSource.
type BatchCloserSource interface {
	BatchCloser() (publicops.BatchCloser, error)
}

// batchCloser closes many issues through one unit of work.
type batchCloser struct {
	provider UnitOfWorkProvider
}

// BatchCloser returns the guarded close-many surface for this provider.
func (p *doltSQLProvider) BatchCloser() (publicops.BatchCloser, error) {
	return NewBatchCloser(p)
}

// NewBatchCloser constructs a public batch closer backed by provider.
func NewBatchCloser(provider UnitOfWorkProvider) (publicops.BatchCloser, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new batch closer: unit-of-work provider must not be nil")
	}
	return &batchCloser{provider: provider}, nil
}

var _ publicops.BatchCloser = (*batchCloser)(nil)

// CloseBatch closes every item it can in ONE unit of work and commits them
// together. The request is the transaction: N ids are N closes and one commit,
// which is the whole reason this is not a loop over Lifecycle.Close.
//
// A per-item refusal lands in that item's outcome and the loop CONTINUES. The
// survivors still commit, because that is what closing a list of issues has
// always done here and the per-id report is what tells the caller which one to
// fix. Only a request-level failure returns an error, and that rolls the whole
// transaction back.
func (o *batchCloser) CloseBatch(ctx context.Context, request publicops.CloseBatchRequest) (publicops.CloseBatchResult, error) {
	if err := storageissueops.ValidateCloseBatchRequest(request); err != nil {
		return publicops.CloseBatchResult{}, err
	}
	var claimFilter *types.WorkFilter
	if request.ClaimNext != nil {
		filter, err := workapi.BuildReadyFilter(*request.ClaimNext)
		if err != nil {
			return publicops.CloseBatchResult{}, err
		}
		claimFilter = &filter
	}

	return RunTxResult(ctx, o.provider, func(ctx context.Context, uw UnitOfWork) (publicops.CloseBatchResult, string, error) {
		result := publicops.CloseBatchResult{Outcomes: make([]publicops.CloseOutcome, len(request.Items))}
		landed := 0
		for i, item := range request.Items {
			outcome := closeBatchItem(ctx, uw, request, item)
			result.Outcomes[i] = outcome
			// Changed, not "no error": an idempotent re-close persisted
			// nothing, so a batch of them lands nothing and earns no claim.
			if outcome.Err == nil && outcome.Changed {
				landed++
			}
		}

		if claimFilter != nil && landed > 0 {
			claimed, err := ClaimNextInUOW(ctx, uw, request.Actor, *claimFilter)
			if err != nil {
				return publicops.CloseBatchResult{}, "", err
			}
			result.ClaimedNext = claimed
		}

		return result, storageissueops.CloseBatchCommitMessage(result), nil
	})
}

// closeBatchItem closes one item, reporting its refusal rather than raising
// it. The resolve and the close share the batch's transaction, so the
// is_blocked guard the checked close runs has no read-then-write window.
func closeBatchItem(ctx context.Context, uw UnitOfWork, request publicops.CloseBatchRequest, item publicops.BatchCloseItem) publicops.CloseOutcome {
	current, isWisp, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), item.IssueID)
	if err != nil {
		return publicops.CloseOutcome{IssueID: item.IssueID, Err: err}
	}

	params := domain.CloseIssueParams{Reason: item.Reason, Session: request.Session}
	var closed domain.CloseIssueResult
	if isWisp {
		closed, err = uw.IssueUseCase().CloseWispChecked(ctx, item.IssueID, params, request.Actor, request.Force)
	} else {
		closed, err = uw.IssueUseCase().CloseIssueChecked(ctx, item.IssueID, params, request.Actor, request.Force)
	}
	if err != nil {
		return publicops.CloseOutcome{IssueID: item.IssueID, Err: err}
	}
	if closed.Issue != nil {
		current = closed.Issue
	}
	hydrated, err := hydrateIssueOperation(ctx, uw, current, false, false)
	if err != nil {
		return publicops.CloseOutcome{IssueID: item.IssueID, Err: err}
	}
	return publicops.CloseOutcome{
		IssueID:      item.IssueID,
		Issue:        hydrated,
		Changed:      closed.Closed,
		OpenChildren: closed.OpenChildren,
	}
}
