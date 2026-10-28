package uow

import (
	"context"
	"fmt"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	publicops "github.com/steveyegge/beads/issueops"
)

// HeartbeaterSource is the capability accessor a unit-of-work provider offers
// for the lease-refresh role, the sibling of LeaseReclaimerSource.
type HeartbeaterSource interface {
	Heartbeater() (publicops.Heartbeater, error)
}

// heartbeater refreshes a held lease through a unit of work.
type heartbeater struct {
	provider UnitOfWorkProvider
}

// Heartbeater returns the lease-refresh surface for this provider.
func (p *doltSQLProvider) Heartbeater() (publicops.Heartbeater, error) {
	return NewHeartbeater(p)
}

// NewHeartbeater constructs a public Heartbeater backed by provider.
func NewHeartbeater(provider UnitOfWorkProvider) (publicops.Heartbeater, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new heartbeater: unit-of-work provider must not be nil")
	}
	return &heartbeater{provider: provider}, nil
}

var _ publicops.Heartbeater = (*heartbeater)(nil)

// Heartbeat reaches domain.IssueUseCase.RenewLease inside ONE unit of work. A
// refresh touches only the dolt_ignored leases table, so it mints no Dolt
// commit and records nothing in dolt_log; a re-arm is recorded as a mutation
// and commits like one. A serialization loser (a heartbeat racing a reclaim,
// release or close) is replayed against a fresh unit of work.
//
// VALIDATION HAPPENS BEFORE THE UNIT OF WORK OPENS, as on the dolt and
// embedded-dolt legs: a refusal then costs no connection. The shared body
// validates again inside the transaction.
func (h *heartbeater) Heartbeat(ctx context.Context, request publicops.HeartbeatRequest) (publicops.HeartbeatResult, error) {
	if err := storageissueops.ValidateHeartbeatRequest(request); err != nil {
		return publicops.HeartbeatResult{}, err
	}
	// A lease refusal commits (it may have deleted an orphaned lease row) and
	// is reported after the commit; see HeartbeatRefusalCommits.
	//
	// THE COMMIT IS SCOPED (RunTxScoped). A refresh or a refusal writes only
	// the dolt_ignored leases table and commits with the plain SQL COMMIT: no
	// working-set scan, no Dolt commit, and so nothing another writer left
	// uncommitted (a batch-mode edit) is swept into a heartbeat commit. A
	// re-arm is recorded as a mutation and commits exactly the tables
	// LeaseRearmVersionCommit names, never DOLT_COMMIT('-Am'), in a Dolt
	// commit made after the SQL COMMIT (ScopedCommitter): built inside the
	// transaction, it would write an issues row a concurrent writer changed
	// meanwhile back to its value at this heartbeat's start.
	type outcome struct {
		result  publicops.HeartbeatResult
		refusal error
	}
	out, err := RunTxScoped(ctx, h.provider, func(ctx context.Context, uw UnitOfWork) (outcome, []string, string, error) {
		result, err := uw.IssueUseCase().RenewLease(ctx, request)
		if storageissueops.HeartbeatRefusalCommits(err) {
			return outcome{refusal: err}, nil, "", nil
		}
		if err != nil {
			return outcome{}, nil, "", err
		}
		tables, msg := storageissueops.LeaseRearmVersionCommit(request.IssueID, result.Rearmed)
		return outcome{result: result}, tables.Sorted(), msg, nil
	})
	if err != nil {
		return publicops.HeartbeatResult{}, err
	}
	if out.refusal != nil {
		return publicops.HeartbeatResult{}, out.refusal
	}
	return out.result, nil
}
