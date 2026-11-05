package main

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// moleculeRoles is where `bd mol current`, `bd mol progress`, `bd ready --mol`
// and `bd close --continue` get their answers on the route this command runs
// on. Every field is a library role; the molecule rules (membership, readiness,
// progress, the advance and its claim) are the issueops functions composed over
// them, so the direct, proxied-server and remote routes answer one way. What
// stays in cmd/bd is presentation.
type moleculeRoles struct {
	reader   issueops.Reader
	molecule issueops.MoleculeReader
	stepper  func() (issueops.MoleculeStepper, error)
}

// directMoleculeRoles binds the roles to the opened store: a local backend, or
// a registered remote one (http), each through its own accessors.
func directMoleculeRoles(s storage.DoltStorage) (moleculeRoles, error) {
	if s == nil {
		return moleculeRoles{}, errors.New("no database connection")
	}
	reader, err := s.IssueReader()
	if err != nil {
		return moleculeRoles{}, err
	}
	molecule, err := issueops.NewMoleculeReader(s)
	if err != nil {
		return moleculeRoles{}, err
	}
	return moleculeRoles{reader: reader, molecule: molecule, stepper: s.MoleculeStepper}, nil
}

// proxiedMoleculeRoles binds the roles to the proxied server's provider,
// through the provider's own accessors (where each of its layers is added).
func proxiedMoleculeRoles() (moleculeRoles, error) {
	if uowProvider == nil {
		return moleculeRoles{}, errors.New("proxied-server UOW provider not initialized")
	}
	reader, err := proxiedIssueReader()
	if err != nil {
		return moleculeRoles{}, err
	}
	src, ok := uowProvider.(interface {
		BatchGetter() (issueops.BatchGetter, error)
		IssueRelations() (issueops.Relations, error)
		EdgeReader() (issueops.EdgeReader, error)
	})
	if !ok {
		return moleculeRoles{}, fmt.Errorf("proxied-server provider %T does not offer the molecule read roles", uowProvider)
	}
	molecule, err := issueops.NewMoleculeReader(src)
	if err != nil {
		return moleculeRoles{}, err
	}
	stepper := func() (issueops.MoleculeStepper, error) {
		src, ok := uowProvider.(uow.MoleculeStepperSource)
		if !ok {
			return nil, fmt.Errorf("proxied-server provider %T does not offer the molecule advance", uowProvider)
		}
		return src.MoleculeStepper()
	}
	return moleculeRoles{reader: reader, molecule: molecule, stepper: stepper}, nil
}

// currentMoleculeRoles picks the route.
func currentMoleculeRoles(s storage.DoltStorage) (moleculeRoles, error) {
	if usesProxiedServer() {
		return proxiedMoleculeRoles()
	}
	return directMoleculeRoles(s)
}

// moleculeProgressOf presents a library MoleculeView as `bd mol current`'s
// MoleculeProgress.
func moleculeProgressOf(view *issueops.MoleculeView) *MoleculeProgress {
	root := view.Graph.Root
	progress := &MoleculeProgress{
		MoleculeID:    root.ID,
		MoleculeTitle: root.Title,
		Assignee:      root.Assignee,
		CurrentStep:   view.CurrentStep,
		NextStep:      view.NextStep,
		Completed:     view.Completed,
		Total:         view.Total,
	}
	for _, step := range view.Steps {
		progress.Steps = append(progress.Steps, &StepStatus{Issue: step.Issue, Status: step.State, IsCurrent: step.IsCurrent})
	}
	return progress
}

// viewMolecules loads and presents each molecule, sorted by id.
func viewMolecules(ctx context.Context, roles moleculeRoles, ids []string) ([]*MoleculeProgress, error) {
	out := make([]*MoleculeProgress, 0, len(ids))
	for _, id := range ids {
		view, err := issueops.ViewMolecule(ctx, roles.molecule, id)
		if err != nil {
			return nil, fmt.Errorf("loading molecule %s: %w", id, err)
		}
		out = append(out, moleculeProgressOf(view))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MoleculeID < out[j].MoleculeID })
	return out, nil
}

