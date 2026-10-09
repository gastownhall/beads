package httpclient

import (
	"context"
	"net/http"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/issueops"
)

// httpLeaseReclaimer is issueops.LeaseReclaimer over reclaimIssues (POST
// /v0/beads/issues:reclaim).
//
// THE MAPPING IS TOTAL: every member of ReclaimRequest has a wire member, so
// nothing here refuses on shape and the ledger carries no row for this
// operation. The grace window travels as fractional seconds, which holds any
// duration a reaper uses exactly.
//
// THE REQUEST RULES ARE CHECKED BEFORE THE DIAL, in the order
// internal/storage/issueops.ValidateReclaimRequest applies them, for
// checkGetManyIDs' reason: the server maps every role validation onto one
// generic invalid_value problem, so a round trip could never hand this client
// back the exact *issueops.TooManyReclaimIDsError{Requested, Cap} the role's
// contract promises. The rules are restated here rather than imported, because
// that package is the storage engine's shared body and does not belong in a
// transport client.
//
// AN EMPTY SCOPE SLICE IS NOT SENT. The server refuses a PRESENT, empty scope
// member (it would read as "no scope", the widening hazard bd reclaim refuses
// on the CLI), and on the role an empty slice and a nil one are the same
// request: no scope. Omitting it says exactly that.
type httpLeaseReclaimer struct{ store *Store }

var _ issueops.LeaseReclaimer = (*httpLeaseReclaimer)(nil)

// Reclaim dials POST /v0/beads/issues:reclaim.
func (l *httpLeaseReclaimer) Reclaim(ctx context.Context, req issueops.ReclaimRequest) (issueops.ReclaimResult, error) {
	if err := validateReclaimRequest(req); err != nil {
		return issueops.ReclaimResult{}, err
	}

	body := apigen.ReclaimIssuesRequest{Actor: req.Actor}
	if req.OlderThan > 0 {
		seconds := req.OlderThan.Seconds()
		body.OlderThanSeconds = &seconds
	}
	scope := func(values []string) *[]string {
		if len(values) == 0 {
			return nil
		}
		// COPIED, never aliased: the role promises never to write through a
		// caller's request.
		copied := append([]string(nil), values...)
		return &copied
	}
	body.Ids = scope(req.Filter.IDs)
	body.Assignees = scope(req.Filter.Assignees)
	body.Labels = scope(req.Filter.Labels)
	body.LabelsAny = scope(req.Filter.LabelsAny)
	body.ExcludeLabels = scope(req.Filter.ExcludeLabels)
	if req.Filter.AnyReplica {
		anyReplica := true
		body.AnyReplica = &anyReplica
	}

	var out apigen.ReclaimIssuesResult
	if err := l.store.dispatch(ctx, wire.Request{
		Op:     wire.OpReclaimIssues,
		Method: http.MethodPost,
		Path:   wire.PathIssuesReclaim,
		Body:   body,
	}, &out); err != nil {
		return issueops.ReclaimResult{}, err
	}

	reclaimed := make([]issueops.ReclaimedLease, 0, len(out.Reclaimed))
	for _, entry := range out.Reclaimed {
		// The revision is carried as the wire's decimal string, the role's own
		// spelling; it is read here only to refuse an answer whose token no
		// guarded write could use.
		if _, err := parseRevision("LeaseReclaimer.Reclaim", entry.Revision); err != nil {
			return issueops.ReclaimResult{}, err
		}
		reclaimed = append(reclaimed, entry)
	}
	return issueops.ReclaimResult{Reclaimed: reclaimed}, nil
}

// validateReclaimRequest restates the role's refusals, in the role's order:
// the actor, the grace window, the id cap (counted as sent), then the first
// blank id.
func validateReclaimRequest(req issueops.ReclaimRequest) error {
	if strings.TrimSpace(req.Actor) == "" {
		return invalid("reclaim actor is required")
	}
	if req.OlderThan < 0 {
		return invalid("reclaim older_than must not be negative")
	}
	if len(req.Filter.IDs) > issueops.MaxReclaimIDs {
		return &issueops.TooManyReclaimIDsError{Requested: len(req.Filter.IDs), Cap: issueops.MaxReclaimIDs}
	}
	for i, id := range req.Filter.IDs {
		if id == "" {
			return invalid("reclaim scope id at position %d is empty", i)
		}
	}
	return nil
}
