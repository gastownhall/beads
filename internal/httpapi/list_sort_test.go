package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// listSortOf drives GET /v0/beads/issues once and returns the filter the
// handler built, which is the only place the order and the position are
// observable — the response body shows rows, not what was asked for.
func listSortOf(t *testing.T, query string) types.IssueFilter {
	t.Helper()
	ts, rec := newReadServer(t, Config{})
	resp := ts.get(t, "/v0/beads/issues"+query)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v0/beads/issues%s: status = %d, want 200; body=%v", query, resp.StatusCode, decodeBody(t, resp))
	}
	filters := rec.searchFilters()
	if len(filters) != 1 {
		t.Fatalf("GET /v0/beads/issues%s built %d filters, want 1", query, len(filters))
	}
	return filters[0]
}

// TestListSortDefaultsToCreatedOrderForever is the compatibility guard on the
// one decision this parameter must NOT quietly make.
//
// Absent `sort` has always meant `(created_at DESC, id ASC)`, and every client
// written against v0 — including the walk-and-sort clients this parameter
// exists to retire — assumes it. Changing the default would be a silent
// wire-behaviour change for all of them: no error, no version bump, just
// different rows once `limit` truncates. The literal is asserted rather than
// the constant the handler reads, because comparing the handler against its own
// default passes for every value that default could take.
func TestListSortDefaultsToCreatedOrderForever(t *testing.T) {
	if got := listSortOf(t, "").SortBy; got != "created" {
		t.Errorf("an absent `sort` reached storage as %q, want %q — the default is the compatibility contract for every v0 client", got, "created")
	}
	if got := listSortOf(t, "?sort=created").SortBy; got != "created" {
		t.Errorf("`sort=created` reached storage as %q, want %q", got, "created")
	}
	// An EMPTY value is the absent spelling, which is this surface's existing
	// reading of one — `ready` resolves `?sort=` to its own default the same
	// way, through the same decoder. It is pinned here rather than left
	// implicit because the alternative readings are both bad: refusing it would
	// make `?sort=` a 400 on one operation and a default on its sibling, and
	// passing "" through to storage would silently adopt whatever the empty
	// sort key maps to there.
	if got := listSortOf(t, "?sort=").SortBy; got != "created" {
		t.Errorf("`sort=` (empty) reached storage as %q, want %q — an empty value is the absent spelling here, as it is on `ready`", got, "created")
	}
}

// TestListSortPriorityReachesStorageAsThePriorityOrder pins the whole point of
// the parameter: `sort=priority` must reach storage as the order `bd list`
// renders flaglessly (priority ASC, created_at DESC, id ASC), so one request
// answers what a walk-to-exhaustion plus a client-side comparator answered
// before.
//
// The assertion is on the filter and not on the row order because the fixture's
// rows come back in whatever order the fake hands them over. What is at risk is
// the ORDER THE QUERY ASKED FOR; a handler that decoded the parameter and then
// forwarded "created" anyway would serve a correct-looking page in the wrong
// order and no response-shaped assertion would see it.
func TestListSortPriorityReachesStorageAsThePriorityOrder(t *testing.T) {
	filter := listSortOf(t, "?sort=priority")
	if filter.SortBy != "priority" {
		t.Errorf("`sort=priority` reached storage as SortBy=%q, want %q", filter.SortBy, "priority")
	}
	if filter.SortDesc {
		t.Error("`sort=priority` reached storage reversed; this operation publishes no direction knob and each direction is a different keyset")
	}
}

