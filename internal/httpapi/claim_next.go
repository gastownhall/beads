package httpapi

import (
	"errors"
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// claimNextRequestMembers is the document's member list for ClaimNextRequest.
// It carries the actor and nothing else — the FILTER is in the query string,
// for the reason the schema gives — and the schema is
// additionalProperties: false, so anything else is refused BY NAME.
var claimNextRequestMembers = []string{claimActorMember}

// handleClaimNext takes ONE ready issue and hands it back claimed.
//
// It exists to retire a RACE, not to save a round trip. A client composing
// GET /v0/beads/ready with POST /v0/beads/issues/{id}:claim reads a row that
// another agent claims before the second request arrives, so it earns a 409 for
// a row it was correctly offered — and a fleet polling one queue spends its
// requests losing those races. Selection, the compare-and-set and the hydration
// share one transaction here, so the row cannot move between being chosen and
// being reported.
//
// THE FILTER IS THE LISTING'S, decoded by readyFilters itself rather than by a
// copy that admits the same names. That is the same argument countReadyWork
// makes for sharing it: a claim answering a different question than `bd ready`
// shows would hand an agent work the listing never offered it.
//
// A COLLECTION-LEVEL CUSTOM METHOD, spelled the way issues:sweep is: the
// segment is a literal, so the router registers the documented path and no
// dispatcher exception is needed. It names no id because the caller names a
// QUESTION — which is also why it has no 404.
//
// Hooks do not fire and the per-command auto-commit machinery does not run,
// exactly as for the claim.
func (s *Server) handleClaimNext(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())
	req := issueops.ClaimNextRequest{Filter: readyFilters(q)}
	// EXPLICIT, always, for handleReady's reason: the storage layer maps an
	// empty sort policy to hybrid, and forwarding an absent `sort` as "" would
	// silently adopt that fallback while the document still reads
	// `default: priority`. It matters more here than on the listing — the sort
	// decides WHICH row this operation writes to, not merely what order rows
	// are printed in.
	req.Filter.Sort = q.oneOf("sort", readySortDefault, "hybrid", "priority", "oldest")
	// `limit` is REFUSED BY VALUE rather than left to the unknown-parameter
	// rule, and the distinction is the difference between two client
	// recoveries. `unknown_parameter` means "this server does not know that
	// name — version skew, degrade or fall back", which would be a lie: the
	// name is one this server knows perfectly well on the sibling listing.
	// This operation will not ACT on it, which is `invalid_value`.
	if q.has("limit") {
		q.invalid("limit", "this operation takes no `limit`: it delivers the one row it wins, "+
			"and its scan must stay unbounded or a window of rows a racing agent already took would report nothing to claim")
	}
	if !s.acceptQuery(w, r, q) {
		return
	}
	actor, ok := s.claimNextActor(w, r)
	if !ok {
		return
	}
	req.Actor = actor

	claimer, err := s.readyClaimer(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := claimer.ClaimNext(r.Context(), req)
	if err != nil {
		s.failClaimNext(w, r, err)
		return
	}
	// A nil row is a 200 with `claimed` ABSENT, which is the role's own answer
	// rather than this handler's interpretation: an empty ready front is the
	// steady state of a drained queue, and a polling agent that had to classify
	// an error to discover it would be pattern-matching prose.
	writeJSON(w, apigen.ClaimNextResponse{Claimed: result.Claimed})
}

// claimNextActor decodes the one body member. It is the close's bodyActor
// behind the claim's refuse-the-rest-by-name check, because this body has
// exactly one member and the document says so.
func (s *Server) claimNextActor(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.requireJSONContent(w, r) {
		return "", false
	}
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return "", false
	}
	if offender, unknown := unknownMember(members, claimNextRequestMembers); unknown {
		s.failUnknownMember(w, r, offender, claimNextRequestMembers)
		return "", false
	}
	return s.bodyActor(w, r, members)
}

// failClaimNext answers a failed claim.
//
// IT HAS NO 409 AND NO 404 ARM, and both absences are the operation's contract
// rather than an omission. There is no id to have missed, and a row another
// agent took is simply not in the set this claim scanned — the role walks past
// it. The whole refusal vocabulary beyond the edge is the role's ErrValidation,
// which reaches the wire as the 400 the document promises.
//
// The role's own validation is defensively unreachable: an empty actor is
// refused at the edge, and the two fields it refuses beyond that — Limit and
// Offset — have no wire spelling this handler will fill. It is mapped anyway,
// because the alternative if that stops being true is a 500 for a request the
// caller could have fixed.
func (s *Server) failClaimNext(w http.ResponseWriter, r *http.Request, err error) {
	if !errors.Is(err, issueops.ErrValidation) {
		s.failErr(w, r, err)
		return
	}
	s.event("request_refused", "request_id", requestInfo(r.Context()).id, "error", err.Error())
	s.fail(w, r, InvalidArgument("", ReasonInvalidValue,
		"the request was refused by this workspace's own validation; nothing was written"))
}
