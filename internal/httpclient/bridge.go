// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/bridge.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The off-role ready bridge, and the raw issue read the `--parent` walk probes
// with.
//
// The bridge exists because `bd ready`'s listing is still raw at tip
// (cmd/bd/ready.go:172 and :211): the front door has not moved onto
// issueops.Reader, so a reverse types.WorkFilter mapper is required until it
// does. D8 calls it deliberately throwaway and it is — what is not throwaway is
// the rule it holds to: every field it cannot express REFUSES, because a dropped
// filter widens a result set invisibly and the server can only reject the
// parameters it receives (L12).
//
// The SEARCH bridge is not here. Its two shapes are id resolution's (resolve.go)
// and the descendant walk's, and both live beside the surfaces that own them.

// GetReadyWork serves `bd ready`'s text listing.
//
// It is the projection and GetReadyWorkWithCounts is the primitive, not the
// other way round: the wire hydrates the three cardinalities either way, because
// the page element IS types.IssueWithCounts.
func (s *Store) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	rows, err := s.GetReadyWorkWithCounts(ctx, filter)
	if err != nil {
		return nil, err
	}
	out := make([]*types.Issue, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Issue)
	}
	return out, nil
}

// GetReadyWorkWithCounts is the same listing with the counts `bd ready --json`
// prints.
//
// ONE REQUEST, NO PAGING. listReadyWork publishes no cursor — its sort policies
// admit no keyset predicate — so `limit` and the server's own has_more ARE the
// page contract, exactly as they are for the Reader.Ready role this bridge dies
// in favor of. The request is built by the same encoder and dialed through the
// same door, so the two spellings of "list ready work" cannot drift apart.
func (s *Store) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	rows, _, err := s.readyBridgePage(ctx, "GetReadyWorkWithCounts", filter)
	return rows, err
}

// GetReadyWorkWithCountsAndTotal is GetReadyWorkWithCounts plus the size of the
// whole ready set, which `bd ready --json` prints as its pagination total.
//
// AT MOST TWO REQUESTS. listReadyWork's ReadyPage carries items and has_more
// but no total, so the total cannot ride the page the way it does in a SQL
// backend's single transaction. When the server says the page is the whole set
// (has_more false — always the case for an unlimited listing) the total IS the
// page length and no second request is made. Only a truncated page pays one
// countReadyWork round trip, encoded from the same filter by
// encode.ReadyBridgeCountParams, which is the round trip the text path's
// ReadyCounter role already makes.
//
// The two requests are two server transactions, not one snapshot: a write
// landing between them can make the total disagree with the page by that
// write. The total is clamped to at least the page length so the envelope
// never reports fewer items than it carries.
//
// Every refusal the listing makes is made before either request (the count
// encoder runs the listing's encoder), so a filter the wire cannot express —
// ExcludeIDs included — fails here exactly as it fails GetReadyWorkWithCounts.
func (s *Store) GetReadyWorkWithCountsAndTotal(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, int, error) {
	const op = "GetReadyWorkWithCountsAndTotal"
	countParams, err := encode.ReadyBridgeCountParams(filter)
	if err != nil {
		return nil, 0, s.inexpressible(op, err)
	}
	rows, hasMore, err := s.readyBridgePage(ctx, op, filter)
	if err != nil {
		return nil, 0, err
	}
	if !hasMore {
		return rows, len(rows), nil
	}
	var body apigen.ReadyCount
	if err := s.dispatch(ctx, wire.Request{
		Op:     wire.OpCountReadyWork,
		Method: http.MethodGet,
		Path:   wire.PathReadyCount,
		Query:  countParams,
	}, &body); err != nil {
		return nil, 0, err
	}
	total := int(body.Total)
	if total < len(rows) {
		total = len(rows)
	}
	return rows, total, nil
}

// readyBridgePage is the one listReadyWork request both bridge listings make,
// returning the rows and the server's has_more.
func (s *Store) readyBridgePage(ctx context.Context, op string, filter types.WorkFilter) ([]*types.IssueWithCounts, bool, error) {
	params, err := encode.ReadyBridgeParams(filter)
	if err != nil {
		return nil, false, s.inexpressible(op, err)
	}
	var body apigen.ReadyPage
	if err := s.dispatch(ctx, wire.Request{
		Op:     wire.OpListReadyWork,
		Method: http.MethodGet,
		Path:   wire.PathReady,
		Query:  params,
	}, &body); err != nil {
		return nil, false, err
	}
	// filter.Lite IS ReadyRequest.Brief — internal/workapi assigns one to the
	// other — so the two doors have to stamp the same marker or the same request
	// answers differently depending on which one it came in. That agreement is
	// the whole of E-WorkFilter.Lite's retirement note.
	rows := wireRows(body.Items, filter.Lite)
	// The cap bounds the rows this client accepted off the wire, which for a
	// cursorless operation is one page. It still fires: a caller whose page is
	// wider than their cap gets exit 2 rather than a silently oversized answer.
	if filter.MaxRows > 0 && len(rows) > filter.MaxRows {
		return nil, false, &storageops.ErrTooManyRows{
			Found:  len(rows),
			Cap:    filter.MaxRows,
			Source: filter.MaxRowsSource,
		}
	}
	return rows, body.HasMore, nil
}

// GetIssue serves getIssue. It is the `bd list --parent` walk's
// parent-existence probe and the molecule loader's read half.
//
// A MISS IS (nil, nil), NOT AN ERROR. That is the raw method's own contract and
// the walk depends on it: getHierarchicalChildren reads a nil result as "parent
// issue not found" and prints that, where an error would print the transport's
// vocabulary instead.
func (s *Store) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	if id == "" {
		return nil, nil
	}
	path, err := wire.IssuePath(id)
	if err != nil {
		return nil, err
	}
	// Neither row list: this is the existence probe and the loader's read, and
	// both discard everything but the row. Asking for dependents and comments
	// would buy two joins nobody reads.
	var details types.IssueDetails
	err = s.dispatch(ctx, wire.Request{
		Op:      wire.OpGetIssue,
		Method:  http.MethodGet,
		Path:    path,
		Query:   url.Values{},
		IssueID: id,
	}, &details)
	if errors.Is(err, issueops.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// The detail view carries labels in a field of its OWN rather than on the
	// embedded issue, and the local GetIssue hydrates them onto the issue.
	// Lifting them across is what makes the two answers the same value — the
	// same correction getIssuesByExactID makes for the resolver's fast path.
	issue := details.Issue
	if len(issue.Labels) == 0 && len(details.Labels) > 0 {
		issue.Labels = append([]string(nil), details.Labels...)
	}
	// Issue.RowVersion is json:"-", so the decode above left it at 0 and
	// Revision — getIssue's only wire spelling of the token — carries the real
	// value. A molecule loader (or any other caller of this read) that then
	// guards a write off issue.RowVersion must see the row's actual token, not
	// every row's as though none had ever been written.
	version, err := parseRevision("getIssue", details.Revision)
	if err != nil {
		return nil, err
	}
	issue.RowVersion = version
	return &issue, nil
}