// TestListSortRefusesAnOrderItCannotPage pins the CLOSED enum. Every served
// order is a keyset contract — a position shape, a strictly-after predicate and
// an index — so an order with no proven total key cannot be accepted and then
// paged. The refusal is by name, and it is the same `unknown_parameter`-adjacent
// machinery a client dispatches on.
//
// The values chosen are the ones a caller is most likely to try, because
// `bd list --sort` and the query operation on this same surface both take them:
// `updated` (maximally mutable key), `id` (an order the database cannot
// express) and `status` (unindexed). Accepting any of them would mean either a
// silently unordered page or a server-side walk — the very thing this parameter
// removes.
func TestListSortRefusesAnOrderItCannotPage(t *testing.T) {
	for _, bad := range []string{"updated", "id", "status", "title", "closed", "assignee", "type", "PRIORITY", "created,priority", "ready", "hybrid"} {
		ts, rec := newReadServer(t, Config{})
		resp := ts.get(t, "/v0/beads/issues?sort="+bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET ?sort=%q: status = %d, want 400", bad, resp.StatusCode)
			continue
		}
		body := decodeBody(t, resp)
		if body["code"] != string(CodeInvalidArgument) {
			t.Errorf("GET ?sort=%q: code = %v, want invalid_argument", bad, body["code"])
		}
		if body["param"] != "sort" {
			t.Errorf("GET ?sort=%q: param = %v, want \"sort\" — param is what a client dispatches on", bad, body["param"])
		}
		if body["reason"] != string(ReasonInvalidValue) {
			t.Errorf("GET ?sort=%q: reason = %v, want invalid_value", bad, body["reason"])
		}
		if n := len(rec.searchFilters()); n != 0 {
			t.Errorf("GET ?sort=%q reached storage", bad)
		}
	}
}

// TestListCursorCarriesItsOwnOrder is the test that decides whether this
// parameter is safe to ship, and it is the one the design's own audit says the
// obvious implementation fails.
//
// bd serve's cursor is base64 of `{t,i}` with no seal and no binding. Add
// `sort` without touching it and a token minted under the created order decodes
// perfectly under `sort=priority`: the server reads its instant and its id,
// applies them as a position in a DIFFERENT total order, and answers 200 with a
// page that skips and duplicates rows. Nothing refuses, nothing logs, and the
// client cannot tell.
//
// A position is only interpretable together with the order it was minted in, so
// the token carries that order and decode refuses a mismatch. This does not
// contradict "the token carries no filters": filters select the SET, the order
// defines what the position MEANS.
func TestListCursorCarriesItsOwnOrder(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	items := []*types.IssueWithCounts{{Issue: &types.Issue{ID: "bd-7", CreatedAt: created, Priority: 2}}}

	createdToken := cursorFor(items, orderCreated)
	priorityToken := cursorFor(items, orderPriority)
	if createdToken == "" || priorityToken == "" {
		t.Fatal("cursorFor returned no token for a nonempty page")
	}
	if createdToken == priorityToken {
		t.Fatal("the two orders mint the SAME token, so a position cannot say which order it is a position in — this is the skip-and-duplicate hole")
	}

	for _, tc := range []struct {
		name  string
		query string
		token string
	}{
		{"a created-order token replayed under sort=priority", "?sort=priority&cursor=", createdToken},
		{"a priority-order token replayed under the default order", "?cursor=", priorityToken},
		{"a priority-order token replayed under sort=created", "?sort=created&cursor=", priorityToken},
	} {
		ts, rec := newReadServer(t, Config{})
		resp := ts.get(t, "/v0/beads/issues"+tc.query+tc.token)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 — it decoded and was silently reinterpreted as a position in the wrong order", tc.name, resp.StatusCode)
			continue
		}
		if body := decodeBody(t, resp); body["code"] != string(CodeInvalidCursor) {
			t.Errorf("%s: code = %v, want invalid_cursor (documented recovery: restart paging)", tc.name, body["code"])
		}
		if n := len(rec.searchFilters()); n != 0 {
			t.Errorf("%s reached storage", tc.name)
		}
	}

	// The matching legs, so the refusal above is shown to be about the ORDER
	// and not about cursors in general.
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"a created-order token under the default order", "?cursor=" + createdToken},
		{"a created-order token under sort=created", "?sort=created&cursor=" + createdToken},
		{"a priority-order token under sort=priority", "?sort=priority&cursor=" + priorityToken},
	} {
		if filter := listSortOf(t, tc.query); filter.AfterCreatedAt == nil {
			t.Errorf("%s: the position did not reach storage", tc.name)
		}
	}
}

