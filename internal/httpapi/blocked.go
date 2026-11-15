package httpapi

import (
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// handleListBlocked answers GET /v0/beads/blocked: what `bd blocked` prints,
// from the same role (issueops.BlockedLister).
//
// It decodes parameters and nothing else. The role normalizes the labels and
// owns the set, the narrowing and the order, so this handler cannot answer a
// different listing from the local command for the same request.
func (s *Server) handleListBlocked(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())
	req := blockedFilters(q)
	if !s.acceptQuery(w, r, q) {
		return
	}

	lister, err := s.blockedLister(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := lister.ListBlocked(r.Context(), req)
	if err != nil {
		s.failReadErr(w, r, err)
		return
	}
	writeJSON(w, apigen.BlockedPage{
		Items: wireBlocked(result.Items),
		// Always false: this operation takes no limit, so nothing truncates
		// the listing. Emitted because the document types it as required.
		HasMore: false,
	})
}

// blockedFilters decodes the blocked listing's request: `bd blocked`'s four
// flags, one parameter each. It is a function of its own so a test can drive
// it over an empty query and read back the exact set of names it asks for.
func blockedFilters(q *query) issueops.BlockedRequest {
	return issueops.BlockedRequest{
		ParentID:      q.str("parent"),
		Labels:        q.list("label"),
		LabelsAny:     q.list("label_any"),
		ExcludeLabels: q.list("exclude_label"),
	}
}

// wireBlocked projects the role's rows onto the generated envelope's element
// type, an alias of the same struct. The document says `items` is an empty
// array and never null, and a row's `blocked_by` is never null either; both
// are made true here as well as by the role, so the wire's guarantee does not
// rest on every implementation of it.
func wireBlocked(items []*types.BlockedIssue) []apigen.BlockedIssue {
	out := make([]apigen.BlockedIssue, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		row := *item
		if row.BlockedBy == nil {
			row.BlockedBy = []string{}
		}
		out = append(out, row)
	}
	return out
}
