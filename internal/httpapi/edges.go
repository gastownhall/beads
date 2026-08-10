package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// maxDependencyAnchors bounds how many issues one call may ask about.
//
// It is the operation's ONLY size bound and it is deliberately on the QUESTION
// rather than on the answer. `bd dep list` takes no limit, so a `limit`
// parameter here would make the two front doors answer differently by default;
// a cap on the answer with no cursor behind it would truncate with no way to
// fetch the rest. Bounding the anchors bounds the work without doing either.
const maxDependencyAnchors = 100

// handleListDependencies answers GET /v0/beads/dependencies.
//
// Everything below the wire is on issueops.EdgeReader: which plane each anchor
// is probed on, whether an absent anchor is a miss or an error, the type
// filter, the edge order, and the de-duplication of a repeated id. What stays
// here is transport — two repeatable parameters, the size bound, and flattening
// the role's per-anchor answer onto the flat array `bd dep list --json` emits.
func (s *Server) handleListDependencies(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())

	ids := q.list("issue_id")
	req := issueops.EdgeReadRequest{IDs: ids}
	for _, depType := range q.list("type") {
		req.Types = append(req.Types, types.DependencyType(depType))
	}

	if !s.acceptQuery(w, r, q) {
		return
	}
	// Both bounds are checked here rather than left to the role: they are this
	// operation's own limits, not statements about what a stored-edge read
	// means, and the role answers an empty request with an empty result.
	if len(ids) == 0 {
		requestInfo(r.Context()).refuse("issue_id")
		s.fail(w, r, InvalidArgument("issue_id", ReasonInvalidValue, "name at least one issue_id"))
		return
	}
	if len(ids) > maxDependencyAnchors {
		requestInfo(r.Context()).refuse("issue_id")
		s.fail(w, r, InvalidArgument("issue_id", ReasonInvalidValue,
			fmt.Sprintf("at most %d issue_id values per request, got %d", maxDependencyAnchors, len(ids))))
		return
	}

	rd, err := s.edgeReader(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := rd.ReadEdges(r.Context(), req)
	if err != nil {
		s.failEdgeReadErr(w, r, err, ids)
		return
	}
	writeJSON(w, wireEdges(result))
}

// handleCountDependencyEdges answers GET /v0/beads/dependencies:count.
//
// Everything below the wire is on issueops.GraphCounter: which plane each
// anchor is probed on, that an absent anchor is a per-anchor miss rather than
// an error, that the count spans both dependency planes as a sum, the type and
// status filters, and the de-duplication of a repeated id. What stays here is
// transport — three repeatable-or-single parameters, the anchor bound, and
// projecting the role's answer onto the wire envelope.
//
// The DIRECTION is not validated here, deliberately. It is the role's closed
// vocabulary, and issueops.GraphCounter has ONE body on all three legs with
// ValidateEdgeCountRequest inside it, so refusing at the edge would be a second
// definition of the same rule — the opposite of GET /v0/beads/issues:count,
// where the handler owns its enum precisely because no role refusal is
// reachable there. Here four of them are, and failEdgeCountErr names the
// parameter for each.
func (s *Server) handleCountDependencyEdges(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())

	ids := q.list("issue_id")
	req := issueops.EdgeCountRequest{
		IDs:       ids,
		Direction: issueops.EdgeDirection(q.str("direction")),
		Status:    q.str("status"),
	}
	for _, depType := range q.list("type") {
		req.Types = append(req.Types, types.DependencyType(depType))
	}

	if !s.acceptQuery(w, r, q) {
		return
	}
	// The anchor bound is this operation's own limit rather than a statement
	// about what an edge count means, exactly as the stored-edge read's is —
	// and it is the SAME bound, from the same constant, because the two
	// operations bound the same thing on the same collection.
	if len(ids) == 0 {
		requestInfo(r.Context()).refuse("issue_id")
		s.fail(w, r, InvalidArgument("issue_id", ReasonInvalidValue, "name at least one issue_id"))
		return
	}
	if len(ids) > maxDependencyAnchors {
		requestInfo(r.Context()).refuse("issue_id")
		s.fail(w, r, InvalidArgument("issue_id", ReasonInvalidValue,
			fmt.Sprintf("at most %d issue_id values per request, got %d", maxDependencyAnchors, len(ids))))
		return
	}

	counter, err := s.graphCounter(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := counter.CountEdges(r.Context(), req)
	if err != nil {
		s.failEdgeCountErr(w, r, err, req)
		return
	}
	writeJSON(w, wireEdgeCounts(result))
}

