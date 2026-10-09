// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/list_walk.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The listIssues pager: the three things the wire cannot carry for `bd list`,
// done here because the encoder table says they are done here.
//
//   - THE DISPLAY ORDER. The operation publishes `sort` now, so a BOUNDED
//     request in an order the caller named is one request: sortedPage names the
//     order on the wire and the server's own LIMIT decides which rows survive
//     it. What is left of ledger row L2 is the three legs that cannot take that
//     route — an unlimited read, a caller-supplied keyset position, and a
//     server that does not advertise the capability — which still walk to
//     exhaustion and then run the client-side comparator. The weld itself is
//     unchanged: a cursor is bound to the paged order it was minted in
//     (`created` or `priority`, upstream #5666), and the server refuses it under
//     any other `sort`, because resuming a keyset position inside a different
//     order skips and duplicates rows; the seven display orders and every
//     reversed order never carry one.
//   - THE CALLER-SUPPLIED KEYSET POSITION. issueops.Reader promises every
//     implementation honors AfterCreatedAt/AfterID, and the wire populates them
//     only from an opaque cursor the client must not mint. So the position is
//     honored by a SKIP-FORWARD WALK: page the created-order wire from the
//     start and discard the rows at or before the position (D8 row 1).
//   - THE MaxRows CAP. The wire publishes no max_rows, so the cap is enforced
//     during page accumulation and the exact sentinel `bd list` classifies into
//     exit 2 is synthesized here (D12).
//
// WHAT IS DELIBERATELY ABSENT is a `created_before` jump for the keyset walk.
// It would be the obvious optimization and it is unsafe: created_before is
// strictly exclusive while the keyset upper bound is inclusive, so a jump
// computed from an instant whose storage granularity the client cannot observe
// would silently drop the same-instant tail — the exact drop L3 and L12 exist
// to prevent, traded for a round trip.

// wireListSort is the order listIssues serves when no `sort` names another,
// spelled as the sort key ListRequest.SortBy takes for it. A request in this
// order and this direction is the one shape the walk can stop early on, and the
// one shape the pushdown leaves alone.
const wireListSort = "created"

// flaglessListSort is how the pager spells ListRequest's own empty SortBy — the
// order `bd list` shows when nobody names one — as a value the published enum
// carries.
//
// The two are the SAME clause and not merely similar ones: sqlbuild renders `""`
// and `"priority"` through one branch (internal/storage/sqlbuild/sort.go,
// `if sortBy == "" || sortBy == "priority"`), and sqlbuild.Less mirrors it the
// same way. What the server does differently under the named spelling is run the
// page epilogue's comparator over the clause's output, which is a stable sort by
// the same key — order-preserving on rows the same clause already ordered.
//
// It cannot be sent as `sort=` instead: an empty value is not in the published
// enum, so the server would refuse it, and refusing is the correct thing for the
// server to do — an empty enum member is a client that failed to decide.
const flaglessListSort = "priority"

// walkPageSize is how many rows one request asks for when the CLIENT owns the
// paging rather than the caller's own limit — an unlimited read, or any walk to
// exhaustion. It is a round trip's worth of rows, not a bound on the answer.
//
// It is a var and not a const for exactly one reason: the multi-page arm of the
// walk — the cursor echo, the accumulation across pages, the termination — is
// only reachable with more rows than this, and a test that seeded two hundred of
// them would be paying seconds to exercise three lines.
var walkPageSize = 200

// defaultListLimit is the shared list default a nil ListRequest.Limit means.
//
// It is redeclared rather than imported: internal/workapi is denied to this
// package by depguard (a client-side filter is one no server-side gate can
// observe), and the constant is needed here because a walk to exhaustion has to
// know what to trim to. TestDefaultListLimitMatchesTheSharedDefault pins it
// against workapi's own from a test file, where the rule does not apply.
const defaultListLimit = 50

