package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// RelationsSource is the capability accessor a unit-of-work provider offers
// for the neighbor-query role, the sibling of IssueReaderSource.
type RelationsSource interface {
	IssueRelations() (publicops.Relations, error)
}

// issueRelations answers neighbor queries through a unit of work.
type issueRelations struct {
	provider UnitOfWorkProvider
}

// IssueRelations returns the guarded neighbor-query surface for this provider.
func (p *doltSQLProvider) IssueRelations() (publicops.Relations, error) {
	return NewIssueRelations(p)
}

// NewIssueRelations constructs public neighbor queries backed by provider.
func NewIssueRelations(provider UnitOfWorkProvider) (publicops.Relations, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new issue relations: unit-of-work provider must not be nil")
	}
	return &issueRelations{provider: provider}, nil
}

var _ publicops.Relations = (*issueRelations)(nil)

// Related answers one neighbor query inside one unit of work.
//
// The anchor probe is workapi.GetIssueOrWisp, the same issue-then-wisp
// fallback Reader.Get runs, so "no such issue" means the same thing on both
// roles. The type filter and the order are storageissueops.FinishRelatedPage —
// the one function the store-backed sibling also calls, for the reason
// workapi.FinishPage is shared: an ordering each implementation applies for
// itself is an ordering the two will eventually disagree about.
func (r *issueRelations) Related(ctx context.Context, request publicops.RelatedRequest) ([]*publicops.RelatedIssue, error) {
	if err := storageissueops.ValidateRelatedRequest(request); err != nil {
		return nil, err
	}
	direction := domain.DepDirectionOut
	if request.Direction == publicops.RelationIn {
		direction = domain.DepDirectionIn
	}
	return RunTxRead(ctx, r.provider, func(ctx context.Context, uw UnitOfWork) ([]*publicops.RelatedIssue, error) {
		issue, isWisp, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), request.ID)
		if err != nil {
			return nil, err
		}
		filter := domain.DepListFilter{Direction: direction}
		var items []*publicops.RelatedIssue
		if isWisp {
			items, err = uw.DependencyUseCase().ListWispWithIssueMetadata(ctx, issue.ID, filter)
		} else {
			items, err = uw.DependencyUseCase().ListWithIssueMetadata(ctx, issue.ID, filter)
		}
		if err != nil {
			return nil, err
		}
		// The filter runs HERE rather than in the use-case call above so both
		// implementations narrow and order through one function; passing
		// filter.Types down would put the narrowing in the query on one side
		// and in Go on the other.
		return storageissueops.FinishRelatedPage(items, request.Types), nil
	})
}
