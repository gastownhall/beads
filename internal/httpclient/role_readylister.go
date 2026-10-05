// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/role_readylister.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// httpReadyLister is issueops.ReadyLister over the same two operations the
// ready bridge's GetReadyWorkWithCountsAndTotal dials: listReadyWork for the
// page and, only when the page does not already settle it, countReadyWork for
// the total. It is what serves `bd ready`'s listing and its "Showing X of N".
//
// AT MOST TWO REQUESTS, and usually one. listReadyWork's ReadyPage carries
// has_more but no total, so a page that the server says is the tail of the set
// sizes the set itself — Offset plus the rows it carried — and only a truncated
// page (or an empty page past an Offset, which proves nothing about the size)
// pays the count. The count is encoded from the SAME request with its page taken
// off, through the encoder's shared filter list, so the two requests ask one
// question.
//
// The two requests are two server transactions, not one snapshot — the role's
// one-snapshot promise is a promise this wire cannot keep for a truncated page,
// exactly as the bridge documents — so the total is clamped from below to
// Offset+len(Items): rows the listing returned exist.
type httpReadyLister struct{ store *Store }

func (l httpReadyLister) ListReady(ctx context.Context, req issueops.ReadyListRequest) (issueops.ReadyListing, error) {
	params, err := encode.ReadyParams(req.ReadyRequest)
	if err != nil {
		return issueops.ReadyListing{}, l.store.inexpressible("ReadyLister.ListReady", err)
	}
	// Encoded before either request so a filter the count cannot carry refuses
	// before the listing is dialed, the ordering the bridge keeps too.
	countReq := req.ReadyRequest
	countReq.Limit = nil
	countReq.Offset = 0
	countParams, err := encode.ReadyCountParams(countReq)
	if err != nil {
		return issueops.ReadyListing{}, l.store.inexpressible("ReadyLister.ListReady", err)
	}

	var page apigen.ReadyPage
	if err := l.store.dispatch(ctx, wire.Request{
		Op:     wire.OpListReadyWork,
		Method: http.MethodGet,
		Path:   wire.PathReady,
		Query:  params,
	}, &page); err != nil {
		return issueops.ReadyListing{}, err
	}
	rows := wireRows(page.Items, req.Brief)
	if req.MaxRows > 0 && len(rows) > req.MaxRows {
		return issueops.ReadyListing{}, &storageops.ErrTooManyRows{
			Found:  len(rows),
			Cap:    req.MaxRows,
			Source: req.MaxRowsSource,
		}
	}

	floor := int64(req.Offset + len(rows))
	total := floor
	if page.HasMore || (req.Offset > 0 && len(rows) == 0) {
		var count apigen.ReadyCount
		if err := l.store.dispatch(ctx, wire.Request{
			Op:     wire.OpCountReadyWork,
			Method: http.MethodGet,
			Path:   wire.PathReadyCount,
			Query:  countParams,
		}, &count); err != nil {
			return issueops.ReadyListing{}, err
		}
		total = max(count.Total, floor)
	}
	return issueops.ReadyListing{Items: rows, HasMore: page.HasMore, Total: total}, nil
}
