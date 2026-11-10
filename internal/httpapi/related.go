package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// handleListRelatedIssues answers GET /v0/beads/issues/{id}/related.
//
// Everything below the wire is on issueops.Relations: which planes the anchor is
// resolved against, that an anchor naming nothing is ErrNotFound rather than an
// empty page, which dependency tables the neighbors are collected from, that an
// edge whose far end this database holds no row for is silently not a neighbor,
// the type filter, and the pinned order. What stays here is transport — the path
// id's bound, two parameters, and the wire envelope.
//
// THE DIRECTION IS NOT VALIDATED HERE, deliberately, and it is the same call
// GET /v0/beads/dependencies:count made. It is the role's closed vocabulary and
// ValidateRelatedRequest refuses the zero value outright, so every accessor
// raises it and refusing at the edge would be a second definition of one rule.
// The opposite choice belongs where NO role refusal is reachable — the
// issues:count group_by — and this is not that case.
func (s *Server) handleListRelatedIssues(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())

	req := issueops.RelatedRequest{Direction: issueops.RelationDirection(q.str("direction"))}
	for _, depType := range q.list("type") {
		req.Types = append(req.Types, types.DependencyType(depType))
	}

	// Before the id bound, which is handleGetIssue's order and for its reason: a
	// refused query string is a 400 that names what to fix, and deciding the id
	// first would answer it with a 404 instead.
	if !s.acceptQuery(w, r, q) {
		return
	}
	id := r.PathValue("id")

	// The id is bounded HERE, before the request buys a concurrency slot and a
	// database round trip, exactly as handleGetIssue bounds its own. The column
	// is VARCHAR(255) and the document calls this an exact canonical id, so a
	// longer one — or one carrying a control character, which a percent-escape
	// in the path decodes to — names no row that can exist. The refusal is the
	// SAME 404 a real miss gets: a distinct answer would let a caller map this
	// server's notion of a well-formed id, and there is nothing to learn from it.
	//
	// It is also what makes ValidateRelatedRequest's empty-id refusal unreachable
	// over this wire, which failRelatedErr relies on when it picks a parameter.
	if id == "" || types.CheckFieldLen("id", id) != nil || strings.ContainsFunc(id, isControlChar) {
		s.fail(w, r, NotFound())
		return
	}
	req.ID = id

	rel, err := s.relations(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	items, err := rel.Related(r.Context(), req)
	if err != nil {
		s.failRelatedErr(w, r, err, req)
		return
	}
	writeJSON(w, apigen.RelatedIssues{Items: wireRelated(items)})
}

// failRelatedErr answers a failed neighbor read. The role's
// request-validation refusals are the caller's own input and reach the client
// as the 400 they are; everything else keeps going through the one mapping in
// problem.go — the absent anchor included.
//
// THAT LAST CLAUSE IS THE ONE WORTH READING, because this operation's 404 is
// half its contract and there is no arm here that produces it. ClassifyError
// already carries a storage.ErrNotFound row, so the role's miss reaches the wire
// as a 404 through the shared mapping, and an arm for it here would be a second
// spelling of one rule. It was written, mutated out and watched to stay green
// on every case in this package — including the one named for the miss — which
// is what says it was redundant rather than untested.
//
// The arm that is NOT redundant is the one below: neither storage leg wraps
// ErrValidation in anything the shared mapping recognizes, so deleting it turns
// every refusal on this operation into a generic 500. Mutation-verified the
// same way, in the opposite direction.
//
// The 400 names `direction` or `type` rather than always naming the same
// parameter, because `param` is what a client dispatches on and the two have
// different recoveries. Which one comes from the REQUEST the handler still
// holds, asked in ValidateRelatedRequest's own order: only a value outside the
// closed set can have produced a direction refusal, and the id — which that
// validator checks FIRST — cannot have produced one at all, because the path
// bound above already turned an unusable id into a 404.
func (s *Server) failRelatedErr(w http.ResponseWriter, r *http.Request, err error, req issueops.RelatedRequest) {
	if !errors.Is(err, issueops.ErrValidation) {
		s.failErr(w, r, err)
		return
	}
	param := "type"
	if req.Direction != issueops.RelationOut && req.Direction != issueops.RelationIn {
		param = "direction"
	}
	requestInfo(r.Context()).refuse(param)
	s.fail(w, r, InvalidArgument(param, ReasonInvalidValue, err.Error()))
}

// wireRelated projects the role's neighbor list onto the generated envelope's
// element type, which is an ALIAS of types.IssueWithDependencyMetadata — the
// same struct `bd show --json` marshals under `dependencies` and `dependents` —
// so this dereferences pointers and does nothing else. There is no second wire
// struct here and there must never be one.
//
// It exists for the one thing that is not free: the document says `items` is an
// empty array and never null. Making that guarantee here means the wire promise
// does not depend on the role keeping its own.
func wireRelated(items []*issueops.RelatedIssue) []apigen.IssueWithDependencyMetadata {
	out := make([]apigen.IssueWithDependencyMetadata, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, *item)
	}
	return out
}
