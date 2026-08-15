package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// BatchCreatorSource is the capability accessor a unit-of-work provider offers
// for the batch-create role.
type BatchCreatorSource interface {
	BatchCreator() (publicops.BatchCreator, error)
}

// batchCreator creates many issues through one unit of work.
type batchCreator struct {
	provider UnitOfWorkProvider
}

// BatchCreator returns the guarded create-many surface for this provider.
func (p *doltSQLProvider) BatchCreator() (publicops.BatchCreator, error) {
	return NewBatchCreator(p)
}

// NewBatchCreator constructs a public batch creator backed by provider.
func NewBatchCreator(provider UnitOfWorkProvider) (publicops.BatchCreator, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new batch creator: unit-of-work provider must not be nil")
	}
	return &batchCreator{provider: provider}, nil
}

var _ publicops.BatchCreator = (*batchCreator)(nil)

// CreateBatch creates every item in ONE unit of work and commits them together:
// N items are N creates and one commit.
//
// IT IS ALL OR NOTHING, unlike the batch CLOSE beside it. The first item that
// refuses returns, which rolls the attempt back with nothing created.
//
// THE ITEMS ARE CREATED IN ORDER, and that is load-bearing: this body writes
// each item's edges as it writes that item, so an edge naming an EARLIER item
// of the same batch resolves and one naming a later item does not.
func (o *batchCreator) CreateBatch(ctx context.Context, request publicops.CreateBatchRequest) (publicops.CreateBatchResult, error) {
	snapshot := storageissueops.CloneCreateBatchRequest(request)
	if err := storageissueops.ValidateCreateBatchRequest(snapshot); err != nil {
		return publicops.CreateBatchResult{}, err
	}

	return RunTxResult(ctx, o.provider, func(ctx context.Context, uw UnitOfWork) (publicops.CreateBatchResult, string, error) {
		attempt := storageissueops.CloneCreateBatchRequest(snapshot)
		createContext, err := uw.ConfigUseCase().LoadCreateContext(ctx)
		if err != nil {
			return publicops.CreateBatchResult{}, "", err
		}
		// The plane rule, enforced BEFORE anything is written. This body
		// creates item by item, so by the time a cross-plane edge is written
		// its target is an ordinary existing row and the domain layer accepts
		// it — the store bodies see the batch as a set and refuse. Same
		// request, opposite outcomes, on a rule the contract states.
		if err := storageissueops.ValidateCreateBatchPlanes(attempt, createContext.InfraTypes); err != nil {
			return publicops.CreateBatchResult{}, "", err
		}

		result := publicops.CreateBatchResult{Issues: make([]*types.Issue, len(attempt.Items))}
		for i, item := range attempt.Items {
			issue, err := createBatchItem(ctx, uw, attempt, item, createContext)
			if err != nil {
				return publicops.CreateBatchResult{}, "", storageissueops.CreateBatchItemError(i, err)
			}
			result.Issues[i] = issue
		}
		return result, storageissueops.CreateBatchCommitMessage(attempt, result), nil
	})
}

// createBatchItem creates one item on the batch's unit of work, through the
// same preparation, infra-type routing and error classification the single
// create runs — so an item's content rules ARE Lifecycle.Create's.
func createBatchItem(ctx context.Context, uw UnitOfWork, request publicops.CreateBatchRequest, item publicops.BatchCreateItem, createContext domain.CreateContext) (*types.Issue, error) {
	prepared, err := storageissueops.PreparePublicCreateRequest(
		storageissueops.CreateBatchItemRequest(request, item),
		storageissueops.PublicCreateContext{
			IssuePrefix:     createContext.IssuePrefix,
			AllowedPrefixes: createContext.AllowedPrefixes,
			CustomStatuses:  types.CustomStatusNames(createContext.CustomStatuses),
			CustomTypes:     createContext.CustomTypes,
		})
	if err != nil {
		return nil, err
	}
	// Configured infra types live in the wisp tables, the same routing
	// ExecuteCreateBatch and the single create apply.
	if !prepared.Issue.Ephemeral && !prepared.Issue.NoHistory && createContext.InfraTypes[string(prepared.Issue.IssueType)] {
		prepared.Issue.Ephemeral = true
	}
	params, useWisp, err := createParams(prepared)
	if err != nil {
		return nil, validationError(err)
	}
	var created domain.CreateIssueResult
	if useWisp {
		created, err = uw.IssueUseCase().CreateWisp(ctx, params, request.Actor)
	} else {
		created, err = uw.IssueUseCase().CreateIssue(ctx, params, request.Actor)
	}
	if err != nil {
		return nil, storageissueops.ClassifyPublicCreateError(err)
	}
	return hydrateIssueOperation(ctx, uw, created.Issue, false, false)
}
