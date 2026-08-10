package httpapi

import (
	"errors"
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// reopenRequestMembers is the document's member list for ReopenIssueRequest,
// read as raw members for the reason closeRequestMembers is.
var reopenRequestMembers = []string{"actor", "reason", expectedVersionMember}

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
		s.failReopen(w, r, request, err)
		return
	}
	result, err := lifecycle.Reopen(r.Context(), request)
	if err != nil {
		s.failReopen(w, r, request, err)
		return
	}
	// `already_open` is the wire's name for the role's unchanged result: a
	// reopen of an issue that was never done. Idempotent, like the re-claim and
	// the re-close, and for the same reason.
	//
	// `revision` is the row's post-reopen concurrency token, on the wire for the
	// close's reason: `expected_version` is a guard a caller cannot fill without
	// it, and a reopen-then-re-close recovery composes its next expectation from
	// this value.
	writeJSON(w, apigen.ReopenIssueResponse{
		Issue:       *result.Issue,
		AlreadyOpen: !result.Changed,
		Revision:    result.Issue.RowVersion,
	})
}

// failReopen answers a failed reopen.
//
// It exists now, where a pass-through wrapper would once have been a lie about
// the vocabulary: this operation has exactly one typed refusal to read an
// extension member out of, and it is the row-version guard rather than a
// policy. Everything else is still the shared mapping.
//
// THE ARM IS FIRST, under the rule ClassifyError states, and the cost of
// forgetting it is a generic 500 for every guard miss here rather than a worse
// 4xx — failClose's finding, on the mirror operation.
func (s *Server) failReopen(w http.ResponseWriter, r *http.Request, request issueops.ReopenRequest, err error) {
	if errors.Is(err, issueops.ErrVersionMismatch) {
		s.fail(w, r, versionPreconditionResult(request.ExpectedVersion))
		return
	}
	s.failErr(w, r, err)
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
	// The close's guard, read the same way: one decoder, one refusal sentence.
	expectedVersion, res := applyVersionGuardMember(members, "")
	if res != nil {
		s.fail(w, r, *res)
		return issueops.ReopenRequest{}, false
	}

	return issueops.ReopenRequest{
		Actor:           actor,
		IssueID:         id,
		Reason:          reason,
		ExpectedVersion: expectedVersion,
		Provenance:      reopenProvenance,
	}, true
}