// continueResultOf presents a library AdvanceResult as `bd close --continue`'s
// ContinueResult. A step in no molecule presents as nil, as it always has.
func continueResultOf(result issueops.AdvanceResult) *ContinueResult {
	if result.MoleculeID == "" {
		return nil
	}
	return &ContinueResult{
		ClosedStep:   result.ClosedStep,
		NextStep:     result.NextStep,
		AutoAdvanced: result.Claimed,
		MolComplete:  result.Complete,
		MoleculeID:   result.MoleculeID,
	}
}

// advanceMolecule runs `bd close --continue` through the route's
// MoleculeStepper role.
func advanceMolecule(ctx context.Context, roles moleculeRoles, closedStepID string, autoClaim bool) (*ContinueResult, error) {
	stepper, err := roles.stepper()
	if err != nil {
		return nil, err
	}
	result, err := stepper.Advance(ctx, issueops.AdvanceRequest{Actor: currentActor(), ClosedStepID: closedStepID, AutoClaim: autoClaim})
	if err != nil {
		return nil, err
	}
	return continueResultOf(result), nil
}

// rawMoleculeReader binds the library's molecule rules to a molReader — the
// raw-method port the template commands (`mol ready --gated`, pour, bond)
// still read through, including the proxied route's in-transaction port. It
// is a data binding, like the in-transaction ones in internal/storage: it
// answers the three role reads from raw methods and decides nothing.
type rawMoleculeReader struct{ s molReader }

// moleculeReader binds the library's rules to this port.
func (r rawMoleculeReader) moleculeReader() issueops.MoleculeReader {
	return issueops.MoleculeReaderOver(r)
}

func (r rawMoleculeReader) GetMany(ctx context.Context, req issueops.GetManyRequest) (issueops.GetManyResult, error) {
	issues, err := r.s.GetIssuesByIDs(ctx, req.IDs)
	if err != nil {
		return issueops.GetManyResult{}, err
	}
	found := make(map[string]*types.Issue, len(issues))
	for _, issue := range issues {
		if issue != nil {
			found[issue.ID] = issue
		}
	}
	out := issueops.GetManyResult{Issues: []*issueops.Issue{}, Missing: []string{}}
	seen := map[string]bool{}
	for _, id := range req.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if issue := found[id]; issue != nil {
			out.Issues = append(out.Issues, issue)
		} else {
			out.Missing = append(out.Missing, id)
		}
	}
	return out, nil
}

func (r rawMoleculeReader) Related(ctx context.Context, req issueops.RelatedRequest) ([]*issueops.RelatedIssue, error) {
	var (
		items []*types.IssueWithDependencyMetadata
		err   error
	)
	if req.Direction == issueops.RelationIn {
		items, err = r.s.GetDependentsWithMetadata(ctx, req.ID)
	} else {
		return nil, fmt.Errorf("%w: raw molecule reads answer inbound relations only", issueops.ErrValidation)
	}
	if err != nil {
		return nil, err
	}
	out := make([]*issueops.RelatedIssue, 0, len(items))
	for _, item := range items {
		if item != nil && (len(req.Types) == 0 || containsDependencyType(req.Types, item.DependencyType)) {
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r rawMoleculeReader) ReadEdges(ctx context.Context, req issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	records, err := r.s.GetDependencyRecordsForIssues(ctx, req.IDs)
	if err != nil {
		return issueops.EdgeReadResult{}, err
	}
	out := issueops.EdgeReadResult{Anchors: make([]issueops.AnchorEdges, 0, len(req.IDs))}
	for _, id := range req.IDs {
		var edges []*issueops.Dependency
		for _, edge := range records[id] {
			if edge != nil && (len(req.Types) == 0 || containsDependencyType(req.Types, edge.Type)) {
				edges = append(edges, edge)
			}
		}
		out.Anchors = append(out.Anchors, issueops.AnchorEdges{ID: id, Edges: edges})
	}
	return out, nil
}

func containsDependencyType(set []types.DependencyType, t types.DependencyType) bool {
	for _, candidate := range set {
		if candidate == t {
			return true
		}
	}
	return false
}
