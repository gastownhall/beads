package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// DeleterSource is the capability accessor a unit-of-work provider offers for
// the named-row erasure role, the sibling of SweeperSource and CounterSource.
type DeleterSource interface {
	Deleter() (publicops.Deleter, error)
}

// deleter erases named rows through a unit of work.
type deleter struct {
	provider UnitOfWorkProvider
}

// Deleter returns the named-row erasure surface for this provider.
func (p *doltSQLProvider) Deleter() (publicops.Deleter, error) {
	return NewDeleter(p)
}

// NewDeleter constructs a public deleter backed by provider.
func NewDeleter(provider UnitOfWorkProvider) (publicops.Deleter, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new deleter: unit-of-work provider must not be nil")
	}
	return &deleter{provider: provider}, nil
}

var _ publicops.Deleter = (*deleter)(nil)

// Delete erases the request's ids inside ONE unit of work.
//
// This is the genuinely separate body: the two store backends share
// issueops.DeleteInTx, and this one reaches the same questions through the
// domain use cases. What it must NOT do differently is which rows go and which
// requests are refused — the id normalization and the request rules run through
// the same internal/workapi functions, and the conformance contract asserts the
// two equal.
//
// THE GUARD IS HERE RATHER THAN IN THE USE CASE, which selects the right SET
// for both modes but has never refused anything. The refusal belongs above it
// with the rest of the role's policy, where a future use-case caller cannot
// inherit the capability and miss it.
//
// A DRY RUN TAKES A READ-ONLY UNIT OF WORK: it writes nothing, so it must not
// take the committing path and leave a history entry describing a preview.
func (d *deleter) Delete(ctx context.Context, req publicops.DeleteRequest) (publicops.DeleteResult, error) {
	if err := workapi.ValidateDeleteRequest(req); err != nil {
		return publicops.DeleteResult{}, err
	}
	req.IDs = workapi.NormalizeDeleteIDs(req.IDs)

	if req.DryRun {
		return RunTxRead(ctx, d.provider, func(ctx context.Context, uw UnitOfWork) (publicops.DeleteResult, error) {
			return deleteInUOW(ctx, uw, req)
		})
	}
	return RunTxResult(ctx, d.provider, func(ctx context.Context, uw UnitOfWork) (publicops.DeleteResult, string, error) {
		result, err := deleteInUOW(ctx, uw, req)
		if err != nil || result.Deleted == 0 {
			// A deletion that removed nothing labels nothing: the role
			// promises at most one history entry per call and none for a
			// no-op.
			return result, "", err
		}
		return result, fmt.Sprintf("bd: delete %d issue(s)", result.Deleted), nil
	})
}

