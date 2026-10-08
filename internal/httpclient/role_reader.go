// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/role_reader.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// httpReader is issueops.Reader over the v0 wire (design D8 row 1): Ready is
// listReadyWork, List is listIssues, Get is getIssue.
//
// It builds no filter and applies no default. Both request types are handed to
// the encoder whole, and the SERVER runs the same two workapi builders a local
// command would — against its own authoritative vocabulary, which is what heals
// L7's degraded client-side status/type set for every query that goes through
// here. What stays on this side is the part the wire genuinely cannot carry:
// the display order, the caller-supplied keyset position and the MaxRows cap,
// all three of them ledgered client-side in the encoder table and all three
// implemented in list_walk.go.
type httpReader struct{ store *Store }

// Ready serves listReadyWork.
//
// One request, no paging: the operation publishes no cursor, so `limit` and the
// server's own has_more ARE the page contract. An Offset is refused by the
// encoder rather than dropped — ready work carries no keyset position, so there
// is no honest way to page it (E-ReadyRequest.Offset).
func (r httpReader) Ready(ctx context.Context, req issueops.ReadyRequest) (issueops.IssuePage, error) {
	params, err := encode.ReadyParams(req)
	if err != nil {
		return issueops.IssuePage{}, r.store.inexpressible("Reader.Ready", err)
	}
	var body apigen.ReadyPage
	if err := r.store.dispatch(ctx, wire.Request{
		Op:     wire.OpListReadyWork,
		Method: http.MethodGet,
		Path:   wire.PathReady,
		Query:  params,
	}, &body); err != nil {
		return issueops.IssuePage{}, err
	}
	return issueops.IssuePage{Items: wireRows(body.Items, req.Brief), HasMore: body.HasMore}, nil
}

// List serves listIssues, through the pager in list_walk.go.
func (r httpReader) List(ctx context.Context, req issueops.ListRequest) (issueops.IssuePage, error) {
	params, err := encode.ListParams(req)
	if err != nil {
		return issueops.IssuePage{}, r.store.inexpressible("Reader.List", err)
	}
	return r.store.walkIssues(ctx, params, req)
}

// Get serves getIssue, including the two landed include parameters that retired
// ledger row L4.
//
// A 404 arrives as the `not_found` problem code, which the wire's problem
// mapper has already turned into issueops.ErrNotFound — the same sentinel a
// local miss answers with — so the role's "a miss is ErrNotFound, a backend
// failure passes through unchanged" promise holds without a single decision
// here. The nil detail view on any error is the other half of it.
func (r httpReader) Get(ctx context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	id, params := encode.GetTarget(req)
	path, err := wire.IssuePath(id)
	if err != nil {
		return nil, err
	}
	var details types.IssueDetails
	if err := r.store.dispatch(ctx, wire.Request{
		Op:      wire.OpGetIssue,
		Method:  http.MethodGet,
		Path:    path,
		Query:   params,
		IssueID: id,
	}, &details); err != nil {
		return nil, err
	}
	// Issue.RowVersion is json:"-"; the wire's only spelling of the token is
	// Revision (the decimal string getIssue publishes), so a bare decode
	// leaves RowVersion at its zero value on every read — a guarded write
	// built off this result would carry a token the row never held, same as
	// bd-enterprise's (*Store).GetIssue bridge below and the write
	// responses parseRevision already stitches this way.
	version, err := parseRevision("getIssue", details.Revision)
	if err != nil {
		return nil, err
	}
	details.Issue.RowVersion = version
	return &details, nil
}

// wireRows lifts a wire page onto the pointer slice the role's page carries,
// and stamps the projection marker the wire cannot carry.
//
// The element type is an ALIAS of types.IssueWithCounts — the same struct the
// server projects onto and the same one `bd list --json` marshals — so this
// takes an address and copies nothing under the header. The result is never
// nil: an empty page is an empty slice on every surface, and a caller must not
// have to tell null from empty to learn that nothing matched.
//
// brief IS A PARAMETER RATHER THAN A FIELD ON ANYTHING, so every page decode on
// this client has to answer the question. types.Issue.IsLitePartial is
// `json:"-"` — it never crosses — and a `brief` page therefore arrives
// byte-identical to a page of genuinely textless rows. The only thing that can
// tell them apart is having ASKED, which is what issueops.ListRequest.Brief's
// own leaf says a wire consumer distinguishes them by, and this client is the
// consumer that asked. A call site that could forget it would be a page whose
// rows lie about being whole; making it an argument means the compiler asks.
//
// IT IS NEVER STAMPED ON A HYDRATED READ. The marker means "the heavy columns
// were not selected", so setting it on a full page would send a caller to
// refetch a body it already has — the same defect as the missing stamp, pointed
// the other way.
func wireRows(items []apigen.IssueWithCounts, brief bool) []*types.IssueWithCounts {
	out := make([]*types.IssueWithCounts, 0, len(items))
	for i := range items {
		row := &items[i]
		if brief && row.Issue != nil {
			row.IsLitePartial = true
		}
		out = append(out, row)
	}
	return out
}