// walkIssues answers one ListRequest from listIssues.
//
// params is the encoder's output and stays authoritative for every filter: the
// only keys this function writes are `limit` and `cursor`, which the encoder
// table already assigns to the pager (the cursor is reserved on the listIssues
// table, and the parent-walk table says the pager owns the per-page limit).
func (s *Store) walkIssues(ctx context.Context, params url.Values, req issueops.ListRequest) (issueops.IssuePage, error) {
	limit := defaultListLimit
	if req.Limit != nil {
		limit = *req.Limit
	}
	// A negative limit is the server's refusal to make, not this pager's: it
	// has a documented 400 for it, and the mapper turns that into the same
	// ErrValidation a local backend answers with. Sending the encoder's params
	// verbatim is how that reaches the caller unmangled.
	if limit < 0 {
		var body apigen.IssuesPage
		if err := s.dispatch(ctx, listRequest(params), &body); err != nil {
			return issueops.IssuePage{}, err
		}
		return issueops.IssuePage{Items: wireRows(body.Items, req.Brief), HasMore: body.HasMore}, nil
	}

	keep := keysetFilter(req)
	// The walk can stop early only when the rows arrive in the order the caller
	// asked for; anything else has to see every row before it knows which ones
	// the page keeps.
	want := 0
	if req.SortBy == wireListSort && !req.Reverse {
		want = limit
	}

	// THE PUSHDOWN LEG. Each conjunct names a leg that must stay a walk, and
	// they are ordered cheapest-decides-first because only the last one can
	// cost a round trip:
	//
	//   limit > 0   — an unlimited read has to cross every row anyway, so
	//                 pushdown saves nothing, and sending `limit=0` down would
	//                 newly meet the server's unlimited-read refusal that the
	//                 fixed-size walk deliberately never reaches. (A negative
	//                 limit returned above; this is "not unlimited".)
	//   want == 0   — the created order the walk ALREADY stops early on. Under
	//                 it `want` is the caller's limit and under every other
	//                 order it is zero, so this is that same fact spelled once
	//                 rather than a second copy of the condition that can drift
	//                 from it. Sending `sort=created` would be accepted and
	//                 would buy nothing but a larger skew surface, so the one
	//                 request family that predates the parameter keeps sending
	//                 no parameter.
	//   keep == nil — no caller-supplied keyset position. keysetFilter returns
	//                 nil for exactly that, so asking IT rather than restating
	//                 its condition is what keeps the two from drifting. The
	//                 filter discards from what the walk FETCHED, and run
	//                 against a page some other order truncated it drops rows
	//                 whose replacements were never fetched — a wrong answer,
	//                 not a slow one.
	//   sort + cap  — a --max-rows cap over an order SQL cannot express. This
	//                 leg's whole cap story is that min(limit, MaxRows+1) IS
	//                 the local expression, and for a GO-SIDE sort it is not:
	//                 workapi.SQLLimit pushes 0 down rather than the limit,
	//                 because the ordering happens after the query, so locally
	//                 the window is MaxRows+1 REGARDLESS of the limit and the
	//                 cap fires on the overage even under a limit at or below
	//                 it. A request bounded by the limit cannot observe that
	//                 overage, so pushing one down would answer where both
	//                 local mode and the walk refuse. sqlbuild.IsGoSideSort is
	//                 asked rather than its member named, so a second Go-side
	//                 key added there cannot reopen this silently. Uncapped,
	//                 the question does not arise and these keys push down like
	//                 any other.
	//
	// The capability probe is last because it is the only one that can dial.
	if limit > 0 && want == 0 && keep == nil &&
		listSortPushdownEligible(req) &&
		!(req.MaxRows > 0 && sqlbuild.IsGoSideSort(req.SortBy)) &&
		s.servesListSort(ctx) {
		return s.sortedPage(ctx, params, req, limit)
	}

	// THE UNLIMITED-READ CAP. An unlimited read walks the cursor to
	// exhaustion, and the walk is bounded on the client by the same cap every
	// unlimited read over http carries (unlimited_read.go): past it the read is
	// refused, never truncated. It rides the walk's existing MaxRows machinery
	// — fetch at most cap+1 wire rows, refuse on the overage — and takes over
	// only where it is the tighter of the two bounds, so a caller's own
	// --max-rows keeps its own refusal and attribution.
	maxRows, maxRowsSource := req.MaxRows, req.MaxRowsSource
	readCap := 0
	if limit == 0 {
		n, err := unlimitedReadCap()
		if err != nil {
			return issueops.IssuePage{}, err
		}
		if maxRows <= 0 || n < maxRows {
			readCap, maxRows, maxRowsSource = n, n, UnlimitedReadCapEnv
		}
	}

	rows, err := s.fetchIssuePages(ctx, params, req.Brief, want, maxRows, maxRowsSource, keep)
	var tooMany *storageops.ErrTooManyRows
	if readCap > 0 && errors.As(err, &tooMany) {
		return issueops.IssuePage{}, &UnlimitedReadCapError{Op: wire.OpListIssues, Cap: readCap, Found: tooMany.Found}
	}
	if err != nil {
		return issueops.IssuePage{}, err
	}

	sortListRows(rows, req.SortBy, req.Reverse)
	hasMore := false
	if limit > 0 && len(rows) > limit {
		rows, hasMore = rows[:limit], true
	}
	return issueops.IssuePage{Items: rows, HasMore: hasMore}, nil
}

