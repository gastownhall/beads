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
// A per-item refusal — storageissueops.CloseRefusalStaysPerItem's vocabulary —
// lands in that item's outcome and the loop CONTINUES. The survivors still
// commit, because that is what closing a list of issues has always done here
// and the per-id report is what tells the caller which one to fix. Any OTHER
// failure inside an item is infrastructure, exactly like a request-level one:
// it returns an error, which rolls the whole attempt back — items already
// closed inside it included — so no partial prefix commits and no
// infrastructure failure is misreported as one item's refusal
// (issueops/batchcloser.go:119-126).
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
			outcome, err := closeBatchItem(ctx, uw, request, item)
			if err != nil {
				return publicops.CloseBatchResult{}, "", err
			}
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
//
// The non-nil error return is the item's INFRASTRUCTURE failure — anything
// outside CloseRefusalStaysPerItem's refusal vocabulary, hydration failures
// always included — and the caller aborts the whole attempt on it. Splitting
// the two returns is what keeps a driver error, a canceled context or a
// post-write hydration failure from riding a survivor's commit as if it were
// one id's refusal.
func closeBatchItem(ctx context.Context, uw UnitOfWork, request publicops.CloseBatchRequest, item publicops.BatchCloseItem) (publicops.CloseOutcome, error) {
	current, isWisp, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), item.IssueID)
	if err != nil {
		return closeBatchRefusalOrFailure(item.IssueID, err)
	}

	params := domain.CloseIssueParams{Reason: item.Reason, Session: request.Session}
	var closed domain.CloseIssueResult
	// The close verbs announce every success, re-close included, because a
	// SINGLE close must. A batch item must not: a teardown replayed against an
	// already-closed convoy would run the workspace's on_close script once per
	// item on every pass (ga-2yaqp.1). The mark is taken here rather than at the
	// top of the function so it covers the close and nothing else.
	mark := markBatchNotifications(uw)
	if isWisp {
		closed, err = uw.IssueUseCase().CloseWispChecked(ctx, item.IssueID, params, request.Actor, request.Force)
	} else {
		closed, err = uw.IssueUseCase().CloseIssueChecked(ctx, item.IssueID, params, request.Actor, request.Force)
	}
	if err != nil {
		return closeBatchRefusalOrFailure(item.IssueID, err)
	}
	if !closed.Closed {
		rewindBatchNotifications(uw, mark)
	}
	if closed.Issue != nil {
		current = closed.Issue
	}
	// Hydration runs after the write, so ANY failure here — the induced seam
	// error included — is infrastructure by construction: the row is closed in
	// the transaction, and an error now says nothing about the item the caller
	// could act on. It is never classified back into the refusal vocabulary.
	hydrated, err := hydrateIssueOperation(ctx, uw, current, false, false)
	if err == nil {
		err = storageissueops.InducedBatchCloseHydrationFailure(item.IssueID)
	}
	if err != nil {
		// Marked post-write so outward classification (httpapi's problem
		// mapper, direct callers) treats a hydration miss as infrastructure
		// even when it wraps ErrNotFound - same rule as the issueops path.
		return publicops.CloseOutcome{}, publicops.MarkPostWrite(fmt.Errorf("close batch item %s: %w", item.IssueID, err))
	}
	return publicops.CloseOutcome{
		IssueID:      item.IssueID,
		Issue:        hydrated,
		Changed:      closed.Closed,
		OpenChildren: closed.OpenChildren,
	}, nil
}

// closeBatchRefusalOrFailure applies the shared taxonomy to one item's resolve
// or close error: a typed refusal is that item's outcome and the batch goes
// on; anything else fails the request (issueops/batchcloser.go:119-126).
func closeBatchRefusalOrFailure(issueID string, err error) (publicops.CloseOutcome, error) {
	if storageissueops.CloseRefusalStaysPerItem(err) {
		return publicops.CloseOutcome{IssueID: issueID, Err: err}, nil
	}
	return publicops.CloseOutcome{}, fmt.Errorf("close batch item %s: %w", issueID, err)
}
