package httpapi

import (
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// advanceMoleculeRequestMembers is the document's member list for
// AdvanceMoleculeRequest, refused BY NAME for the reason every other body on
// this surface is.
var advanceMoleculeRequestMembers = []string{claimActorMember, "auto_claim"}

// handleAdvanceMolecule moves a molecule on after one of its steps closed: the
// operation behind `bd close --continue`. Decode, issueops.MoleculeStepper,
// encode — the readiness rule and the claim are the role's.
func (s *Server) handleAdvanceMolecule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue(customMethodIDValue)
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return
	}
	if offender, unknown := unknownMember(members, advanceMoleculeRequestMembers); unknown {
		s.failUnknownMember(w, r, offender, advanceMoleculeRequestMembers)
		return
	}
	actor, ok := s.bodyActor(w, r, members)
	if !ok {
		return
	}
	autoClaim, ok := s.booleanMember(w, r, members, "auto_claim")
	if !ok {
		return
	}

	stepper, err := s.moleculeStepper(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := stepper.Advance(r.Context(), issueops.AdvanceRequest{Actor: actor, ClosedStepID: id, AutoClaim: autoClaim})
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	body := apigen.AdvanceMoleculeResponse{
		Complete:   result.Complete,
		Claimed:    result.Claimed,
		MoleculeId: optionalString(result.MoleculeID),
		NextStep:   result.NextStep,
	}
	if result.ClosedStep != nil {
		body.ClosedStep = *result.ClosedStep
	}
	writeJSON(w, body)
}