// servesListSort reports whether the server advertises issues.list.sort.
//
// It forces the one lazy handshake the store already owns and reads the cached
// capability list, exactly as servesBatchClose does, so it costs at most one
// round trip per process and none once the handshake has run. listIssues is a
// BASELINE operation — dispatch preflights nothing for it — so on a `bd list`
// this is the only handshake there is, which is why the gate above consults it
// last: a request that cannot push down never pays for it.
//
// A handshake that cannot be obtained reports false rather than an error, and
// that is a deliberate choice about which behavior is preserved. This is a
// CAPABILITY PROBE on a baseline read: the answer it is asking for is "may I
// take the fast leg", and every negative answer — the token absent, the server
// too old, the handshake refused — has the same correct response, which is to
// take the leg this client has always taken. Propagating the error instead
// would newly fail `bd list` against a server it works against today, since the
// baseline exemption means today's walk never asks for a handshake at all. The
// error is not lost: the walk's own first request meets the same server, and
// whatever it says — a transport failure, a wrong-project refusal — reaches the
// caller from there.
func (s *Store) servesListSort(ctx context.Context) bool {
	snap, err := s.snapshot(ctx)
	if err != nil || snap == nil {
		return false
	}
	return slices.Contains(snap.Capabilities, wire.CapListSort)
}

// listSortPushdownEligible reports whether req's display order is one
// listIssues can serve directly.
//
// The operation's `sort` enum is closed to the two keyset orders this client
// names wireListSort and flaglessListSort ("created" and "priority") — the
// seven other `bd list --sort` orders have no keyset predicate the server can
// page, so the spec does not publish them here at all; sending one anyway
// would meet the same `invalid_value` 400 a stale client earns for a value
// outside the enum.
//
// listIssues ALSO publishes no `reverse` parameter — that member exists only
// on GET /v0/beads/issues:query, a different operation with a different
// vocabulary (nine orders, used for the Go-side sort case up above, not a
// keyset order at all). A pushdown request naming `reverse` would not be
// answered backwards; it would be refused outright as a parameter this
// operation does not know, since listIssues never reads one. So a reversed
// request of any order, including the two eligible ones, has to come home
// over the walk and be re-sorted client-side in sortListRows — the walk and
// the early-stop leg above both already gate on !req.Reverse for the same
// reason.
func listSortPushdownEligible(req issueops.ListRequest) bool {
	if req.Reverse {
		return false
	}
	switch req.SortBy {
	case "", wireListSort, flaglessListSort:
		return true
	default:
		return false
	}
}

