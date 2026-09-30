package issueops

import "context"

// ReadyListRequest is one ready listing: the ready question plus the client's
// defensive row cap.
//
// It EMBEDS ReadyRequest rather than restating it, for the reason ReadyCounter
// takes ReadyRequest verbatim: a listing, its count and a claim over the same
// request ask ONE question of ONE set, and a parallel request type is how two
// of them would start disagreeing about the predicate.
type ReadyListRequest struct {
	ReadyRequest

	// MaxRows is the DEFENSIVE CAP ListRequest.MaxRows describes, with the
	// same meaning here: a listing that would materialize more rows than it
	// allows comes back as *internal/storage/issueops.ErrTooManyRows and no
	// listing, rather than as a truncated page. 0 disables it.
	//
	// It is a field of the LISTING, not of ReadyRequest, because it bounds the
	// rows this call materializes and nothing else: a claim consumes one row
	// however large the pool it scanned and a count materializes none, so
	// neither ReadyClaimer nor ReadyCounter has a cap to honor. It is a client
	// knob (`--max-rows`, BEADS_MAX_ROWS) describing this machine's patience,
	// not a property of the question.
	MaxRows int
	// MaxRowsSource attributes the cap in the refusal text; see
	// ListRequest.MaxRowsSource. It decides no answer.
	MaxRowsSource string
}

// ReadyListing is one page of ready work together with the size of the whole
// ready set it was cut from.
type ReadyListing struct {
	// Items is the page, in the ready policy's order. Never nil for a
	// successful call.
	Items []*IssueWithCounts
	// HasMore reports that the limit truncated the result: rows past the page
	// exist. It is false for an unlimited request.
	HasMore bool
	// Total is how many rows the request's ready predicate admits, ignoring
	// the page — the number `bd ready` publishes as "Showing X of N" and as
	// pagination.total. It is never smaller than Offset+len(Items).
	Total int64
}

// ReadyLister describes listing ready work: `bd ready`'s question, answered
// whole — the page AND the size of the set it was cut from — by one role with
// its own accessor. A new capability gets a new role interface and its own
// accessor; never append a method here.
//
// IT IS NOT Reader.Ready AND NOT ReadyCounter, and it does not replace either.
// Reader.Ready answers "a page, and did the limit hide anything"; ReadyCounter
// answers "how many". A listing that publishes a total needs both answers ABOUT
// THE SAME SET, and asking them as two calls is two reads — two round-trip
// sequences against a remote SQL server, two defer-wake sweeps, and a write
// landing between them can leave the total disagreeing with the page. This
// role is where an implementation that CAN answer both in one pass does so:
// the store-backed body takes the total from the page's own statements
// (a window count over the ready predicate), so `bd ready --limit 1` costs no
// more statements than the page itself.
//
// THE IDENTITIES, for every request r this method accepts:
//
//	ListReady(r).Items   == Reader.Ready(r.ReadyRequest).Items
//	ListReady(r).HasMore == Reader.Ready(r.ReadyRequest).HasMore
//	ListReady(r).Total   == ReadyCounter.CountReady(r.ReadyRequest with Limit and Offset unset).Total
//
// when nothing writes between the calls being compared. MaxRows is the one
// field with no counterpart on the other two roles: a request its cap refuses
// has no listing to compare.
//
// ONE SNAPSHOT IS PROMISED FOR THE PAGE AND THE TOTAL on every implementation:
// the store-backed body reads both in one transaction and the unit-of-work body
// reads both inside one read-only unit of work. Total is also clamped from
// below to Offset+len(Items) — rows the listing itself returned exist — so a
// caller printing "Showing X of N" never prints an N smaller than X.
//
// Offset, ExcludeIDs, Brief and every other ReadyRequest field mean exactly
// what they mean to Reader.Ready (see ReadyRequest). Deterministic
// request-validation failures match ErrValidation; result values are
// unspecified when error is non-nil. Implementations never mutate caller-owned
// request values.
//
// LISTING IS A READ. It records no history entry and fires no completion hook.
// Like Reader.Ready it may run the advisory defer-wake sweep first, which is a
// maintenance write of its own and not part of the answer.
type ReadyLister interface {
	// ListReady returns one page of unblocked open work, in the requested
	// policy's order, and the size of the whole ready set.
	ListReady(ctx context.Context, req ReadyListRequest) (ReadyListing, error)
}
