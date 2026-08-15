package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

// MetadataCASSource is the capability accessor a unit-of-work provider offers
// for the conditional metadata write, the sibling of CommenterSource and
// DependencyEditorSource.
type MetadataCASSource interface {
	MetadataCAS() (publicops.MetadataCAS, error)
}

// metadataCAS swaps one metadata key through a unit of work.
type metadataCAS struct {
	provider UnitOfWorkProvider
}

// MetadataCAS returns the conditional metadata write for this provider.
func (p *doltSQLProvider) MetadataCAS() (publicops.MetadataCAS, error) {
	return NewMetadataCAS(p)
}

// NewMetadataCAS constructs a public compare-and-set surface backed by provider.
func NewMetadataCAS(provider UnitOfWorkProvider) (publicops.MetadataCAS, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new metadata cas: unit-of-work provider must not be nil")
	}
	return &metadataCAS{provider: provider}, nil
}

var _ publicops.MetadataCAS = (*metadataCAS)(nil)

// CompareAndSetKey applies the request's transition inside ONE unit of work.
//
// THIS LEG IS NOT AN INDEPENDENT BODY, and the contract says so: it reaches
// issueops.CompareAndSetMetadataKeyInTx through the domain issue repository,
// which is the same function the two store backends wrap. What it can still
// get wrong, and what the conformance cases are written to catch, is the
// WRAPPER: a dropped request field, a lost transaction, a refusal that stops
// matching errors.Is.
//
// A SWAP THAT WROTE NOTHING COMPOSES NO COMMIT MESSAGE, which is RunTxResult's
// existing signal for a unit of work that has nothing to version — a lost race
// and a swap over an already-equal value both take it.
//
// THE RETRY IS LOAD-BEARING. RunTxResult re-runs the whole body on a
// serialization failure, so a concurrent writer that commits mid-flight makes
// this attempt collide on the row_lock rewrite and be replayed against the
// winner's committed row. That re-read is what makes a lost race report the
// value that actually beat it.
func (m *metadataCAS) CompareAndSetKey(ctx context.Context, req publicops.CompareAndSetKeyRequest) (publicops.CompareAndSetKeyResult, error) {
	plan, err := storage.PlanCompareAndSetKey(req)
	if err != nil {
		return publicops.CompareAndSetKeyResult{}, err
	}
	return RunTxResult(ctx, m.provider, func(ctx context.Context, uw UnitOfWork) (publicops.CompareAndSetKeyResult, string, error) {
		result, wrote, err := uw.IssueUseCase().CompareAndSetMetadataKey(ctx, plan)
		if err != nil || !wrote {
			return result, "", err
		}
		return result, fmt.Sprintf("bd: compare-and-set metadata %s.%s", plan.IssueID, plan.Key), nil
	})
}
