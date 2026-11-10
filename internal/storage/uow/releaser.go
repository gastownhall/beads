package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// ReleaserSource is the capability accessor a unit-of-work provider offers for
// the claim-release role, the sibling of MetadataCASSource and CounterSource.
type ReleaserSource interface {
	Releaser() (publicops.Releaser, error)
}

// releaser gives up a claim through a unit of work.
type releaser struct {
	provider UnitOfWorkProvider
}

// Releaser returns the claim-release surface for this provider.
func (p *doltSQLProvider) Releaser() (publicops.Releaser, error) {
	return NewReleaser(p)
}

// NewReleaser constructs a public release surface backed by provider.
func NewReleaser(provider UnitOfWorkProvider) (publicops.Releaser, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new releaser: unit-of-work provider must not be nil")
	}
	return &releaser{provider: provider}, nil
}

var _ publicops.Releaser = (*releaser)(nil)

// Release gives up the claim on one issue inside ONE unit of work.
//
// THIS LEG IS NOT AN INDEPENDENT BODY, and the contract says so: it reaches
// issueops.ReleaseIssueInTx through the domain issue repository, which is the
// same function the two store backends wrap. What it can still get wrong, and
// what the conformance cases are written to catch, is the WRAPPER: a dropped
// request field, a lost transaction, a refusal that stops matching errors.Is.
//
// IT COMPOSES A MESSAGE FOR EVERY RELEASE THAT WROTE A ROW, including an
// EPHEMERAL one that versions nothing, and that is the one place this leg
// differs from the two stores rather than a copy of them. RunTxResult's empty
// message means "nothing to version" AND "nothing to commit", so gating it on
// the durable table set rolls an ephemeral release back and the wisp comes out
// still claimed. Measured, not reasoned about: the wisp case went green on both
// stores and red here until this line read the row fact instead. The
// version-control layer below already demotes a commit with nothing pending to
// a plain SQL COMMIT, so the ephemeral release still records no history entry.
// Wrote and Tables are two facts for exactly this reason.
//
// THE RETRY IS LOAD-BEARING. RunTxResult re-runs the whole body on a
// serialization failure, so a concurrent claimant that commits mid-flight makes
// this attempt collide on the row_lock rewrite and be replayed against the
// winner's committed row. That re-read is what makes a conditional release lose
// honestly rather than clobber a claim that moved.
func (r *releaser) Release(ctx context.Context, req publicops.ReleaseRequest) (publicops.ReleaseResult, error) {
	if err := workapi.ValidateReleaseRequest(req); err != nil {
		return publicops.ReleaseResult{}, err
	}
	return RunTxResult(ctx, r.provider, func(ctx context.Context, uw UnitOfWork) (publicops.ReleaseResult, string, error) {
		result, wrote, err := uw.IssueUseCase().ReleaseIssue(ctx, req)
		if err != nil || !wrote {
			return result, "", err
		}
		return result, fmt.Sprintf("bd: unclaim %s", req.IssueID), nil
	})
}