// sortedPage answers a bounded request in the caller's own order with ONE
// request, by naming that order on the wire.
//
// What this fixes is not the sort — the client could always re-sort what it
// fetched — but the ROW SET: which rows survive the caller's limit is now
// decided under the caller's order, by the same reader a local `bd list` runs,
// instead of by fetching every row the filter matches. The page carries no
// cursor and needs none: there is exactly one request, and the server's
// has_more IS the caller's, because unlike the walk this leg discards nothing
// the server counted.
//
// sortListRows still runs. It is O(n log n) over the page rather than over the
// result set, and keeping it leaves the display order self-authoritative
// against tie-semantics drift in a server one version away — which is the half
// of an order a re-sort CAN repair.
func (s *Store) sortedPage(ctx context.Context, params url.Values, req issueops.ListRequest, limit int) (issueops.IssuePage, error) {
	page := url.Values{}
	for key, values := range params {
		page[key] = values
	}
	sortBy := req.SortBy
	if sortBy == "" {
		sortBy = flaglessListSort
	}
	page.Set("sort", sortBy)
	// No `reverse` here: listIssues publishes no such parameter (that member
	// belongs only to GET /v0/beads/issues:query's nine-order vocabulary), and
	// listSortPushdownEligible already refused this call before it got here if
	// req.Reverse were set. Sending it — even spelled false — would meet this
	// operation's unknown-parameter refusal, since the handler never reads a
	// `reverse` key at all.

	// The cap, spelled as the storage layer spells it: LIMIT min(Limit,
	// MaxRows+1). The overage row is what proves the cap fired, and asking for
	// no more than one past it is what stops the cap from bounding a page it
	// was never meant to bound. Under a cap at or above the limit the request
	// is the limit's own, so the cap CANNOT fire — which is local semantics
	// (the window could not have exceeded it) and the L15 narrowing.
	//
	// That equivalence holds because a capped GO-SIDE sort never reaches this
	// function: for those keys workapi.SQLLimit sends 0 rather than the limit,
	// so the local window is MaxRows+1 and the cap CAN fire under a limit
	// beneath it. walkIssues' gate is what keeps them on the walk, which is
	// the leg that models that unbounded window.
	size := limit
	if req.MaxRows > 0 && req.MaxRows+1 < size {
		size = req.MaxRows + 1
	}
	page.Set("limit", strconv.Itoa(size))

	var body apigen.IssuesPage
	if err := s.dispatch(ctx, listRequest(page), &body); err != nil {
		return issueops.IssuePage{}, err
	}
	if req.MaxRows > 0 && len(body.Items) > req.MaxRows {
		return issueops.IssuePage{}, &storageops.ErrTooManyRows{Found: len(body.Items), Cap: req.MaxRows, Source: req.MaxRowsSource}
	}

	rows := wireRows(body.Items, req.Brief)
	sortListRows(rows, req.SortBy, req.Reverse)
	return issueops.IssuePage{Items: rows, HasMore: body.HasMore}, nil
}

