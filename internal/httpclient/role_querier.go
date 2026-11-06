// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/role_querier.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"fmt"
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/issueops"
)

// httpQuerier is issueops.Querier over queryIssues (design D8 row 5).
//
// The expression is not parsed here and must not be: parsing, the predicate
// decision and the scan bound all live inside the server's role, which is what
// stops this client from shipping the truncating window both front doors used
// to apply. Five parameters go out and a page comes back.
//
// It is post-baseline, so the dispatch pre-flights: an older server that does
// not route this path answers a bare 404 that is indistinguishable from an
// entity's not_found, and consulting the advertised `issues.query` token before
// dialing is the only way to tell "this server is too old" from "nothing
// matched".
type httpQuerier struct{ store *Store }

func (q httpQuerier) Query(ctx context.Context, req issueops.QueryRequest) (issueops.IssuePage, error) {
	if err := validateQueryOffset(req); err != nil {
		return issueops.IssuePage{}, err
	}
	params, err := encode.QueryParams(req)
	if err != nil {
		return issueops.IssuePage{}, q.store.inexpressible("Querier.Query", err)
	}
	// An explicit Limit of 0 is bounded at the client cap rather than sent as
	// `limit=0`, which a non-loopback server refuses (unlimited_read.go).
	readCap, err := boundUnlimitedPage(params, req.Limit != nil && *req.Limit == 0)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	var body apigen.QueryPage
	if err := q.store.dispatch(ctx, wire.Request{
		Op:     wire.OpQueryIssues,
		Method: http.MethodGet,
		Path:   wire.PathIssuesQuery,
		Query:  params,
	}, &body); err != nil {
		return issueops.IssuePage{}, err
	}
	if err := checkUnlimitedPage(wire.OpQueryIssues, readCap, len(body.Items), body.HasMore); err != nil {
		return issueops.IssuePage{}, err
	}
	// NEVER projected: issueops.QueryRequest has no Brief member, so this role
	// cannot ask for one and its rows are always whole. The `false` is the
	// compiler asking the question rather than a value to tune — if the query
	// vocabulary ever grows the projection, this is the line that has to move
	// with it.
	return issueops.IssuePage{Items: wireRows(body.Items, false), HasMore: body.HasMore}, nil
}

// validateQueryOffset answers the two Offset refusals the ROLE owns, ahead of
// the one the WIRE owns.
//
// The distinction is the whole reason this function exists. A negative Offset,
// and an Offset combined with a display order, are ErrValidation at EVERY
// backend — the leaf says so in as many words, because under a page bound each
// page is sorted for itself and a walk with an offset would neither visit every
// row nor visit any row once. Those are the caller's request being wrong, and a
// caller must not be told to upgrade a server over them. Only what is left —
// a plain positive Offset — is this backend's own inability, and the encoder
// refuses that one with *ErrUnsupported (E-QueryRequest.Offset).
func validateQueryOffset(req issueops.QueryRequest) error {
	switch {
	case req.Offset < 0:
		return fmt.Errorf("%w: offset %d is negative", issueops.ErrValidation, req.Offset)
	case req.Offset > 0 && req.SortBy != "":
		return fmt.Errorf("%w: an offset cannot be combined with a display order (sort %q): "+
			"the order is applied to the rows the query bounded, so each page would be sorted for itself",
			issueops.ErrValidation, req.SortBy)
	}
	return nil
}
