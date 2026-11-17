// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/role_readycounter.go@49d1df2f6)
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

// httpReadyCounter is issueops.ReadyCounter over countReadyWork (design D8
// row 2). It is what serves `bd ready`'s "showing 2 of 5".
//
// The request it sends is the LISTING's request with the page taken off, which
// is what makes the answer the size of the page Reader.Ready would return: the
// encoder's ready-count table shares one filter list with the listing's, so a
// parameter one of them sent and the other did not cannot exist.
type httpReadyCounter struct{ store *Store }

func (c httpReadyCounter) CountReady(ctx context.Context, req issueops.ReadyRequest) (issueops.ReadyCountResult, error) {
	if err := validateCountReadyPage(req); err != nil {
		return issueops.ReadyCountResult{}, err
	}
	params, err := encode.ReadyCountParams(req)
	if err != nil {
		return issueops.ReadyCountResult{}, c.store.inexpressible("ReadyCounter.CountReady", err)
	}
	var body apigen.ReadyCount
	if err := c.store.dispatch(ctx, wire.Request{
		Op:     wire.OpCountReadyWork,
		Method: http.MethodGet,
		Path:   wire.PathReadyCount,
		Query:  params,
	}, &body); err != nil {
		return issueops.ReadyCountResult{}, err
	}
	return issueops.ReadyCountResult{Total: body.Total}, nil
}

// validateCountReadyPage answers the role's own page refusals before the
// encoder can answer them as wire refusals.
//
// A Limit and an Offset are ErrValidation on THIS ROLE at every backend, and
// for a reason that has nothing to do with the wire: a cardinality has no page.
// A Limit would answer "how many of the first N", which the identity with
// Reader.Ready(Limit=0) would then stop being true of, and an Offset would
// subtract the rows it skipped from the size of a set that still contains them.
// An explicit zero Limit is refused with the rest — an unlimited count is the
// only kind there is, so asking for one is asking for the default.
//
// The encoder refuses both fields too (E-ReadyRequest.Limit@countReadyWork,
// E-ReadyRequest.Offset), and that row is not redundant: it is what keeps the
// field from being silently dropped if this check were ever removed. What it
// cannot do is tell a caller their REQUEST is wrong rather than their server.
func validateCountReadyPage(req issueops.ReadyRequest) error {
	switch {
	case req.Limit != nil:
		return fmt.Errorf("%w: a ready count takes no limit (%d): a cardinality has no page, "+
			"and a bounded count would not agree with the listing it sizes", issueops.ErrValidation, *req.Limit)
	case req.Offset != 0:
		return fmt.Errorf("%w: a ready count takes no offset (%d): it would subtract the rows it skipped "+
			"from the size of a set that still contains them", issueops.ErrValidation, req.Offset)
	}
	return nil
}
