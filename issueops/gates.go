package issueops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// GateListExpression is the query `bd gate list` asks: every row of the gate
// type, on both planes, whatever its other fields. It names no status, so the
// Querier's default closed exclusion applies unless GateListRequest.All lifts
// it (QueryRequest.IncludeClosed).
const GateListExpression = "type=gate"

// GateListRequest describes `bd gate list` over the whole workspace, and the
// listing `bd gate check` evaluates.
type GateListRequest struct {
	// All admits closed gates, which the listing otherwise hides.
	All bool
	// ExcludeTemplates leaves template gates (a persisted proto's, which every
	// pour copies) out of the query itself, so none of them takes a row of
	// Limit. `bd gate list` leaves it unset and lists them; `bd gate check`,
	// which never evaluates a template, sets it.
	ExcludeTemplates bool
	// Limit bounds the rows returned; 0 means unlimited. A negative limit is
	// the Querier's ErrValidation.
	Limit int
}

// ListGates answers `bd gate list` (no issue argument) through the Querier
// role, which every backend serves (a remote one as queryIssues, which every
// deployed server publishes). It is the one library entry for the listing:
// the CLI's direct, proxied and remote routes each call it with their route's
// Querier, so no route builds a storage filter of its own.
//
// The question is exactly the one the raw filter used to ask: the gate type,
// both planes, closed rows excluded unless All is set, no other default
// suppression (templates, pinned rows and ephemeral gates are all listed),
// in the store's default order, cut at Limit. ExcludeTemplates adds
// `template=false` to the expression rather than dropping templates from the
// page, so the cut at Limit counts only the gates that remain.
//
// A failed read is an error, never an empty list. Rows come back in the
// Querier's order; a row carrying no issue is dropped.
func ListGates(ctx context.Context, querier Querier, req GateListRequest) ([]*types.Issue, error) {
	if querier == nil {
		return nil, &ErrUnsupported{Op: "ListGates", Backend: "nil"}
	}
	expression := GateListExpression
	if req.ExcludeTemplates {
		expression += " AND template=false"
	}
	limit := req.Limit
	page, err := querier.Query(ctx, QueryRequest{
		Expression:    expression,
		IncludeClosed: req.All,
		Limit:         &limit,
	})
	if err != nil {
		return nil, fmt.Errorf("listing gates: %w", err)
	}
	gates := make([]*types.Issue, 0, len(page.Items))
	for _, row := range page.Items {
		if row != nil && row.Issue != nil {
			gates = append(gates, row.Issue)
		}
	}
	return gates, nil
}

// The gate members a GateFieldError can name, spelled as the wire and the
// stored columns spell them.
const (
	GateFieldAwaitType = "await_type"
	GateFieldAwaitID   = "await_id"
	GateFieldTimeout   = "timeout"
	GateFieldWaiters   = "waiters"
)

// GateFieldError reports a gate member a create or an update refused, and the
// one member it is about, so a front door can name that member without parsing
// the message. It wraps ErrValidation.
//
// The widths are the stored columns' and nothing more, so every backend
// refuses the same values a Dolt column always did: await_type at most 32
// characters, await_id at most 255, and the JSON-encoded waiter list at most
// one TEXT column. The one rule no column enforces is a timeout that is not
// negative (timeout_ns is a BIGINT), which the wire publishes as minimum 0;
// the raw store creates do not run it yet (see ValidateGateFields). There is
// deliberately no await_type vocabulary (formula gate types are free-form), no
// gate-type-only rule (any issue may carry the members, as local updates
// always allowed), and no waiter deduplication.
type GateFieldError struct {
	// Field is one of the GateField* constants.
	Field string
	// Detail is the sentence the error reads as. It is prose, and the HTTP
	// client's copy ends with the server's own clause ("; nothing was
	// created", "; nothing was written"), so a caller dispatches on Field,
	// never on Detail.
	Detail string
}

func (e *GateFieldError) Error() string { return e.Detail }

// Unwrap makes GateFieldError match ErrValidation.
func (e *GateFieldError) Unwrap() error { return ErrValidation }
