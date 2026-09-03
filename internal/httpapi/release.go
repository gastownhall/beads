package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

const (
	// releaseExpectedAssigneeMember is the compare-and-set guard on the holder.
	// It is named the way updateIssue names its own guard, because it is the
	// same guard — but it does NOT admit the empty string, which is the one
	// place the two disagree. See its schema.
	releaseExpectedAssigneeMember = "expected_assignee"
	// releaseForceMember is the ownership-fence bypass.
	releaseForceMember = "force"
)

// releaseRequestMembers is the document's member list for ReleaseIssueRequest.
// The schema is additionalProperties: false, so anything else is refused BY
// NAME, the posture every body on this surface takes.
var releaseRequestMembers = []string{claimActorMember, releaseExpectedAssigneeMember, releaseForceMember}

// handleRelease gives back the claim on one issue: the claim's inverse, and the
// verb `bd unclaim` spells.
//
// It carries the claim's posture verbatim. The actor is caller-ASSERTED
// provenance and not authenticated identity; hooks do not fire and the
// per-command auto-commit machinery does not run. The only durable effect is
// the single storage commit the role makes inside its own transaction.
//
// THE ACTOR IS ALSO THE OWNERSHIP FENCE'S SUBJECT here, which it is on no other
// operation: a release carrying neither guard nor force succeeds only while the
// actor is the holder. That is not authorization: the actor is not the
// authenticated principal even where a bearer is required, because the token a
// deployment configures is shared and admits a client to the whole surface. It
// is the anti-yank guard the claim gets from refusing a foreign holder, pointed
// the other way, and it is the role's rather than this handler's.
//
// PLANES, as for close and reopen and unlike the claim: the id resolves across
// both. A wisp can hold a claim, so an operation that refused to release one
// would strand an ephemeral row owned by an agent that is gone.
func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	// The custom-method dispatcher split the id off the segment and bounded it
	// before this handler was chosen at all; see customMethodTarget.
	id := r.PathValue(customMethodIDValue)
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.releaseRequest(w, r, id)
	if !ok {
		return
	}

	releaser, err := s.releaser(r)
	if err != nil {
		s.failRelease(w, r, request, err)
		return
	}
	result, err := releaser.Release(r.Context(), request)
	if err != nil {
		s.failRelease(w, r, request, err)
		return
	}
	// `changed` is the role's own, and it is true on every answer that reaches
	// here: the role refuses every shape that would not write. It is published
	// because claimIssue and updateIssue publish the same fact, and it is the
	// negative space that answers "where is the already_released member" —
	// there is none, and the document says why.
	//
	// `revision` is the row's post-write concurrency token, read off the same
	// snapshot rather than computed. A release REMINTS it, so this member is
	// the only way a caller composing a following `expected_version` stays in
	// step; types.Issue.RowVersion is `json:"-"`, so the issue body cannot
	// carry it.
	writeJSON(w, apigen.ReleaseIssueResponse{
		Issue:    *result.Issue,
		Changed:  result.Changed,
		Revision: result.Issue.RowVersion,
	})
}

// releaseRequest decodes and validates the body, and reports whether the
// request may proceed. Every refusal here happens BEFORE any database work.
//
// The two guard members are validated AGAINST EACH OTHER at the edge, not only
// individually, because the role refuses the pair and the caller deserves to
// learn that from a 400 naming a member rather than from a generic refusal that
// cost two round trips.
func (s *Server) releaseRequest(w http.ResponseWriter, r *http.Request, id string) (issueops.ReleaseRequest, bool) {
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.ReleaseRequest{}, false
	}
	if offender, unknown := unknownMember(members, releaseRequestMembers); unknown {
		s.failUnknownMember(w, r, offender, releaseRequestMembers)
		return issueops.ReleaseRequest{}, false
	}

	actor, ok := s.bodyActor(w, r, members)
	if !ok {
		return issueops.ReleaseRequest{}, false
	}
	expected, ok := s.releaseExpectedAssignee(w, r, members)
	if !ok {
		return issueops.ReleaseRequest{}, false
	}
	force, ok := s.booleanMember(w, r, members, releaseForceMember)
	if !ok {
		return issueops.ReleaseRequest{}, false
	}
	// The pair the role refuses, refused here by name. They are answers to the
	// same question and they disagree — the guard says "only if X still holds
	// it" and force says "whoever holds it" — so honoring either would be this
	// server picking which half of the request the caller meant.
	//
	// It names `force`, the member that is redundant given the guard, so a
	// caller that added a belt-and-braces flag learns which one to drop.
	if force && expected != nil {
		s.fail(w, r, InvalidArgument(releaseForceMember, ReasonInvalidValue,
			"`"+releaseForceMember+"` and `"+releaseExpectedAssigneeMember+"` disagree about which claim to release; send one"))
		return issueops.ReleaseRequest{}, false
	}

	return issueops.ReleaseRequest{
		Actor:            actor,
		IssueID:          id,
		ExpectedAssignee: expected,
		Force:            force,
	}, true
}

