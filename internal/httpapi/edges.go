package httpapi

import (
	"errors"
	"fmt"
	"net/http"

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