// failEdgeCountErr answers a failed edge count. The role's four
// request-validation refusals are the caller's own input and reach the client
// as the 400 they are; everything else keeps going through the one mapping in
// problem.go.
//
// It classifies on the SENTINEL, as failEdgeReadErr does, and picks the
// parameter to name by re-asking the request the validator's own questions IN
// THE VALIDATOR'S OWN ORDER. That order is part of ValidateEdgeCountRequest's
// contract — the direction is checked FIRST so an empty request is a refusal
// about the direction rather than an empty answer — and a picker that asked in
// a different order would name a second offender on a request carrying two,
// sending the caller to fix a parameter the server had not reached.
func (s *Server) failEdgeCountErr(w http.ResponseWriter, r *http.Request, err error, req issueops.EdgeCountRequest) {
	if !errors.Is(err, issueops.ErrValidation) {
		s.failErr(w, r, err)
		return
	}
	param := "type"
	switch {
	case req.Direction != issueops.EdgeDirectionOut && req.Direction != issueops.EdgeDirectionIn:
		param = "direction"
	case req.Status != "" && req.Direction != issueops.EdgeDirectionIn:
		param = "status"
	case slices.Contains(req.IDs, ""):
		param = "issue_id"
	}
	requestInfo(r.Context()).refuse(param)
	s.fail(w, r, InvalidArgument(param, ReasonInvalidValue, err.Error()))
}

// wireEdgeCounts projects the role's per-anchor answer onto the wire envelope.
//
// It does NOT flatten, which is the difference from wireEdges beside it. An
// edge row carries its own issue_id, so a flat array of rows loses nothing; a
// number does not, so the per-anchor shape is the answer and folding it would
// produce a total nobody asked for.
//
// `anchors` is non-nil so the body carries `[]` rather than `null`, and both
// `count` and `missing` are emitted on every entry — 0 is the common answer
// and `false` is a fact, so an omitted member on either would be ambiguous.
func wireEdgeCounts(result issueops.EdgeCountResult) apigen.EdgeCounts {
	body := apigen.EdgeCounts{Anchors: []apigen.AnchorEdgeCount{}}
	for _, anchor := range result.Anchors {
		body.Anchors = append(body.Anchors, apigen.AnchorEdgeCount{
			Id:      anchor.ID,
			Count:   anchor.Count,
			Missing: anchor.Missing,
		})
	}
	return body
}

// failEdgeReadErr answers a failed stored-edge read. The role's two
// request-validation refusals — an empty id, an unusable dependency type — are
// the caller's own input and reach the client as the 400 they are; everything
// else keeps going through the one mapping in problem.go.
//
// It classifies on the SENTINEL rather than on prose, unlike the read handlers'
// invalidFilterParam: this role publishes issueops.ErrValidation for exactly
// those two cases. Which parameter is named comes from the request the handler
// still holds — an empty entry can only have come from `issue_id`.
func (s *Server) failEdgeReadErr(w http.ResponseWriter, r *http.Request, err error, ids []string) {
	if !errors.Is(err, issueops.ErrValidation) {
		s.failErr(w, r, err)
		return
	}
	param := "type"
	for _, id := range ids {
		if id == "" {
			param = "issue_id"
			break
		}
	}
	requestInfo(r.Context()).refuse(param)
	s.fail(w, r, InvalidArgument(param, ReasonInvalidValue, err.Error()))
}

// wireEdges flattens the role's per-anchor answer onto the wire envelope.
//
// The element type is an ALIAS of types.Dependency — the same struct the CLI's
// --json marshals. There is no second wire struct here and there must never be
// one.
//
// Both members are non-nil so the body carries `[]` rather than `null`.
func wireEdges(result issueops.EdgeReadResult) apigen.DependencyEdges {
	body := apigen.DependencyEdges{
		Items:   []apigen.Dependency{},
		Missing: []string{},
	}
	for _, anchor := range result.Anchors {
		if anchor.Missing {
			body.Missing = append(body.Missing, anchor.ID)
			continue
		}
		for _, edge := range anchor.Edges {
			if edge == nil {
				continue
			}
			body.Items = append(body.Items, *edge)
		}
	}
	return body
}