// releaseExpectedAssignee reads the optional compare-and-set guard.
//
// ABSENT AND ONLY ABSENT selects the unconditional path, so this answers a
// POINTER: the role models "do not check" as nil and this member has no other
// spelling for it. Explicit `null` is a 400 rather than a second spelling of
// absent, the rule every optional member on this surface follows.
//
// THE EMPTY STRING IS A 400 — the one place this member disagrees with
// updateIssue's `expected_assignee`, where an empty string is a real guard
// meaning "expected unassigned". "Release a row nobody holds" describes no
// release, so the role refuses it and this refuses it at the edge, where the
// 400 can name the member. Emptiness is judged AFTER trimming, matching the
// role, and the value is passed on UNTRIMMED, also matching the role: a padded
// expectation must lose every time rather than intermittently.
//
// It is not length- or pattern-checked the way an actor is. This value is
// COMPARED and never stored, so a value no assignee column could hold simply
// cannot match, and refusing it here would be a refusal the role does not have.
func (s *Server) releaseExpectedAssignee(w http.ResponseWriter, r *http.Request, members map[string]json.RawMessage) (*string, bool) {
	raw, present := members[releaseExpectedAssigneeMember]
	if !present {
		return nil, true
	}
	refuse := func(detail string) (*string, bool) {
		s.fail(w, r, InvalidArgument(releaseExpectedAssigneeMember, ReasonInvalidValue, detail))
		return nil, false
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return refuse("`" + releaseExpectedAssigneeMember + "` must be a string")
	}
	if strings.TrimSpace(*value) == "" {
		return refuse("`" + releaseExpectedAssigneeMember + "` is empty after trimming; omit it to release whatever claim is there, " +
			"or read the row if you meant to assert that nobody holds it")
	}
	return value, true
}

// failRelease answers a failed release, mapping the role's TYPED refusals onto
// the frozen codes.
//
// EVERY 409 BRANCH IS MATCHED BEFORE THE ErrValidation AND ErrNotFound ARMS,
// and that order is the whole correctness of this function — failUpdate's
// hazard, in a sharper form. None of the four refusals below is wrapped in
// ErrValidation by any leg: ReleaseIssueInTx returns its two classifications
// bare and passes the raw seam's two through unchanged, and all three database
// legs reach that one function. Below a generic arm they would fall into
// failErr and be answered 500 — on every leg, for four conditions this document
// names by code.
//
// NO BRANCH QUOTES THE ROLE'S MESSAGE. Every one of these refusals formats its
// observation into prose — the holder it found, the status it saw — and this
// surface publishes typed members or nothing. Three of the four have nothing
// typed to publish, so they publish nothing; see the codes' own docs. The real
// error goes to the log with the request id.
func (s *Server) failRelease(w http.ResponseWriter, r *http.Request, request issueops.ReleaseRequest, err error) {
	// The 4xx path does not log by default, so the refusals whose real reason
	// the response replaces with the server's own words are recorded here.
	refused := func() {
		s.event("request_refused", "request_id", requestInfo(r.Context()).id, "error", err.Error())
	}

	switch {
	// THE GUARD, and it is matched first because it is the only refusal here
	// with a member to carry. `expected_assignee` is the REQUEST's value and
	// there is no `actual_assignee` beside it: the refusal rolled its
	// transaction back, and the role carries the holder it observed in prose
	// only — no ClaimConflictError equivalent exists for this sentinel. That is
	// updateIssue's rule for the same guard, unchanged.
	case errors.Is(err, issueops.ErrAssigneeMismatch):
		res := PreconditionFailed()
		res.Problem.Param = releaseGuardParam()
		if request.ExpectedAssignee != nil {
			res = res.WithExpectedAssignee(*request.ExpectedAssignee)
		}
		s.fail(w, r, res)

	// THE OWNERSHIP FENCE, answered with the code updateIssue already gives the
	// same situation: a live foreign owner refusing a write, with a force
	// bypass and a name-the-holder bypass. No `assignee` member, for the reason
	// that operation states — the fence refuses without naming the holder, so
	// absence means "re-read the row" and never "nobody holds it".
	case errors.Is(err, issueops.ErrNotOwner):
		s.fail(w, r, newResult(CodeAlreadyClaimed,
			"this issue is held by another actor; send `"+releaseForceMember+"`, or name the holder in `"+
				releaseExpectedAssigneeMember+"`"))

	// THE ROW REFUSING TO PRODUCE A RELEASE AT ALL, under one code and with no
	// member telling the two conditions apart. Neither sentinel carries a typed
	// observation, and this surface does not scrape prose to invent one; the
	// code's own doc carries the analysis, including why splitting later is the
	// direction that stays open.
	//
	// The detail names both conditions rather than guessing between them, so a
	// human reading a `bd unclaim` failure is not told a closed issue is
	// unclaimed or the reverse.
	case errors.Is(err, issueops.ErrNotClaimed), errors.Is(err, issueops.ErrNotReleasable):
		refused()
		s.fail(w, r, newResult(CodeNotReleasable,
			"this issue holds no claim, or its status is neither `open` nor `in_progress`; nothing was written. "+
				"Read the row rather than assuming the claim is gone: a status this workspace configured can refuse a "+
				"release while the row is still assigned"))

	case errors.Is(err, storage.ErrNotFound):
		s.fail(w, r, NotFound())

	case !errors.Is(err, storage.ErrValidation):
		s.failErr(w, r, err)

	// DEFENSIVE. Every request-validation refusal the role has is refused at the
	// edge above — an empty actor, a blank guard, force beside the guard — and
	// the dispatcher guarantees a non-empty id, so nothing should reach this.
	// It stays because the alternative if that ever stops being true is a 500
	// for a request the caller could have fixed.
	default:
		refused()
		s.fail(w, r, InvalidArgument("", ReasonInvalidValue,
			"the request was refused by this workspace's own validation; nothing was written"))
	}
}

// releaseGuardParam names the guard member on a `precondition_failed`, so a
// client reads one member to find the offending input whichever way the request
// was refused. It is a function for updateGuardParam's reason: `param` is a
// pointer on the envelope and a package-level address would be shared state.
func releaseGuardParam() *string {
	member := releaseExpectedAssigneeMember
	return &member
}
