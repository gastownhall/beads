package uow

import (
	"context"
	"fmt"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	publicops "github.com/steveyegge/beads/issueops"
)

// LeaseReclaimerSource is the capability accessor a unit-of-work provider
// offers for the lease-sweep role, the sibling of DeleterSource and
// ReleaserSource.
type LeaseReclaimerSource interface {
	LeaseReclaimer() (publicops.LeaseReclaimer, error)
}

// leaseReclaimer sweeps stale leases through a unit of work.
type leaseReclaimer struct {
	provider UnitOfWorkProvider
}

// LeaseReclaimer returns the guarded lease-sweep surface for this provider.
func (p *doltSQLProvider) LeaseReclaimer() (publicops.LeaseReclaimer, error) {
	return NewLeaseReclaimer(p)
}

// NewLeaseReclaimer constructs a public LeaseReclaimer backed by provider.
func NewLeaseReclaimer(provider UnitOfWorkProvider) (publicops.LeaseReclaimer, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new lease reclaimer: unit-of-work provider must not be nil")
	}
	return &leaseReclaimer{provider: provider}, nil
}

var _ publicops.LeaseReclaimer = (*leaseReclaimer)(nil)

// Reclaim reaches domain.IssueUseCase.Reclaim inside ONE committing unit of
// work, composing the same conditional commit message the dolt and
// embedded-dolt legs use: a sweep that reverts nothing leaves no history
// entry describing a no-op.
//
// VALIDATION HAPPENS BEFORE THE UNIT OF WORK OPENS, as on those two legs and
// for Releaser's reason: a refusal then changes nothing on the connection as
// well as on the rows. The shared body validates again inside the transaction,
// which is what keeps a leg that skipped this from answering a different
// contract.
func (l *leaseReclaimer) Reclaim(ctx context.Context, request publicops.ReclaimRequest) (publicops.ReclaimResult, error) {
	if err := storageissueops.ValidateReclaimRequest(request); err != nil {
		return publicops.ReclaimResult{}, err
	}
	return RunTxResult(ctx, l.provider, func(ctx context.Context, uw UnitOfWork) (publicops.ReclaimResult, string, error) {
		result, err := uw.IssueUseCase().Reclaim(ctx, request)
		if err != nil {
			return result, "", err
		}
		_, msg := storageissueops.ReclaimVersionCommit(result)
		return result, msg, nil
	})
}
