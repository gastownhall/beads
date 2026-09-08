package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// closeRequestMembers is the document's member list for CloseIssueRequest. The
// schema is additionalProperties: false, so anything else is refused BY NAME —
// which is why the body is decoded as raw members first, the posture claim and
// batchCreate already take.
var closeRequestMembers = []string{"actor", "reason", "session", "force", expectedVersionMember}

// handleClose closes one issue: the second half of the loop this surface exists
// to serve, and the half that still forked a subprocess.
//
// It carries the claim's posture verbatim. The actor is caller-ASSERTED
// provenance for the audit trail and not authenticated identity; hooks do not
// fire and the per-command auto-commit machinery does not run, exactly as for
// POST /v0/beads/issues/{id}:claim. The only durable effect is the single
// storage commit the role makes inside its own transaction.
//
// Everything above the role is argument validation: the media type, the body
// shape, and the actor, reason and session rules. The close itself — the
// done-status normalization, first-close-wins on reason and session, the open-
// children and live-blocker policy, the transaction retry — belongs to
// issueops.Lifecycle, reached through the provider's own accessor.
//
// PLANES. Unlike the claim, this resolves ids across BOTH planes, because the
// role does and this surface serves the same work contract the CLI serves. A
// close whose target is a wisp lands on the unversioned plane and records no
// durable history entry.
func (s *Server) handleClose(w http.ResponseWriter, r *http.Request) {
	// The custom-method dispatcher split the id off the segment and bounded it
	// before this handler was chosen at all; see customMethodTarget.
	id := r.PathValue(customMethodIDValue)
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.closeRequest(w, r, id)
	if !ok {
		return
	}

	lifecycle, err := s.lifecycle(r)
	if err != nil {
		s.failClose(w, r, request, err)
		return
	}
	result, err := lifecycle.Close(r.Context(), request)
	if err != nil {
		s.failClose(w, r, request, err)
		return
	}
	// `already_closed` is the wire's name for the idempotent re-close, which is
	// exactly the case the role reports as an unchanged result — and the case
	// whose `reason` and `session` were NOT rewritten.
	//
	// `revision` is the row's post-close concurrency token, read off the same
	// snapshot rather than computed. It is on the wire because `expected_version`
	// is: a guard whose token no response carries is a guard a caller cannot
	// fill. types.Issue.RowVersion is `json:"-"`, so the Issue body cannot carry
	// it and this member is where it lives — updateIssue's arrangement exactly.
	writeJSON(w, apigen.CloseIssueResponse{
		Issue:         *result.Issue,
		AlreadyClosed: !result.Changed,
		OpenChildren:  result.OpenChildren,
		Revision:      result.Issue.RowVersion,
	})
}

// closeRequest decodes and validates the body, and reports whether the request
// may proceed. Every refusal here happens BEFORE any database work, which is
// what lets the 400s reflect the caller's own input back.
func (s *Server) closeRequest(w http.ResponseWriter, r *http.Request, id string) (issueops.CloseRequest, bool) {
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.CloseRequest{}, false
	}
	if offender, unknown := unknownMember(members, closeRequestMembers); unknown {
		s.failUnknownMember(w, r, offender, closeRequestMembers)
		return issueops.CloseRequest{}, false
	}

	actor, ok := s.bodyActor(w, r, members)
	if !ok {
		return issueops.CloseRequest{}, false
	}
	reason, ok := s.storedTextMember(w, r, members, "reason")
	if !ok {
		return issueops.CloseRequest{}, false
	}
	session, ok := s.storedTextMember(w, r, members, "session")
	if !ok {
		return issueops.CloseRequest{}, false
	}
	force, ok := s.booleanMember(w, r, members, "force")
	if !ok {
		return issueops.CloseRequest{}, false
	}
	// The guard the response's `revision` exists to feed. Read through the
	// shared int64 decoder rather than a local one so a malformed token is
	// refused with the same sentence on every operation that takes one.
	expectedVersion, res := applyVersionGuardMember(members, "")
	if res != nil {
		s.fail(w, r, *res)
		return issueops.CloseRequest{}, false
	}

	return issueops.CloseRequest{
		Actor:           actor,
		IssueID:         id,
		Reason:          reason,
		Session:         session,
		Force:           force,
		ExpectedVersion: expectedVersion,
	}, true
}

// bodyActor validates `actor` under the claim's rules, shared rather than
// restated: the value lands in the same columns and the same storage commit
// message, so a newline forges the same audit-trail lines.
//
// It is batchCreateActor's body, lifted so that every operation carrying an
// actor in an object body reads it the same way. The claim keeps its own
// because its body has exactly one member and it refuses the rest by name
// before looking at any of them.
func (s *Server) bodyActor(w http.ResponseWriter, r *http.Request, members map[string]json.RawMessage) (string, bool) {
	raw, ok := members[claimActorMember]
	if !ok {
		s.fail(w, r, InvalidArgument(claimActorMember, ReasonInvalidValue, "`"+claimActorMember+"` is required"))
		return "", false
	}
	// Through a POINTER so that `null` reaches the type-mismatch branch, for
	// the reason claimActor gives: unmarshaling JSON null into a string is a
	// no-op, which would report a null as "empty after trimming" — the right
	// status attached to prose that misdescribes what the client sent.
	var actor *string
	if err := json.Unmarshal(raw, &actor); err != nil || actor == nil {
		s.fail(w, r, InvalidArgument(claimActorMember, ReasonInvalidValue, "`"+claimActorMember+"` must be a string"))
		return "", false
	}
	trimmed, res := validateActor(*actor)
	if res != nil {
		s.fail(w, r, *res)
		return "", false
	}
	return trimmed, true
}

