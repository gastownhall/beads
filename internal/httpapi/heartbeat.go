package httpapi

import (
	"errors"
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// heartbeatRequestMembers is the document's member list for
// HeartbeatIssueRequest. The schema is additionalProperties: false, so anything
// else is refused BY NAME, the posture every body on this surface takes.
var heartbeatRequestMembers = []string{claimActorMember}

// handleHeartbeat refreshes the lease on one issue the actor holds: the verb
// `bd heartbeat` spells, behind issueops.Heartbeater.
//
// A REFRESH WRITES ONLY THE LEASE ROW, and that is the role's promise rather
// than this handler's: no revision is reminted (so the holder's own guarded
// writes keep their token across heartbeats), no version is recorded,
// updated_at does not move, and no event or journal entry is written. A
// RE-ARM (`rearmed: true`: a live claim that had no lease row) is recorded as
// a mutation and remints the revision; see issueops.Heartbeater. The response
// carries no `revision` member either way: it reports the lease, not the
// issue, so a refresh publishes no unchanged token a reader could take for a
// write, and `rearmed: true` tells the caller to re-read the issue before a
// guarded write.
//
// THE ACTOR IS THE FENCE'S SUBJECT, the release's posture: the heartbeat lands
// only while the actor holds the claim. It is caller-asserted provenance, not
// authenticated identity, exactly as on the claim.
//
// PLANES: the id resolves across both, so a wisp is answered with the role's
// typed refusal (`lease_not_held`) rather than a 404 — wisps are never leased.
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	// The custom-method dispatcher split the id off the segment and bounded it
	// before this handler was chosen at all; see customMethodTarget.
	id := r.PathValue(customMethodIDValue)
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	actor, ok := s.heartbeatActor(w, r)
	if !ok {
		return
	}

	heartbeater, err := s.heartbeater(r)
	if err != nil {
		s.failHeartbeat(w, r, err)
		return
	}
	result, err := heartbeater.Heartbeat(r.Context(), issueops.HeartbeatRequest{Actor: actor, IssueID: id})
	if err != nil {
		s.failHeartbeat(w, r, err)
		return
	}
	writeJSON(w, apigen.HeartbeatIssueResponse{
		Id:             result.IssueID,
		Holder:         result.Holder,
		HeartbeatAt:    result.HeartbeatAt.UTC(),
		LeaseExpiresAt: result.LeaseExpiresAt.UTC(),
		Rearmed:        result.Rearmed,
	})
}

// heartbeatActor decodes the body and validates its one member by the claim's
// rules (validateActor, through bodyActor), and reports whether the request may
// proceed. Every refusal here happens BEFORE any database work.
func (s *Server) heartbeatActor(w http.ResponseWriter, r *http.Request) (string, bool) {
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return "", false
	}
	if offender, unknown := unknownMember(members, heartbeatRequestMembers); unknown {
		s.failUnknownMember(w, r, offender, heartbeatRequestMembers)
		return "", false
	}
	return s.bodyActor(w, r, members)
}

// failHeartbeat answers a failed heartbeat, mapping the role's TYPED refusals
// onto the frozen codes.
//
// The 409 arms are matched first, from *issueops.LeaseConflictError's typed
// fields — never from the sentinel's prose — and BEFORE the generic arms:
// ErrLeaseNotHeld also matches ErrNotClaimable, which ClassifyError would
// answer `not_claimable`, the code CodeLeaseNotHeld's doc says this must not
// be.
//
// A FOREIGN HOLDER is the claim's own conflict shape: `already_claimed`
// naming the holder in `assignee`, with `issue_status`. The role reads both
// inside the refusing transaction.
func (s *Server) failHeartbeat(w http.ResponseWriter, r *http.Request, err error) {
	// The 4xx path does not log by default, so the refusals whose real reason
	// the response replaces with the server's own words are recorded here.
	refused := func() {
		s.event("request_refused", "request_id", requestInfo(r.Context()).id, "error", err.Error())
	}

	var conflict *issueops.LeaseConflictError
	var field *issueops.HeartbeatFieldError
	switch {
	case errors.As(err, &conflict) && errors.Is(err, issueops.ErrAlreadyClaimed):
		res := newResult(CodeAlreadyClaimed,
			"another actor holds this issue's claim; nothing on the issue was written")
		if conflict.Holder != "" {
			res = res.WithAssignee(conflict.Holder)
		}
		if conflict.Status != "" {
			res = res.WithIssueStatus(string(conflict.Status))
		}
		s.fail(w, r, res)

	case errors.As(err, &conflict) && errors.Is(err, issueops.ErrLeaseNotHeld):
		refused()
		// The detail names the state the refusing transaction found, from the
		// typed fields (never the role's prose): a wisp, an unclaimed issue, or
		// the status the claim was ended into. A wisp also carries
		// `reason: ephemeral`, the typed form a client reads.
		res := newResult(CodeLeaseNotHeld, leaseNotHeldDetail(conflict))
		if conflict.Status != "" {
			res = res.WithIssueStatus(string(conflict.Status))
		}
		if conflict.Ephemeral {
			res = res.WithReason(ReasonEphemeral)
		}
		s.fail(w, r, res)

	case errors.Is(err, storage.ErrNotFound):
		s.fail(w, r, NotFound())

	// The role's request rules. The actor's are refused at the edge by the
	// claim's stricter rules, and the dispatcher guarantees a non-empty id, so
	// this is defensive — but it names the member when it fires, rather than
	// a generic refusal.
	case errors.As(err, &field) && field.Field == issueops.HeartbeatFieldActor:
		refused()
		s.fail(w, r, InvalidArgument(claimActorMember, ReasonInvalidValue, field.Detail))

	case !errors.Is(err, storage.ErrValidation):
		s.failErr(w, r, err)

	default:
		refused()
		s.fail(w, r, InvalidArgument("", ReasonInvalidValue,
			"the request was refused by this workspace's own validation; nothing was written"))
	}
}

// leaseNotHeldDetail says WHICH state refused a heartbeat with no live claim,
// from *issueops.LeaseConflictError's typed fields. Each sentence names one
// state only, so a reader of `bd heartbeat` output (and a pattern over it)
// can tell a closed issue from a released one and from a wisp.
func leaseNotHeldDetail(conflict *issueops.LeaseConflictError) string {
	// The tail is "on the issue": the one write a refusal can make is deleting
	// a stale lease row this actor still held (see the 409's description).
	const tail = "; nothing on the issue was written"
	switch {
	case conflict.Ephemeral:
		return "this issue is an ephemeral wisp, which is never leased" + tail
	case conflict.Status == "":
		return "this actor holds no live claim on this issue" + tail
	case conflict.Status == types.StatusInProgress:
		return "this actor holds no live claim on this issue: it is in_progress but not claimed by this actor" + tail
	case conflict.Status == types.StatusOpen:
		return "this actor holds no live claim on this issue: it is open and not claimed (released, reclaimed or never claimed)" +
			tail + ". Claim it again rather than heartbeating"
	default:
		return "this actor holds no live claim on this issue: it is " + string(conflict.Status) + tail
	}
}
