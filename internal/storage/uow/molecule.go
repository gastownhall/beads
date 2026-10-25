package uow

import (
	"context"
	"fmt"
	"strconv"

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
// this backend: CloseCompletedMoleculesInUOW for one step.
func CloseCompletedMoleculeInUOW(ctx context.Context, uw UnitOfWork, stepID, actor, session string) (storageissueops.MoleculeAutoClose, error) {
	autos, err := CloseCompletedMoleculesInUOW(ctx, uw, []string{stepID}, actor, session)
	if err != nil {
		return storageissueops.MoleculeAutoClose{}, err
	}
	return autos[0], nil
}

// CloseCompletedMoleculesInUOW is CloseBatchRequest.AutoCloseMolecule's body
// for this backend, the twin of storageissueops.CloseCompletedMoleculesInTx:
// for every auto-closing molecule root a close of stepIDs could have completed
// (issueops.AutoClosingMoleculeRoots read in uw), it rewrites the root's
// coordination cell (storageissueops.MoleculeCoordinationKey, so two units of
// work deciding the same root conflict at commit and the retry reads the
// other's close; the cost, serialized sibling closes, is
// storageissueops.TouchMoleculeCoordinationInTx's), and when every step under
// the root is now closed (issueops.MoleculeStepsClosed) it closes the root in
// uw — unforced, guarded on the root revision read in the same unit of work,
// recording issueops.MoleculeAutoCloseReason and session.
//
// It answers one MoleculeAutoClose per step id, in order: each root is decided
// once and reported on the first step under it. A refusal of the root
// (issueops.IsMoleculeAutoCloseRefusal) is answered as the Refusal and leaves
// the root untouched; the caller's own closes stand. Any other failure is
// returned and fails the caller's unit of work.
func CloseCompletedMoleculesInUOW(ctx context.Context, uw UnitOfWork, stepIDs []string, actor, session string) ([]storageissueops.MoleculeAutoClose, error) {
	reader := MoleculeReaderInUOW(uw)
	roots, err := publicops.AutoClosingMoleculeRoots(ctx, reader, stepIDs)
	if err != nil {
		return nil, err
	}
	autos := make([]storageissueops.MoleculeAutoClose, len(stepIDs))
	decided := make(map[string]bool, len(roots))
	for i, stepID := range stepIDs {
		root := roots[stepID]
		if root == nil || decided[root.ID] {
			continue
		}
		decided[root.ID] = true
		auto, err := closeCompletedMoleculeRootInUOW(ctx, uw, reader, root, actor, session)
		if err != nil {
			return nil, err
		}
		autos[i] = auto
	}
	return autos, nil
}

func closeCompletedMoleculeRootInUOW(ctx context.Context, uw UnitOfWork, reader publicops.MoleculeReader, root *publicops.Issue, actor, session string) (storageissueops.MoleculeAutoClose, error) {
	// storageissueops.TouchMoleculeCoordinationInTx, through this unit of work.
	touch := strconv.FormatInt(storageissueops.FreshRowLock(), 10)
	if err := uw.ConfigUseCase().SetLocalMetadata(ctx, storageissueops.MoleculeCoordinationKey(root.ID), touch); err != nil {
		return storageissueops.MoleculeAutoClose{}, fmt.Errorf("touch molecule coordination for %s: %w", root.ID, err)
	}
	complete, err := publicops.MoleculeStepsClosed(ctx, reader, root.ID)
	if err != nil || !complete {
		return storageissueops.MoleculeAutoClose{}, err
	}
	req := publicops.MoleculeRootClose(root, actor, session)
	// A probe, not a write: an UpdateSpec with no Fields applies nothing. It
	// only checks the root's revision against req.ExpectedVersion, the guard
	// the close below does not take itself.
	if _, err := uw.IssueUseCase().ApplyUpdate(ctx, req.IssueID, domain.UpdateSpec{ExpectedVersion: req.ExpectedVersion}, req.Actor); err != nil {
		if publicops.IsMoleculeAutoCloseRefusal(err) {
			return storageissueops.RefusedMoleculeAutoClose(err), nil
		}
		return storageissueops.MoleculeAutoClose{}, fmt.Errorf("auto-closing molecule %s: %w", root.ID, err)
	}
	params := domain.CloseIssueParams{Reason: req.Reason, Session: req.Session}
	var closed domain.CloseIssueResult
	if storageissueops.IsWisp(root) {
		closed, err = uw.IssueUseCase().CloseWispChecked(ctx, req.IssueID, params, req.Actor, req.Force)
	} else {
		closed, err = uw.IssueUseCase().CloseIssueChecked(ctx, req.IssueID, params, req.Actor, req.Force)
	}
	if publicops.IsMoleculeAutoCloseRefusal(err) {
		return storageissueops.RefusedMoleculeAutoClose(err), nil
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