// storedTextMember reads an optional string member destined for a stored
// column, or reports the refusal it earned. An absent member is the empty
// string, which is what the role reads as "not supplied".
//
// The bound is types.CheckFieldLen keyed on storage's own constant rather than
// a second copy of the schema's number — the actor precedent, where the
// schema's maxLength is prose and the constant is binding. Control characters
// are refused for the actor's reason: these values land in columns that
// renderers print, so an unfiltered C1 introducer makes a close reason an
// escape-sequence payload in anything that shows it.
//
// Explicit `null` is a 400 naming the member rather than a clear. This surface
// has no clearable member here — the role reads absence as "not supplied" and
// first-close-wins means a re-close writes neither — so admitting null would
// publish a third state with no meaning behind it.
func (s *Server) storedTextMember(w http.ResponseWriter, r *http.Request, members map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := members[name]
	if !ok {
		return "", true
	}
	refuse := func(detail string) bool {
		s.fail(w, r, InvalidArgument(name, ReasonInvalidValue, detail))
		return false
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return "", refuse("`" + name + "` must be a string")
	}
	switch {
	case types.CheckFieldLen(name, *value) != nil:
		return "", refuse(fmt.Sprintf("`%s` is %d characters; storage holds at most %d",
			name, utf8.RuneCountInString(*value), types.MaxFieldLen))
	case strings.ContainsFunc(*value, isControlChar):
		return "", refuse("`" + name + "` must not contain control characters")
	}
	return *value, true
}

// booleanMember reads an optional boolean member, defaulting to false — the
// document's default for every flag on this surface. Explicit `null` is a 400
// naming the member, by storedTextMember's rule and for its reason.
func (s *Server) booleanMember(w http.ResponseWriter, r *http.Request, members map[string]json.RawMessage, name string) (bool, bool) {
	raw, ok := members[name]
	if !ok {
		return false, true
	}
	var value *bool
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		s.fail(w, r, InvalidArgument(name, ReasonInvalidValue, "`"+name+"` must be a boolean"))
		return false, false
	}
	return *value, true
}

// failClose answers a failed close: the precondition's 409, then the shared
// classification with the extension member the open-children 409 carries.
//
// THE PRECONDITION ARM IS MATCHED TYPED AND FIRST, obeying the rule
// ClassifyError states rather than restating it: that function cannot build
// this 409, because the refusal echoes the value the REQUEST guarded on and it
// is handed an error alone.
//
// The cost of forgetting the arm is not a worse 4xx. Neither leg wraps
// ErrVersionMismatch in ErrValidation — the store legs return CheckVersionInTx's
// error through runIssueOperationTx unchanged and the unit of work returns its
// own — so it arrives as a bare sentinel, falls through failErr's default, and
// every guard miss on this operation becomes a GENERIC 500. Verified by
// mutation rather than asserted: removing the arm turns
// TestCloseRefusesAStaleGuard into a 500.
//
// The open-children count comes from *issueops.CloseOpenChildrenError's own
// field, which the role fills inside the transaction that refused — never from
// parsing the sentinel's message ("%d open child issue(s)"). That substring
// classification is what a client adopting this endpoint gets to delete, and it
// can only delete it if the server never does it either.
//
// The live-blocker refusal shares the code and carries NO member, which is what
// makes member presence the discriminator the document promises.
func (s *Server) failClose(w http.ResponseWriter, r *http.Request, request issueops.CloseRequest, err error) {
	if errors.Is(err, issueops.ErrVersionMismatch) {
		s.fail(w, r, versionPreconditionResult(request.ExpectedVersion))
		return
	}
	var openChildren *issueops.CloseOpenChildrenError
	if !errors.As(err, &openChildren) {
		s.failErr(w, r, err)
		return
	}
	res := ClassifyError(err)
	if res.Problem.Code == string(CodeNotClosable) {
		res = res.WithOpenChildren(openChildren.OpenChildren)
	}
	s.fail(w, r, res)
}

// versionPreconditionResult builds the 409 for the row-version guard that
// missed, naming the member and echoing what the request asked for.
//
// It is shared by the close, the reopen and the delete because all three
// publish ONE guard, where updatePreconditionResult has to choose between
// three. The rule is updatePreconditionResult's unchanged: the expected value
// comes from the REQUEST rather than from a read, and the observed value is
// absent, because the refusal rolled its transaction back and a read afterwards
// would describe a row the refusal never saw. See PreconditionFailed.
//
// A nil expectation cannot reach here from any of the three handlers — the
// role raises this sentinel only when a guard was sent — but it is handled
// rather than dereferenced, so a role that returns it unprompted is a 409
// without the echoed member instead of a panic on a live server.
func versionPreconditionResult(expected *int64) Result {
	res := PreconditionFailed()
	member := expectedVersionMember
	res.Problem.Param = &member
	if expected != nil {
		res = res.WithExpectedVersion(*expected)
	}
	return res
}

// expectedVersionMember is the one spelling of the row-version guard, shared by
// the three operations that publish it so the member name a client reads off
// `param` cannot drift from the member name it sent.
const expectedVersionMember = "expected_version"
