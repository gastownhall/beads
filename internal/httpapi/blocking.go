package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// handleBlockingAnnotations answers GET /v0/beads/dependencies/blocking.
//
// Everything below the wire is on issueops.BlockingAnnotator: which edge types
// count, which row's status decides an edge is live, that an unresolvable
// blocker still blocks, that a `parent-child` edge is the parent rather than a
// blocker, the order within each list and the de-duplication of a repeated id.
// What stays here is transport — decoding one repeatable parameter, the size
// bound, and an envelope whose element type is the role's own struct.
//
// The size bound is maxDependencyAnchors, shared with the stored-edge read
// beside it rather than given a constant of its own: the two operations bound
// the same thing for the same reason (the QUESTION, because neither answer has
// a cursor), they are asked about the same page of ids, and two numbers would
// mean a client could ask one of them about a page the other refuses.
func (s *Server) handleBlockingAnnotations(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())

	ids := q.list("issue_id")
	if !s.acceptQuery(w, r, q) {
		return
	}
	// Both bounds are checked here rather than left to the role, for the reason
	// handleListDependencies gives: they are this operation's own limits, not
	// statements about what a blocking annotation means, and the role answers an
	// empty request with an empty result rather than a refusal.
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

	annotator, err := s.blockingAnnotator(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := annotator.AnnotateBlocking(r.Context(), issueops.BlockingRequest{IDs: ids})
	if err != nil {
		s.failBlockingErr(w, r, err)
		return
	}
	writeJSON(w, wireBlocking(result))
}

// failBlockingErr answers a failed blocking annotation. The role's one
// request-validation refusal — an empty id — is the caller's own input and
// reaches the client as the 400 it is; everything else keeps going through the
// one mapping in problem.go. It classifies on the SENTINEL rather than on
// prose, as failEdgeReadErr does.
func (s *Server) failBlockingErr(w http.ResponseWriter, r *http.Request, err error) {
	if !errors.Is(err, issueops.ErrValidation) {
		s.failErr(w, r, err)
		return
	}
	requestInfo(r.Context()).refuse("issue_id")
	s.fail(w, r, InvalidArgument("issue_id", ReasonInvalidValue, err.Error()))
}

// wireBlocking projects the role's answer onto the generated envelope, whose
// element type is an ALIAS of the role's own struct — so this is a slice copy
// and nothing else. There is no second wire struct here and there must never be
// one.
//
// It exists for the one thing that is not free: the document says `items` is an
// empty array and never null, so the wire promise does not depend on the role
// keeping its own.
func wireBlocking(result issueops.BlockingResult) apigen.BlockingAnnotations {
	if result.Items == nil {
		return apigen.BlockingAnnotations{Items: []apigen.IssueBlocking{}}
	}
	return apigen.BlockingAnnotations{Items: result.Items}
}
