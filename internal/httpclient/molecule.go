package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/issueops"
)

// servesCapability reports whether the server advertises token. It forces the
// one lazy handshake the store owns and returns that handshake's error rather
// than reading a failure as "not served": every caller below is a write whose
// two legs both dial, so a handshake that cannot be had means the write cannot
// be attempted at all (servesBatchClose's reasoning).
func (s *Store) servesCapability(ctx context.Context, token string) (bool, error) {
	snap, err := s.snapshot(ctx)
	if err != nil {
		return false, err
	}
	return snap != nil && slices.Contains(snap.Capabilities, token), nil
}

// autoCloseMoleculeOverRoles is the DEGRADED auto-close, for a server that
// does not advertise wire.CapCloseAutoCloseMolecule: after the step close has
// landed, it runs the library's own rule composed over this client's roles
// (issueops.CloseCompletedMolecule — CompletedMolecule's read, then a
// Lifecycle.Close guarded on the root revision it read). It is the same rule
// the server runs in the step close's transaction, in two transactions
// instead of one; nothing is dropped.
//
// The step close it follows has already succeeded, so a failure here is
// reported in the refusal slot rather than failing that close.
func (s *Store) autoCloseMoleculeOverRoles(ctx context.Context, stepID, actor, session string) (*issueops.Issue, string) {
	reader, err := issueops.NewMoleculeReader(s)
	if err != nil {
		return nil, fmt.Sprintf("molecule auto-close did not run: %v", err)
	}
	lifecycle, err := s.IssueLifecycle()
	if err != nil {
		return nil, fmt.Sprintf("molecule auto-close did not run: %v", err)
	}
	root, refusal, err := issueops.CloseCompletedMolecule(ctx, reader, lifecycle, stepID, actor, session)
	if err != nil {
		return nil, fmt.Sprintf("molecule auto-close did not run: %v", err)
	}
	return root, refusal
}

// autoCloseMoleculeMember reports whether a close asking for AutoCloseMolecule
// may carry the member (the server advertises it), and whether the caller must
// run the degraded auto-close after the close instead.
func (s *Store) autoCloseMoleculeMember(ctx context.Context, requested bool) (send, degrade bool, err error) {
	if !requested {
		return false, false, nil
	}
	served, err := s.servesCapability(ctx, wire.CapCloseAutoCloseMolecule)
	if err != nil {
		return false, false, err
	}
	return served, !served, nil
}

// MoleculeStepper serves the advance-a-molecule role on
// POST /v0/beads/issues/{id}:advanceMolecule.
func (s *Store) MoleculeStepper() (issueops.MoleculeStepper, error) {
	if _, err := s.roleWire("MoleculeStepper"); err != nil {
		return nil, err
	}
	return &httpMoleculeStepper{store: s}, nil
}

// httpMoleculeStepper dials advanceMolecule where the server advertises
// issues.advanceMolecule. Against an older server it DEGRADES rather than
// refuses: it runs issueops.NewMoleculeStepper composed over this client's own
// roles — the very implementation every backend's accessor answers — so the
// outcome is the same rule, read and claimed over more round trips.
type httpMoleculeStepper struct{ store *Store }

var _ issueops.MoleculeStepper = (*httpMoleculeStepper)(nil)

func (m *httpMoleculeStepper) Advance(ctx context.Context, req issueops.AdvanceRequest) (issueops.AdvanceResult, error) {
	if err := issueops.ValidateAdvanceRequest(req); err != nil {
		return issueops.AdvanceResult{}, err
	}
	token, _ := wire.CapabilityFor(wire.OpAdvanceMolecule)
	served, err := m.store.servesCapability(ctx, token)
	if err != nil {
		return issueops.AdvanceResult{}, err
	}
	if !served {
		roles, err := issueops.MoleculeStepperRolesOf(m.store)
		if err != nil {
			return issueops.AdvanceResult{}, err
		}
		return issueops.NewMoleculeStepper(roles).Advance(ctx, req)
	}

	path, err := wire.IssueMethodPath(req.ClosedStepID, wire.MethodAdvanceMolecule)
	if err != nil {
		return issueops.AdvanceResult{}, err
	}
	body := apigen.AdvanceMoleculeRequest{Actor: req.Actor}
	if req.AutoClaim {
		autoClaim := true
		body.AutoClaim = &autoClaim
	}
	var res apigen.AdvanceMoleculeResponse
	if err := m.store.dispatch(ctx, wire.Request{
		Op:      wire.OpAdvanceMolecule,
		Method:  http.MethodPost,
		Path:    path,
		Body:    body,
		IssueID: req.ClosedStepID,
	}, &res); err != nil {
		return issueops.AdvanceResult{}, err
	}
	closed := res.ClosedStep
	out := issueops.AdvanceResult{
		ClosedStep: &closed,
		Complete:   res.Complete,
		NextStep:   res.NextStep,
		Claimed:    res.Claimed,
	}
	if res.MoleculeId != nil {
		out.MoleculeID = *res.MoleculeId
	}
	return out, nil
}