// TestListPriorityCursorReachesStorageAsAWholePosition pins the plumbing the
// order tag exists to protect. Under `sort=priority` the position is
// (priority, created_at, id); a handler that decoded the priority and forwarded
// only the instant and the id would page a priority-ordered ORDER BY with a
// created-order predicate, dropping every row of a higher-numbered priority
// created after the cursor — silently, with a 200.
func TestListPriorityCursorReachesStorageAsAWholePosition(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	items := []*types.IssueWithCounts{{Issue: &types.Issue{ID: "bd-7", CreatedAt: created, Priority: 3}}}

	filter := listSortOf(t, "?sort=priority&cursor="+cursorFor(items, orderPriority))
	if filter.AfterCreatedAt == nil || !filter.AfterCreatedAt.Equal(created) {
		t.Fatalf("filter.AfterCreatedAt = %v, want %v", filter.AfterCreatedAt, created)
	}
	if filter.AfterID != "bd-7" {
		t.Errorf("filter.AfterID = %q, want %q", filter.AfterID, "bd-7")
	}
	if filter.AfterPriority == nil {
		t.Fatal("filter.AfterPriority is nil: the priority half of the position was dropped between the cursor and storage")
	}
	if *filter.AfterPriority != 3 {
		t.Errorf("filter.AfterPriority = %d, want 3", *filter.AfterPriority)
	}

	// The created order does NOT position by priority: sending one there would
	// AND a priority bound onto a created-order walk and drop rows the caller
	// asked for.
	if p := listSortOf(t, "?cursor="+cursorFor(items, orderCreated)).AfterPriority; p != nil {
		t.Errorf("the created order forwarded AfterPriority=%d; that order's position is (created_at, id) and nothing else", *p)
	}
}

// TestOutstandingV1CursorsStillPage is the no-flag-day guard. Bumping the
// encoding is the ONE thing the document says invalidates a cursor, and a
// client mid-traversal when this server upgrades holds a `v1.` token. Those
// tokens were all minted in the created order — it was the only order there
// was — so they stay readable as exactly that, and the upgrade costs no
// restarted traversal.
//
// The same rule closes the door behind them: a `v1.` token is a created-order
// position, so replaying one under `sort=priority` is the same mismatch a `v2.`
// created token is, and is refused the same way.
func TestOutstandingV1CursorsStillPage(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	legacy := legacyV1Cursor(created, "bd-7")

	filter := listSortOf(t, "?cursor="+legacy)
	if filter.AfterCreatedAt == nil || !filter.AfterCreatedAt.Equal(created) {
		t.Fatalf("a v1 token did not resume: AfterCreatedAt = %v, want %v", filter.AfterCreatedAt, created)
	}
	if filter.AfterID != "bd-7" {
		t.Errorf("a v1 token resumed at id %q, want %q", filter.AfterID, "bd-7")
	}
	if filter.AfterPriority != nil {
		t.Errorf("a v1 token positioned by priority (%d); it was minted in the created order and means nothing else", *filter.AfterPriority)
	}

	ts, rec := newReadServer(t, Config{})
	resp := ts.get(t, "/v0/beads/issues?sort=priority&cursor="+legacy)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a v1 token under sort=priority: status = %d, want 400", resp.StatusCode)
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeInvalidCursor) {
		t.Errorf("a v1 token under sort=priority: code = %v, want invalid_cursor", body["code"])
	}
	if n := len(rec.searchFilters()); n != 0 {
		t.Error("a v1 token under sort=priority reached storage")
	}
}

// TestPriorityCursorNeedsItsPriority pins the decode of a token that names the
// priority order and carries no priority. It is not reachable from this
// server's own minting, which is exactly why it is asserted: the token is
// base64 of legible JSON, the document tells clients never to construct one,
// and a hand-rolled `{"o":"priority","t":…,"i":…}` would otherwise decode into
// a half position and page a priority order with a created-order predicate.
func TestPriorityCursorNeedsItsPriority(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	handRolled := encodeCursor(cursorPosition{Order: orderPriority, CreatedAt: created, ID: "bd-7"})

	if _, ok := decodeCursor(handRolled, orderPriority); ok {
		t.Error("a priority-order token with no priority decoded; the position is (priority, created_at, id) and two thirds of one is not a position")
	}
}

// legacyV1Cursor mints a token in the retired v1 encoding — base64 of `{t,i}`
// under a `v1.` prefix — INDEPENDENTLY of production code. Minting it by
// calling the production encoder would make the compatibility assertion
// circular: the encoder no longer produces v1, and a helper that reached for
// whatever it does produce would assert only that a token round-trips through
// itself. This is the literal bytes a client is holding across the upgrade.
func legacyV1Cursor(createdAt time.Time, id string) string {
	blob, err := json.Marshal(struct {
		CreatedAt time.Time `json:"t"`
		ID        string    `json:"i"`
	}{createdAt, id})
	if err != nil {
		panic(err)
	}
	return "v1." + base64.RawURLEncoding.EncodeToString(blob)
}
