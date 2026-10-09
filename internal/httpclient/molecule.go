package httpclient

import (
	"context"
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

// requireAutoCloseMolecule refuses, before anything is dialed, a close that
// asks for AutoCloseMolecule against a server that does not advertise
// wire.CapCloseAutoCloseMolecule. The rule runs in ONE place — the server's
// library, inside the step close's transaction — so the client never runs a
// second, client-side copy of it, and it never drops the member and closes the
// step without the molecule either. The refusal is the typed capability error
// (Unsup.Capability names the token).
func (s *Store) requireAutoCloseMolecule(ctx context.Context, op string, requested bool) error {
	if !requested {
		return nil
	}
	served, err := s.servesCapability(ctx, wire.CapCloseAutoCloseMolecule)
	if err != nil {
		return err
	}
	if !served {
		return s.unsupportedCapability(op, wire.CapCloseAutoCloseMolecule)
	}
	return nil
}

// MoleculeStepper serves the advance-a-molecule role on
// POST /v0/beads/issues/{id}:advanceMolecule.
func (s *Store) MoleculeStepper() (issueops.MoleculeStepper, error) {
	if _, err := s.roleWire("MoleculeStepper"); err != nil {
		return nil, err
	}
	return &httpMoleculeStepper{store: s}, nil
}

// httpMoleculeStepper dials advanceMolecule, where the server's library runs
// the one stepper. Against a server that does not advertise
// issues.advanceMolecule it refuses before dialing with the typed capability
// error; it never composes a client-side copy of the advance.
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
		return issueops.AdvanceResult{}, m.store.unsupportedCapability("MoleculeStepper.Advance", token)
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
