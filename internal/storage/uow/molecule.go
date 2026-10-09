package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	publicops "github.com/steveyegge/beads/issueops"
)

// MoleculeReaderInUOW binds the molecule rules (issueops.MoleculeReader) to a
// caller's unit of work: the SAME BatchGetter, Relations and EdgeReader bodies
// this backend's roles run (GetManyInUOW, RelatedInUOW, ReadEdgesInUOW),
// against uw, so a close that decides whether it completed a molecule reads
// the state its own writes produced. It is this backend's twin of
// internal/storage/issueops.TxMoleculeReader.
func MoleculeReaderInUOW(uw UnitOfWork) publicops.MoleculeReader {
	return publicops.MoleculeReaderOver(uowMoleculeReader{uw: uw})
}

type uowMoleculeReader struct{ uw UnitOfWork }

func (r uowMoleculeReader) GetMany(ctx context.Context, request publicops.GetManyRequest) (publicops.GetManyResult, error) {
	return GetManyInUOW(ctx, r.uw, request)
}

func (r uowMoleculeReader) Related(ctx context.Context, request publicops.RelatedRequest) ([]*publicops.RelatedIssue, error) {
	if err := storageissueops.ValidateRelatedRequest(request); err != nil {
		return nil, err
	}
	return RelatedInUOW(ctx, r.uw, request)
}

func (r uowMoleculeReader) ReadEdges(ctx context.Context, request publicops.EdgeReadRequest) (publicops.EdgeReadResult, error) {
	return ReadEdgesInUOW(ctx, r.uw, request)
}

// CloseCompletedMoleculeInUOW is CloseRequest.AutoCloseMolecule's body for
// this backend, the twin of storageissueops.CloseCompletedMoleculeInTx: when
// closing stepID completed an auto-closing molecule (issueops.CompletedMolecule
// read in uw), it closes the root in uw — unforced, guarded on the root
// revision read in the same unit of work, recording
// issueops.MoleculeAutoCloseReason and session.
//
// A close-policy refusal of the root is answered as the Refusal and leaves the
// root untouched; the caller's own close stands. Any other failure is
// returned and fails the caller's unit of work.
func CloseCompletedMoleculeInUOW(ctx context.Context, uw UnitOfWork, stepID, actor, session string) (storageissueops.MoleculeAutoClose, error) {
	root, err := publicops.CompletedMolecule(ctx, MoleculeReaderInUOW(uw), stepID)
	if err != nil || root == nil {
		return storageissueops.MoleculeAutoClose{}, err
	}
	version := root.RowVersion
	if _, err := uw.IssueUseCase().ApplyUpdate(ctx, root.ID, domain.UpdateSpec{ExpectedVersion: &version}, actor); err != nil {
		if publicops.IsMoleculeAutoCloseRefusal(err) {
			return storageissueops.MoleculeAutoClose{Refusal: err.Error()}, nil
		}
		return storageissueops.MoleculeAutoClose{}, fmt.Errorf("auto-closing molecule %s: %w", root.ID, err)
	}
	params := domain.CloseIssueParams{Reason: publicops.MoleculeAutoCloseReason, Session: session}
	var closed domain.CloseIssueResult
	if storageissueops.IsWisp(root) {
		closed, err = uw.IssueUseCase().CloseWispChecked(ctx, root.ID, params, actor, false)
	} else {
		closed, err = uw.IssueUseCase().CloseIssueChecked(ctx, root.ID, params, actor, false)
	}
	if publicops.IsMoleculeAutoCloseRefusal(err) {
		return storageissueops.MoleculeAutoClose{Refusal: err.Error()}, nil
	}
	if err != nil {
		return storageissueops.MoleculeAutoClose{}, fmt.Errorf("auto-closing molecule %s: %w", root.ID, err)
	}
	if !closed.Closed {
		return storageissueops.MoleculeAutoClose{}, nil
	}
	current := root
	if closed.Issue != nil {
		current = closed.Issue
	}
	hydrated, err := hydrateIssueOperation(ctx, uw, current, false, false)
	if err != nil {
		return storageissueops.MoleculeAutoClose{}, err
	}
	return storageissueops.MoleculeAutoClose{Root: hydrated}, nil
}

// MoleculeStepperSource is the capability accessor a unit-of-work provider
// offers for the advance-a-molecule role.
type MoleculeStepperSource interface {
	MoleculeStepper() (publicops.MoleculeStepper, error)
}

// MoleculeStepper returns the advance-a-molecule surface for this provider.
func (p *doltSQLProvider) MoleculeStepper() (publicops.MoleculeStepper, error) {
	return NewMoleculeStepper(p)
}

// NewMoleculeStepper is issueops.NewMoleculeStepper composed over the roles
// THIS provider builds (its batch getter, relations, edge reader, claimer and
// lifecycle), so a wrapper that builds its roles over itself — the notifying
// provider, a policy decorator, the server's timed provider — gets an advance
// whose every read and claim goes through its own layer.
func NewMoleculeStepper(provider UnitOfWorkProvider) (publicops.MoleculeStepper, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new molecule stepper: unit-of-work provider must not be nil")
	}
	getter, err := NewBatchGetter(provider)
	if err != nil {
		return nil, err
	}
	relations, err := NewIssueRelations(provider)
	if err != nil {
		return nil, err
	}
	edges, err := NewEdgeReader(provider)
	if err != nil {
		return nil, err
	}
	claimer, err := NewIssueClaimer(provider)
	if err != nil {
		return nil, err
	}
	lifecycle, err := NewIssueOperations(provider)
	if err != nil {
		return nil, err
	}
	return publicops.NewMoleculeStepper(publicops.MoleculeStepperRoles{
		Reader:    publicops.MoleculeReader{BatchGetter: getter, Relations: relations, EdgeReader: edges},
		Claimer:   claimer,
		Lifecycle: lifecycle,
	}), nil
}
