package httpapi

import (
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// reopenRequestMembers is the document's member list for ReopenIssueRequest,
// read as raw members for the reason closeRequestMembers is.
var reopenRequestMembers = []string{"actor", "reason"}

// reopenProvenance labels the version-control history entry a reopen records,
// naming the surface it came from.
//
// The role's own default is not good enough for this: the implementations do
// not agree on it — the store-backed ones write "bd: reopen issue" and the
// unit-of-work one "reopen issue" — so a workspace served by one backend today
// and another tomorrow would grow two spellings of the same event. Spelling it
// here makes the entry read the same whichever backend answered, which is the
// field's own recommendation.
//
// It never changes WHETHER history is recorded, only how the entry reads, and
// it is not wire-visible.
const reopenProvenance = "bd serve: reopen issue"

// handleReopen reopens one issue: the close's mirror, and the half that makes a
// recovery flow work end to end over this surface.
//
// It carries the claim's posture verbatim. The actor is caller-ASSERTED
// provenance for the audit trail and not authenticated identity; hooks do not
// fire and the per-command auto-commit machinery does not run, exactly as for
// POST /v0/beads/issues/{id}:claim. The only durable effect is the single
// storage commit the role makes inside its own transaction.
//
// Everything above the role is argument validation. The move itself — which
// statuses count as done, clearing the close reason and session, and the
// `reopened` event the reason is recorded on — belongs to issueops.Lifecycle.
//
// PLANES, as for the close: the id resolves across both, so a reopen whose
// target is a wisp lands on the unversioned plane and records no durable
// history entry.
func (s *Server) handleReopen(w http.ResponseWriter, r *http.Request) {
	// The custom-method dispatcher split the id off the segment and bounded it
	// before this handler was chosen at all; see customMethodTarget.
	id := r.PathValue(customMethodIDValue)
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.reopenRequest(w, r, id)
	if !ok {
		return
	}

	lifecycle, err := s.lifecycle(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := lifecycle.Reopen(r.Context(), request)
	if err != nil {
		// No failReopen sibling, deliberately: this operation has no conflict
		// code and therefore no typed refusal to read extension members out of.
		// The shared mapping is the whole of its error vocabulary, and adding a
		// pass-through wrapper would suggest otherwise.
		s.failErr(w, r, err)
		return
	}
	// `already_open` is the wire's name for the role's unchanged result: a
	// reopen of an issue that was never done. Idempotent, like the re-claim and
	// the re-close, and for the same reason.
	writeJSON(w, apigen.ReopenIssueResponse{
		Issue:       *result.Issue,
		AlreadyOpen: !result.Changed,
	})
}

// reopenRequest decodes and validates the body, and reports whether the request
// may proceed.
func (s *Server) reopenRequest(w http.ResponseWriter, r *http.Request, id string) (issueops.ReopenRequest, bool) {
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.ReopenRequest{}, false
	}
	if offender, unknown := unknownMember(members, reopenRequestMembers); unknown {
		s.failUnknownMember(w, r, offender, reopenRequestMembers)
		return issueops.ReopenRequest{}, false
	}

	actor, ok := s.bodyActor(w, r, members)
	if !ok {
		return issueops.ReopenRequest{}, false
	}
	// The close's bounds, shared rather than restated: this value reaches the
	// `reopened` event rather than a column, and an event stream is printed by
	// the same renderers a close reason is.
	reason, ok := s.storedTextMember(w, r, members, "reason")
	if !ok {
		return issueops.ReopenRequest{}, false
	}

	// ExpectedVersion stays unpublished on this surface, as for the close.
	return issueops.ReopenRequest{
		Actor:      actor,
		IssueID:    id,
		Reason:     reason,
		Provenance: reopenProvenance,
	}, true
}
