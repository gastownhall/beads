package uow

import (
	"context"
	"fmt"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	publicops "github.com/steveyegge/beads/issueops"
)

// BlockedListerSource is the capability accessor a unit-of-work provider
// offers for the blocked-listing role.
type BlockedListerSource interface {
	BlockedLister() (publicops.BlockedLister, error)
}

// blockedLister lists the blocked issues through a unit of work.
type blockedLister struct {
	provider UnitOfWorkProvider
}

// BlockedLister returns the blocked-listing surface for this provider.
func (p *doltSQLProvider) BlockedLister() (publicops.BlockedLister, error) {
	return NewBlockedLister(p)
}

// NewBlockedLister constructs a public BlockedLister backed by provider.
func NewBlockedLister(provider UnitOfWorkProvider) (publicops.BlockedLister, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new blocked lister: unit-of-work provider must not be nil")
	}
	return &blockedLister{provider: provider}, nil
}

var _ publicops.BlockedLister = (*blockedLister)(nil)

// ListBlocked answers one blocked listing inside one read-only unit of work,
// through the use case's GetBlockedIssues: the same body the store legs run
// (issueops.GetBlockedIssuesInTx), and, under a provider that applies the
// external-dependency policy, that provider's decoration of it.
func (l *blockedLister) ListBlocked(ctx context.Context, req publicops.BlockedRequest) (publicops.BlockedResult, error) {
	filter := storageissueops.BlockedFilter(req)
	return RunTxRead(ctx, l.provider, func(ctx context.Context, uw UnitOfWork) (publicops.BlockedResult, error) {
		items, err := uw.IssueUseCase().GetBlockedIssues(ctx, filter)
		if err != nil {
			return publicops.BlockedResult{}, err
		}
		return storageissueops.BlockedResultOf(items), nil
	})
}