// deleteInUOW is the whole deletion on one unit of work, shared by the preview
// path and the committing one so the two cannot answer differently:
// issueops.Deleter promises that a dry run refuses exactly where the real run
// refuses.
func deleteInUOW(ctx context.Context, uw UnitOfWork, req publicops.DeleteRequest) (publicops.DeleteResult, error) {
	issueUC := uw.IssueUseCase()
	result := publicops.DeleteResult{DryRun: req.DryRun}

	// The existence probe comes FIRST, so a request naming a typo reports the
	// typo rather than whatever the graph says about the ids that resolved. It
	// keeps the ROWS rather than a set of ids, because the version precondition
	// below needs their RowVersion and re-reading them for it would be a second
	// read of the same rows in the same transaction.
	present := make(map[string]*types.Issue, len(req.IDs))
	for _, load := range []func(context.Context, []string) ([]*types.Issue, error){
		issueUC.GetIssuesByIDs,
		issueUC.GetWispsByIDs,
	} {
		rows, err := load(ctx, req.IDs)
		if err != nil {
			return publicops.DeleteResult{}, fmt.Errorf("delete: resolve ids: %w", err)
		}
		for _, row := range rows {
			if row != nil {
				present[row.ID] = row
			}
		}
	}
	var missing []string
	for _, id := range req.IDs {
		if present[id] == nil {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return publicops.DeleteResult{}, &publicops.NotFoundError{IDs: missing}
	}

	// The version precondition, between the existence probe and the dependents
	// guard exactly as issueops.Deleter.Delete orders them.
	//
	// This leg compares the row the probe already loaded rather than issuing
	// its own guard read, which is what updatePreconditionsHold does for the
	// same token on the update path. The row was read inside this unit of work,
	// so the comparison and the deletion still see one snapshot; the sentinel
	// and the message are the shared ones, because a caller matching
	// ErrVersionMismatch must not have to know which backend answered.
	//
	// req.IDs[0] is the only distinct id: ValidateDeleteRequest refused a
	// multi-id request carrying a version and NormalizeDeleteIDs collapsed the
	// duplicates before either ran.
	if req.ExpectedVersion != nil {
		if current := present[req.IDs[0]].RowVersion; current != *req.ExpectedVersion {
			return publicops.DeleteResult{}, fmt.Errorf("%w: expected %d, got %d",
				publicops.ErrVersionMismatch, *req.ExpectedVersion, current)
		}
	}

	// The guard runs only when the request did not already say what to do
	// about dependents. Under Cascade there is nothing outside the set by
	// construction.
	if !req.Cascade {
		idSet := make(map[string]bool, len(req.IDs))
		for _, id := range req.IDs {
			idSet[id] = true
		}
		external, err := externalDependentsBySourceInUOW(ctx, uw, req.IDs, idSet)
		if err != nil {
			return publicops.DeleteResult{}, err
		}
		if !req.Force {
			// Request order, so the id a caller is told about is stable
			// across runs and across backends.
			for _, id := range req.IDs {
				if deps := external[id]; len(deps) > 0 {
					return publicops.DeleteResult{}, &publicops.DependentsOutsideRequestError{
						IssueID:    id,
						Dependents: deps,
					}
				}
			}
		} else {
			orphaned := make(map[string]bool)
			for _, deps := range external {
				for _, id := range deps {
					orphaned[id] = true
				}
			}
			result.Orphaned = workapi.SortedDeleteIDs(orphaned)
		}
	}

	deleted, err := issueUC.DeleteIssues(ctx, domain.DeleteIssuesParams{
		IDs:     req.IDs,
		Cascade: req.Cascade,
		DryRun:  req.DryRun,
		// A preview rewrites nothing, so it must not ask for the rewrite.
		UpdateTextReferences: !req.DryRun,
	}, req.Actor)
	if err != nil {
		return publicops.DeleteResult{}, err
	}
	result.Deleted = deleted.DeletedCount
	result.Dependencies = deleted.DependenciesCount
	result.Labels = deleted.LabelsCount
	result.Events = deleted.EventsCount
	result.ReferencesUpdated = deleted.ReferencesUpdated
	return result, nil
}

// externalDependentsBySourceInUOW reports, for each of ids, the DIRECT
// dependents idSet does not contain — the rows a forced delete orphans and an
// unforced one refuses over.
//
// It asks both planes, the way the shared store body's
// issueops.ExternalDependentsBySourceInTx does, because an edge from a wisp
// into an issue lives in the wisp table and a guard that missed it would
// silently orphan the wisp.
func externalDependentsBySourceInUOW(
	ctx context.Context, uw UnitOfWork, ids []string, idSet map[string]bool,
) (map[string][]string, error) {
	depUC := uw.DependencyUseCase()
	bySource := make(map[string]map[string]bool)

	for _, plane := range []struct {
		list     func(context.Context, []string, domain.DepListFilter) (domain.DepBulkResult, error)
		optional bool
	}{
		{list: depUC.ListByIssueIDs},
		// The wisp plane is optional the way every other cross-plane read here
		// treats it: a workspace whose schema predates it has no table, and
		// that is not a failed guard.
		{list: depUC.ListByWispIDs, optional: true},
	} {
		res, err := plane.list(ctx, ids, domain.DepListFilter{Direction: domain.DepDirectionIn})
		if err != nil {
			if plane.optional && dberrors.IsTableNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("delete: check dependents: %w", err)
		}
		for target, edges := range res.Incoming {
			for _, edge := range edges {
				if edge == nil || idSet[edge.IssueID] {
					continue
				}
				if bySource[target] == nil {
					bySource[target] = make(map[string]bool)
				}
				bySource[target][edge.IssueID] = true
			}
		}
	}

	out := make(map[string][]string, len(bySource))
	for target, dependents := range bySource {
		out[target] = workapi.SortedDeleteIDs(dependents)
	}
	return out, nil
}