// fetchIssuePages walks the created-order wire, keeping the rows keep admits.
//
// want is how many KEPT rows are enough to answer, or 0 for "every one of
// them". It over-fetches by one, exactly as the store-backed reader asks its
// query for one row past the page: the extra row IS the has-more verdict, and
// asking the wire for its own has_more instead would answer about the rows the
// SERVER hid rather than the ones the caller's page does.
//
// brief travels down here rather than being applied to the walk's answer because
// this is where the rows are lifted, and wireRows is the one place that knows
// how to mark a projected one. It sits next to params rather than beside want
// and maxRows deliberately: it is the only bool in the list, so it has no
// same-typed neighbor to be transposed with.
func (s *Store) fetchIssuePages(
	ctx context.Context,
	params url.Values,
	brief bool,
	want, maxRows int,
	maxRowsSource string,
	keep func(*types.IssueWithCounts) bool,
) ([]*types.IssueWithCounts, error) {
	page := url.Values{}
	for key, values := range params {
		page[key] = values
	}

	var (
		kept    []*types.IssueWithCounts
		fetched int
		cursor  string
	)
	for {
		size := walkPageSize
		if want > 0 {
			if remaining := want + 1 - len(kept); remaining < size {
				size = remaining
			}
		}
		if maxRows > 0 {
			// Mirrors the storage layer's LIMIT MaxRows+1 overage probe: one
			// row past the cap is what proves the cap fired, and never more
			// than one, because the cap bounds WIRE ROWS FETCHED (D12).
			if allowed := maxRows + 1 - fetched; allowed < size {
				size = allowed
			}
		}
		if size <= 0 {
			break
		}

		page.Set("limit", strconv.Itoa(size))
		if cursor != "" {
			page.Set("cursor", cursor)
		}
		var body apigen.IssuesPage
		if err := s.dispatch(ctx, listRequest(page), &body); err != nil {
			return nil, err
		}

		fetched += len(body.Items)
		if maxRows > 0 && fetched > maxRows {
			return nil, &storageops.ErrTooManyRows{Found: fetched, Cap: maxRows, Source: maxRowsSource}
		}
		for _, row := range wireRows(body.Items, brief) {
			if keep == nil || keep(row) {
				kept = append(kept, row)
			}
		}

		if want > 0 && len(kept) > want {
			break
		}
		// next_cursor is present if and only if has_more WITHIN THE UNSORTED
		// FAMILY, which is the scope the document now states the biconditional
		// at: a page the server ordered by a `sort` reports a truthful has_more
		// and carries no cursor at all, because a cursor is a position in the
		// created order and that page is not in it.
		//
		// THIS FUNCTION IS ALWAYS INSIDE THAT FAMILY, and it is the reason the
		// stop below is still a correctness check rather than an early exit
		// from a shape the server ships on purpose: the `sort` key is written
		// by sortedPage and by nothing else, and sortedPage sends one request
		// and reads no cursor. So on every page this walk can receive, a claim
		// of more with no position named is a server this client cannot page,
		// and it stops rather than re-issuing the same request forever.
		if !body.HasMore || body.NextCursor == nil || *body.NextCursor == "" {
			break
		}
		cursor = *body.NextCursor
	}
	if kept == nil {
		kept = []*types.IssueWithCounts{}
	}
	return kept, nil
}

func listRequest(query url.Values) wire.Request {
	return wire.Request{
		Op:     wire.OpListIssues,
		Method: http.MethodGet,
		Path:   wire.PathIssues,
		Query:  query,
	}
}

// keysetFilter turns a caller-supplied position into the discard rule of the
// skip-forward walk, or nil when the request carries none.
//
// AfterCreatedAt ALONE decides whether there is a position, which is the
// contract's rule (issueops.ListRequest) and the local predicate's
// (sqlbuild.BuildIssueFilterClauses). An AfterID or AfterPriority with no instant
// is ignored there, so it is ignored here — read as a position at the zero
// instant instead, it would discard every row, since every row is after it.
//
// The position is a PAIR and both halves matter: rows older than its timestamp
// are kept, and rows sharing that timestamp are kept only when their id sorts
// after it. A filter that compared the timestamp alone would drop the
// same-instant row, which is exactly how a keyset page loses records.
//
// AfterPriority extends the pair to the (priority ASC, created_at DESC, id ASC)
// order, and it is decided FIRST: a row at another priority is past the
// position exactly when its priority is the higher-numbered one, whatever its
// instant, and only a row at the position's own priority falls through to the
// pair. That nesting is sqlbuild.KeysetPriorityCreatedAtIDPredicate's, for its
// reason: the pair alone would re-deliver every higher-priority row created
// before the position and drop every lower-priority row created after it.
func keysetFilter(req issueops.ListRequest) func(*types.IssueWithCounts) bool {
	if req.AfterCreatedAt == nil {
		return nil
	}
	at, after := *req.AfterCreatedAt, req.AfterID
	byPriority, atPriority := req.AfterPriority != nil, 0
	if byPriority {
		atPriority = *req.AfterPriority
	}
	return func(row *types.IssueWithCounts) bool {
		if row == nil || row.Issue == nil {
			return false
		}
		switch {
		case byPriority && row.Priority != atPriority:
			return row.Priority > atPriority
		case row.CreatedAt.After(at):
			return false
		case row.CreatedAt.Equal(at) && row.ID <= after:
			return false
		default:
			return true
		}
	}
}
