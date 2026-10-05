//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_list_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The List behaviors the shared Reader contract cannot reach over this wire,
// asserted through the same composition.
//
// Almost every List case in reader_contract.go scopes itself with IDFilter, and
// listIssues publishes no id parameter, so those cases are parked next door and
// their behaviors are pinned HERE instead — same assertions, scoped by a label,
// which the wire does carry. What is not restated here is anything the contract
// case actually runs: this file exists to close the gap the parking opened, not
// to grow a second contract.

// listScope mints a label nothing else in the package uses.
func listScope(name string) string { return servedIssuePrefix + "-ls-" + name }

func listID(name, tag string) string { return fmt.Sprintf("%s-ls%s-%s", servedIssuePrefix, name, tag) }

func seedListIssue(t *testing.T, ctx context.Context, c *servedComposition, id, scope string, at time.Time, priority int) {
	t.Helper()
	issue := &types.Issue{
		ID: id, Title: id, Status: types.StatusOpen, Priority: priority,
		IssueType: types.TypeTask, Labels: []string{scope},
	}
	if !at.IsZero() {
		issue.CreatedAt = at
		issue.UpdatedAt = at
	}
	if err := c.seedIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func servedReader(t *testing.T) (*servedComposition, issueops.Reader) {
	t.Helper()
	c := composition(t)
	reader := bindRole(t, c.fixture(t), func(s *Store) (issueops.Reader, error) { return s.IssueReader() })
	return c, reader
}

func pageIDs(page issueops.IssuePage) []string {
	ids := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		if item != nil && item.Issue != nil {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

// listSpy is a client onto the composition that records every listIssues
// request the pager sends, against a handshake the TEST chose.
//
// It answers the two questions the sort pushdown makes checkable and that no
// assertion about the returned page can reach: HOW MANY round trips a request
// cost, and WHICH keys each one carried. Both are the point of the leg — the
// answer is supposed to be identical either way — so a case that only compared
// answers would pass against a client that had never gained the leg at all.
//
// The handshake is supplied rather than fetched because that is how a
// down-level server is modelled without a second binary: New's third parameter
// pre-seeds the snapshot cache, so the capability probe reads it and never
// dials. That has a second effect this file relies on — the recorded requests
// are listIssues and nothing else, so a dial count is a page count.
type listSpy struct {
	store *Store
	sent  []url.Values
}

func (c *servedComposition) listSpy(t *testing.T, snapshot *apigen.ContextResponse) *listSpy {
	t.Helper()
	base, err := url.Parse(c.baseURL)
	if err != nil {
		t.Fatalf("parse the composition base URL: %v", err)
	}
	transport, err := wire.New(base, nil, wire.Options{})
	if err != nil {
		t.Fatalf("build a spying wire client: %v", err)
	}
	spy := &listSpy{}
	spy.store = New(Target{BaseURL: base}, tamperedWire{
		WireClient: servedWire{transport},
		tamper: func(req *wire.Request) {
			if req.Op != wire.OpListIssues {
				return
			}
			// Copied, because the pager reuses ONE url.Values across the pages
			// of a walk: recording the map itself would record the last page's
			// query as many times as there were pages.
			query := url.Values{}
			for key, values := range req.Query {
				query[key] = append([]string(nil), values...)
			}
			spy.sent = append(spy.sent, query)
		},
	}, snapshot)
	return spy
}

func (s *listSpy) reader(t *testing.T) issueops.Reader {
	t.Helper()
	reader, err := s.store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	return reader
}

func (s *listSpy) dials() int { return len(s.sent) }

// sortKeys is the `sort` each recorded request named, "" for one that named
// none. It exists so a failure prints what the client actually sent.
func (s *listSpy) sortKeys() []string {
	keys := make([]string, 0, len(s.sent))
	for _, query := range s.sent {
		keys = append(keys, query.Get("sort"))
	}
	return keys
}

// listSortHandshakes returns the server's OWN handshake and a copy of it with
// the pushdown capability struck out — the two arms the cases below run.
//
// The advertised half is CHECKED rather than assumed. If the served build
// stopped advertising the token, both arms would be the down-level one, every
// answer comparison would still pass, and the pushdown would be silently
// untested; that is what this Fatalf exists to make loud.
func listSortHandshakes(ctx context.Context, t *testing.T, c *servedComposition) (advertised, downLevel *apigen.ContextResponse) {
	t.Helper()
	snapshot, err := c.client.snapshot(ctx)
	if err != nil {
		t.Fatalf("handshake the served composition: %v", err)
	}
	if !slices.Contains(snapshot.Capabilities, wire.CapListSort) {
		t.Fatalf("the served bd serve advertises %v, which does not include %q; every arm below would be the "+
			"down-level one and the comparison would prove nothing", snapshot.Capabilities, wire.CapListSort)
	}
	up, down := *snapshot, *snapshot
	up.Capabilities = slices.Clone(snapshot.Capabilities)
	down.Capabilities = slices.DeleteFunc(slices.Clone(snapshot.Capabilities),
		func(token string) bool { return token == wire.CapListSort })
	if len(down.Capabilities) != len(up.Capabilities)-1 {
		t.Fatalf("striking %q left %d capabilities of %d", wire.CapListSort, len(down.Capabilities), len(up.Capabilities))
	}
	return &up, &down
}

// TestServedReaderListLimitBoundaryUnderALabelScope is
// RunReaderListLimitBoundaryUnderASortTheDatabaseCanExpress with the id scope
// swapped for a label one. Under created order the walk stops early and the
// verdict comes from the over-fetched row, which is the arm an off-by-one lives
// in.
func TestServedReaderListLimitBoundaryUnderALabelScope(t *testing.T) {
	ctx := t.Context()
	c, reader := servedReader(t)
	scope := listScope("limit")

	base := time.Now().UTC().Truncate(time.Second).Add(-5 * time.Hour)
	var ids []string
	for i, tag := range []string{"a", "b", "c"} {
		id := listID("limit", tag)
		ids = append(ids, id)
		seedListIssue(t, ctx, c, id, scope, base.Add(time.Duration(i)*time.Minute), 2)
	}

	full, err := reader.List(ctx, issueops.ListRequest{Labels: []string{scope}, SortBy: "created", Limit: ptrTo(0)})
	if err != nil {
		t.Fatalf("List --limit 0: %v", err)
	}
	if len(full.Items) != len(ids) {
		t.Fatalf("List --limit 0 returned %v, want the three seeded rows", pageIDs(full))
	}
	if full.HasMore {
		t.Error("an unlimited list can hide nothing but reported HasMore")
	}
	order := pageIDs(full)

	for _, test := range []struct {
		name    string
		limit   *int
		wantN   int
		hasMore bool
	}{
		{"unset takes the shared default, which does not truncate three rows", nil, 3, false},
		{"a limit under the result count truncates and says so", ptrTo(2), 2, true},
		{"a limit exactly at the result count hides nothing", ptrTo(3), 3, false},
		{"a limit of one is the tightest page the over-fetch has to get right", ptrTo(1), 1, true},
	} {
		page, err := reader.List(ctx, issueops.ListRequest{Labels: []string{scope}, SortBy: "created", Limit: test.limit})
		if err != nil {
			t.Fatalf("List (%s): %v", test.name, err)
		}
		if page.Items == nil {
			t.Errorf("List (%s) returned a nil Items", test.name)
		}
		if len(page.Items) != test.wantN {
			t.Errorf("List (%s) returned %v, want %d rows", test.name, pageIDs(page), test.wantN)
		}
		if page.HasMore != test.hasMore {
			t.Errorf("List (%s) HasMore = %v, want %v", test.name, page.HasMore, test.hasMore)
		}
		if got := pageIDs(page); !slices.Equal(got, order[:min(len(got), len(order))]) {
			t.Errorf("List (%s) returned %v, want a prefix of %v", test.name, got, order)
		}
	}
}

// TestServedReaderListAppliesTheClientSideDisplayOrder is ledger row L2 in one
// case: the wire is welded to created order, so `--sort id` — natural-numeric,
// which no lexical ORDER BY produces — can only be produced client-side, and the
// TRIM has to run after it or the page keeps the wrong rows.
func TestServedReaderListAppliesTheClientSideDisplayOrder(t *testing.T) {
	ctx := t.Context()
	c, reader := servedReader(t)
	scope := listScope("order")

	one, two, ten := listID("order", "1"), listID("order", "2"), listID("order", "10")
	// Seeded in an order that is neither the natural one nor the lexical one, so
	// a client that returned the wire's own order fails visibly.
	for _, id := range []string{ten, one, two} {
		seedListIssue(t, ctx, c, id, scope, time.Time{}, 2)
	}

	page, err := reader.List(ctx, issueops.ListRequest{Labels: []string{scope}, SortBy: "id"})
	if err != nil {
		t.Fatalf("List --sort id: %v", err)
	}
	if want := []string{one, two, ten}; !slices.Equal(pageIDs(page), want) {
		t.Errorf("List --sort id = %v, want %v", pageIDs(page), want)
	}
	if page.HasMore {
		t.Error("List --sort id reported HasMore on an untruncated page")
	}

	page, err = reader.List(ctx, issueops.ListRequest{Labels: []string{scope}, SortBy: "id", Limit: ptrTo(2)})
	if err != nil {
		t.Fatalf("List --sort id --limit 2: %v", err)
	}
	if want := []string{one, two}; !slices.Equal(pageIDs(page), want) {
		t.Errorf("List --sort id --limit 2 = %v, want %v: the trim runs after the sort", pageIDs(page), want)
	}
	if !page.HasMore {
		t.Error("List --sort id --limit 2 hid a row without reporting HasMore")
	}

	page, err = reader.List(ctx, issueops.ListRequest{Labels: []string{scope}, SortBy: "id", Reverse: true, Limit: ptrTo(2)})
	if err != nil {
		t.Fatalf("List --sort id --reverse --limit 2: %v", err)
	}
	if want := []string{ten, two}; !slices.Equal(pageIDs(page), want) {
		t.Errorf("List --sort id --reverse --limit 2 = %v, want %v", pageIDs(page), want)
	}
}

// TestServedReaderListSortPushdownIsOneDial is what the pushdown BUYS, stated
// as the only thing that distinguishes it: a bounded request in an order the
// caller named costs one round trip instead of a walk to exhaustion.
//
// The request is the FLAGLESS one on purpose. It is the order `bd list` shows
// when nobody names a sort, so it is the order almost every real invocation is
// in, and it is the one the walk could never stop early on — before S2 it read
// every matching row to return five.
//
// GOES RED when the pushdown leg is removed from walkIssues: the first arm
// takes four requests instead of one. The control arm is what tells that apart
// from a spy that has stopped counting — it asserts the walk's exact page
// arithmetic, so a broken counter fails there and a working pushdown does not.
func TestServedReaderListSortPushdownIsOneDial(t *testing.T) {
	ctx := t.Context()
	c := composition(t)
	scope := listScope("pushdown")

	restore := walkPageSize
	walkPageSize = 3
	t.Cleanup(func() { walkPageSize = restore })

	base := time.Now().UTC().Truncate(time.Second).Add(-9 * time.Hour)
	const seeded = 10
	for i := range seeded {
		// Priorities cycle against a monotone creation order, so the flagless
		// order (priority ASC, created DESC, id ASC) picks a different five
		// rows than a created-order prefix would — which is what makes the
		// answer comparison below evidence about the ORDER rather than a
		// coincidence of the fixture.
		seedListIssue(t, ctx, c, listID("pushdown", fmt.Sprintf("%02d", i)), scope,
			base.Add(time.Duration(i)*time.Minute), i%4)
	}

	advertised, downLevel := listSortHandshakes(ctx, t, c)
	req := issueops.ListRequest{Labels: []string{scope}, Limit: ptrTo(5)}

	push := c.listSpy(t, advertised)
	got, err := push.reader(t).List(ctx, req)
	if err != nil {
		t.Fatalf("List under the pushdown: %v", err)
	}
	if push.dials() != 1 {
		t.Errorf("the pushdown took %d requests (sorts %v), want exactly one", push.dials(), push.sortKeys())
	}

	walk := c.listSpy(t, downLevel)
	want, err := walk.reader(t).List(ctx, req)
	if err != nil {
		t.Fatalf("List under the down-level walk: %v", err)
	}
	if walk.dials() != 4 {
		t.Fatalf("the down-level walk took %d requests, want the four that %d rows at %d per page cost; "+
			"if this says one, the spy has stopped counting rather than the pushdown having fired",
			walk.dials(), seeded, walkPageSize)
	}

	if !slices.Equal(pageIDs(got), pageIDs(want)) {
		t.Errorf("the pushdown answered %v and the walk answered %v; the fast leg must not change the answer",
			pageIDs(got), pageIDs(want))
	}
	if got.HasMore != want.HasMore {
		t.Errorf("the pushdown reported HasMore = %v and the walk %v", got.HasMore, want.HasMore)
	}
	// THE PREMISE, so neither comparison above can be satisfied vacuously: the
	// page has to truncate, and it has to truncate to rows a created-order
	// prefix would not have chosen.
	if !got.HasMore {
		t.Fatalf("a limit of 5 over %d rows reported no more; this case never exercised a truncating page", seeded)
	}
	welded := c.listSpy(t, downLevel)
	created, err := welded.reader(t).List(ctx, issueops.ListRequest{
		Labels: []string{scope}, SortBy: "created", Limit: ptrTo(5),
	})
	if err != nil {
		t.Fatalf("List in the welded order: %v", err)
	}
	if slices.Equal(pageIDs(got), pageIDs(created)) {
		t.Fatalf("the flagless page and the created-order page are both %v; the fixture no longer "+
			"distinguishes the pushdown's order from the wire's own, so the assertions above prove nothing",
			pageIDs(got))
	}
}

// TestServedReaderListFallsBackToTheWalkWithoutTheCapability is the other half
// of the preflight: a server that does not advertise the token is sent no
// `sort` and no `reverse` AT ALL, on any page, and still gets the right answer.
//
// The two arms assert different things and both are load-bearing. The
// advertised arm pins the request the pushdown utters, key by key, because that
// request is the whole contract with the server. The down-level arm pins its
// ABSENCE, because a client that emitted `sort` unconditionally would work
// perfectly against this composition's server and 400 against every older one.
//
// GOES RED when the servesListSort conjunct is dropped from the gate: the
// down-level arm sends one request carrying sort=priority.
func TestServedReaderListFallsBackToTheWalkWithoutTheCapability(t *testing.T) {
	ctx := t.Context()
	c := composition(t)
	scope := listScope("downlevel")

	restore := walkPageSize
	walkPageSize = 1
	t.Cleanup(func() { walkPageSize = restore })

	base := time.Now().UTC().Truncate(time.Second).Add(-11 * time.Hour)
	ids := map[string]string{}
	for i, seed := range []struct {
		tag      string
		priority int
	}{{"a", 3}, {"b", 1}, {"c", 2}, {"d", 0}} {
		ids[seed.tag] = listID("downlevel", seed.tag)
		seedListIssue(t, ctx, c, ids[seed.tag], scope, base.Add(time.Duration(i)*time.Minute), seed.priority)
	}
	// Priority ASC takes d then b; the welded created-DESC order takes d then
	// c. The two pages differ in their SECOND row, so an answer of [d b] is
	// only reachable by honoring the sort.
	wantPage := []string{ids["d"], ids["b"]}

	advertised, downLevel := listSortHandshakes(ctx, t, c)
	req := issueops.ListRequest{Labels: []string{scope}, SortBy: "priority", Limit: ptrTo(2)}

	push := c.listSpy(t, advertised)
	got, err := push.reader(t).List(ctx, req)
	if err != nil {
		t.Fatalf("List under the pushdown: %v", err)
	}
	if push.dials() != 1 {
		t.Fatalf("the pushdown took %d requests (sorts %v), want one; the per-page size is not its bound, "+
			"the caller's limit is", push.dials(), push.sortKeys())
	}
	sent := push.sent[0]
	for _, want := range []struct{ key, value string }{
		{"sort", "priority"},
		{"limit", "2"},
	} {
		if got := sent.Get(want.key); got != want.value {
			t.Errorf("the pushdown sent %s=%q, want %q (whole query %v)", want.key, got, want.value, sent)
		}
	}
	// No `reverse` ever, pushdown or not: listIssues publishes no such
	// parameter at all (S3 reconciliation, 2026-10) — it
	// belongs only to GET /v0/beads/issues:query's nine-order vocabulary —
	// so a client that sent one, even spelled false, would meet this
	// operation's unknown-parameter refusal rather than a direction.
	if sent.Has("reverse") {
		t.Errorf("the pushdown sent reverse=%q; listIssues publishes no `reverse` parameter", sent.Get("reverse"))
	}

	down := c.listSpy(t, downLevel)
	fallback, err := down.reader(t).List(ctx, req)
	if err != nil {
		t.Fatalf("List against a server without the capability: %v", err)
	}
	for i, query := range down.sent {
		if query.Has("sort") || query.Has("reverse") {
			t.Errorf("down-level request %d of %d carried sort=%q reverse=%q; a server that does not advertise "+
				"%q must be sent neither", i, down.dials(), query.Get("sort"), query.Get("reverse"), wire.CapListSort)
		}
	}
	// Checked AFTER the absence, not before it, so that a client which had
	// stopped consulting the capability at all reports the key it wrongly sent
	// rather than only the round trip it wrongly saved.
	if down.dials() < 2 {
		t.Errorf("the down-level arm took %d requests over %d rows at %d per page; it did not walk, so the "+
			"absence checked above is the absence of a single request rather than of a walk",
			down.dials(), len(ids), walkPageSize)
	}

	for name, page := range map[string]issueops.IssuePage{"the pushdown": got, "the fallback walk": fallback} {
		if !slices.Equal(pageIDs(page), wantPage) {
			t.Errorf("%s answered %v, want %v", name, pageIDs(page), wantPage)
		}
		if !page.HasMore {
			t.Errorf("%s hid two rows without reporting HasMore", name)
		}
	}
}

// TestServedReaderListWeldedOrderKeepsSendingNoSortParameter is the `want == 0`
// conjunct, which is the only one of the four that is not about correctness.
//
// The server would accept `sort=created` and answer identically — it is the
// order it serves anyway — so nothing here would be WRONG. What the conjunct
// buys is that the request family which predates the parameter keeps sending no
// parameter: an un-reversed created-order page is already one round trip
// (the walk stops on the over-fetched row), so naming the order would buy no
// request and would newly make every such `bd list` depend on a server-side
// code path that did not exist a release ago. That is a compatibility surface
// taken on for nothing, and this case is what keeps it from being taken on by
// accident.
//
// The reversed arm is here to keep the conjunct NARROW rather than merely
// present: created-DESCENDING is not the order the wire serves and the walk
// cannot stop early on it — but it does not push down either (S3
// reconciliation, 2026-10). listIssues publishes no
// `reverse` parameter at all, so there is no wire shape for "the other
// direction of an order this operation does serve"; listSortPushdownEligible
// refuses every reversed request regardless of SortBy, and this arm walks to
// exhaustion and re-sorts client-side exactly as an unpublished SortBy would.
//
// GOES RED when `want == 0` is dropped from the gate: the first arm sends
// sort=created and asks for the caller's two rows rather than the walk's three.
// It also goes red if listSortPushdownEligible ever stopped refusing a
// reversed request: the second arm's dial count would drop as the walk gave
// way to a single pushdown request, and the key-by-key absence check below
// would catch the `reverse` key that request sent.
func TestServedReaderListWeldedOrderKeepsSendingNoSortParameter(t *testing.T) {
	ctx := t.Context()
	c := composition(t)
	scope := listScope("welded")

	base := time.Now().UTC().Truncate(time.Second).Add(-17 * time.Hour)
	ids := map[string]string{}
	for i, tag := range []string{"a", "b", "c", "d"} {
		ids[tag] = listID("welded", tag)
		seedListIssue(t, ctx, c, ids[tag], scope, base.Add(time.Duration(i)*time.Minute), 2)
	}
	advertised, _ := listSortHandshakes(ctx, t, c)

	forward := c.listSpy(t, advertised)
	page, err := forward.reader(t).List(ctx, issueops.ListRequest{
		Labels: []string{scope}, SortBy: wireListSort, Limit: ptrTo(2),
	})
	if err != nil {
		t.Fatalf("List --sort created --limit 2: %v", err)
	}
	if want := []string{ids["d"], ids["c"]}; !slices.Equal(pageIDs(page), want) {
		t.Errorf("List --sort created --limit 2 = %v, want %v", pageIDs(page), want)
	}
	if !page.HasMore {
		t.Error("List --sort created --limit 2 hid two rows without reporting HasMore")
	}
	if forward.dials() != 1 {
		t.Fatalf("the welded order took %d requests; the walk already stops on the over-fetched row, so this "+
			"case's premise — that the pushdown would save nothing here — is what changed", forward.dials())
	}
	sent := forward.sent[0]
	if sent.Has("sort") || sent.Has("reverse") {
		t.Errorf("the welded order sent sort=%q reverse=%q; the request family that predates the parameter "+
			"must keep sending neither", sent.Get("sort"), sent.Get("reverse"))
	}
	if got := sent.Get("limit"); got != "3" {
		t.Errorf("the welded order asked for limit=%q, want the walk's own over-fetch of 3 (the caller's two "+
			"rows plus the row that decides HasMore)", got)
	}

	// The same key REVERSED is a different order from the one the wire serves,
	// and listIssues has no parameter that names the reversed direction of
	// anything — so this walks to exhaustion (one request: four seeded rows
	// is well under the walk's own page size) and gets re-sorted client-side,
	// the same leg an unpublished SortBy takes.
	backward := c.listSpy(t, advertised)
	page, err = backward.reader(t).List(ctx, issueops.ListRequest{
		Labels: []string{scope}, SortBy: wireListSort, Reverse: true, Limit: ptrTo(2),
	})
	if err != nil {
		t.Fatalf("List --sort created --reverse --limit 2: %v", err)
	}
	if want := []string{ids["a"], ids["b"]}; !slices.Equal(pageIDs(page), want) {
		t.Errorf("List --sort created --reverse --limit 2 = %v, want %v", pageIDs(page), want)
	}
	if backward.dials() != 1 {
		t.Fatalf("the reversed welded order took %d requests, want the one the walk needs to see all four "+
			"seeded rows", backward.dials())
	}
	sent = backward.sent[0]
	if sent.Has("sort") || sent.Has("reverse") {
		t.Errorf("the reversed welded order sent sort=%q reverse=%q; listIssues publishes no `reverse` "+
			"parameter, so a reversed request must walk and sort client-side like any other order this "+
			"operation cannot push down", sent.Get("sort"), sent.Get("reverse"))
	}
	if got := sent.Get("limit"); got != strconv.Itoa(walkPageSize) {
		t.Errorf("the reversed welded order asked for limit=%q, want the walk's own page size %d", got, walkPageSize)
	}
}

// TestServedReaderListUnlimitedStaysAWalk is the `limit > 0` conjunct: an
// unlimited read has to cross every matching row whoever orders them, so it
// keeps the fixed-size walk and never names an order on the wire.
//
// GOES RED — and this is worth being exact about, because the obvious red is
// not the one that fires — when `limit > 0` is dropped from the gate. The
// ANSWER stays correct: the leg would send `limit=0`, and this composition
// binds loopback, where the server permits an unlimited read, so it would come
// back whole and in the right order. What changes is the two things asserted
// below and nothing else: the request count collapses to one, and the requests
// start carrying `sort`. Against a NON-loopback server the same mutation is an
// outright 400, which is the cost this conjunct is really avoiding.
func TestServedReaderListUnlimitedStaysAWalk(t *testing.T) {
	ctx := t.Context()
	c := composition(t)
	scope := listScope("unlimited")

	restore := walkPageSize
	walkPageSize = 3
	t.Cleanup(func() { walkPageSize = restore })

	base := time.Now().UTC().Truncate(time.Second).Add(-13 * time.Hour)
	const seeded = 7
	for i := range seeded {
		seedListIssue(t, ctx, c, listID("unlimited", fmt.Sprintf("%02d", i)), scope,
			base.Add(time.Duration(i)*time.Minute), i%3)
	}

	advertised, _ := listSortHandshakes(ctx, t, c)
	spy := c.listSpy(t, advertised)
	page, err := spy.reader(t).List(ctx, issueops.ListRequest{
		Labels: []string{scope}, SortBy: "priority", Limit: ptrTo(0),
	})
	if err != nil {
		t.Fatalf("List --limit 0 --sort priority: %v", err)
	}
	if len(page.Items) != seeded {
		t.Errorf("an unlimited list returned %d rows, want the %d seeded ones: %v", len(page.Items), seeded, pageIDs(page))
	}
	if page.HasMore {
		t.Error("an exhausted unlimited read reported HasMore")
	}
	if spy.dials() != 3 {
		t.Errorf("the unlimited read took %d requests, want the three that %d rows at %d per page cost; "+
			"one means the pushdown claimed a leg that is not bounded", spy.dials(), seeded, walkPageSize)
	}
	for i, query := range spy.sent {
		if query.Has("sort") {
			t.Errorf("unlimited request %d carried sort=%q; an unbounded read names no order on the wire",
				i, query.Get("sort"))
		}
	}
}

// TestServedReaderListMaxRowsUnderPushdown is ledger row L15's owning proof,
// and it pins a CONVERGENCE the pushdown produced rather than a divergence.
//
// L15 says the http client's MaxRows cap counts WIRE ROWS FETCHED while local
// mode's counts the window the query opened, so a cap roomier than the caller's
// limit could fire here and never fire locally. On the pushdown leg that gap is
// gone: the leg asks for min(limit, MaxRows+1), which is the local expression,
// so a cap at or above the limit cannot fire. The first half asserts that
// against the reference store, and the down-level arm is the CONTROL that keeps
// the row honest — it shows the divergence is still there on the walk leg L15
// now names, so a green here is a statement about which leg ran and not about
// whether the cap works at all.
//
// The second half is the case the pushdown must NOT change: a cap tighter than
// the limit still refuses, with the same Found/Cap/Source triple `bd list`
// classifies into exit 2.
//
// The third half is the BOUNDARY the first two straddle without touching: a
// result set exactly EQUAL to the cap. Both arms above have matches either
// above the cap or below it, so `>` and `>=` in the overage check answer them
// identically, and the defect `>=` would ship is user-visible.
//
// The fourth is the matrix over the PUBLISHED SORT VOCABULARY, which is what
// keeps the convergence claim from being a claim about `priority` alone. The
// claim rests on min(limit, MaxRows+1) being the local expression, and that is
// false for a Go-side sort, where workapi.SQLLimit pushes 0 down instead of the
// limit. Ranging over the enum rather than naming `id` is deliberate: a second
// Go-side key added to sqlbuild.IsGoSideSort tomorrow must not reopen the hole
// silently.
//
// GOES RED when sortedPage sizes its request from the limit alone (dropping the
// MaxRows term): the tight-cap arm fetches five rows under a cap of two and
// reports Found = 5 instead of the single overage row. GOES RED when the
// overage check reads `>=`: the at-cap arm refuses a page it should serve. GOES
// RED when walkIssues' gate stops excluding a capped Go-side sort: the matrix
// arm answers where the reference store refuses.
func TestServedReaderListMaxRowsUnderPushdown(t *testing.T) {
	ctx := t.Context()
	c := composition(t)
	scope := listScope("maxrows-pushdown")

	local, err := c.reference.IssueReader()
	if err != nil {
		t.Fatalf("reference IssueReader(): %v", err)
	}

	base := time.Now().UTC().Truncate(time.Second).Add(-15 * time.Hour)
	const seeded = 6
	for i := range seeded {
		seedListIssue(t, ctx, c, listID("maxrows-pushdown", fmt.Sprintf("%02d", i)), scope,
			base.Add(time.Duration(i)*time.Minute), i%3)
	}
	advertised, downLevel := listSortHandshakes(ctx, t, c)

	// A cap ROOMIER than the limit. Local mode never opens a window wider than
	// the limit, so it cannot exceed a cap above it.
	roomy := issueops.ListRequest{
		Labels: []string{scope}, SortBy: "priority", Limit: ptrTo(3),
		MaxRows: 4, MaxRowsSource: "--max-rows",
	}
	want, err := local.List(ctx, roomy)
	if err != nil {
		t.Fatalf("the reference store refused a cap above the limit (%v); L15's convergence claim rests on it "+
			"not doing that, so this row's premise is wrong rather than the client", err)
	}
	push := c.listSpy(t, advertised)
	got, err := push.reader(t).List(ctx, roomy)
	if err != nil {
		t.Fatalf("List under a cap above the limit: %v, want the reference store's answer", err)
	}
	if !slices.Equal(pageIDs(got), pageIDs(want)) {
		t.Errorf("under a roomy cap the pushdown answered %v and the reference %v", pageIDs(got), pageIDs(want))
	}
	if got.HasMore != want.HasMore {
		t.Errorf("under a roomy cap the pushdown reported HasMore = %v and the reference %v", got.HasMore, want.HasMore)
	}

	// THE CONTROL. Same request, same cap, the leg L15 still describes: the
	// walk has to see every row before it can order them, so it fetches past a
	// cap the local window never reaches and refuses.
	down := c.listSpy(t, downLevel)
	if _, err := down.reader(t).List(ctx, roomy); err == nil {
		t.Error("the down-level walk accepted a cap above the limit; if the walk leg no longer diverges from " +
			"local mode here, ledger row L15 is retired rather than narrowed and this control is the wrong shape")
	} else {
		var tooMany *storageops.ErrTooManyRows
		if !errors.As(err, &tooMany) {
			t.Errorf("the down-level walk failed with %v, want the *ErrTooManyRows L15 describes", err)
		}
	}

	// A cap TIGHTER than the limit still refuses, and the overage it reports is
	// one row, because the request asked for exactly one past the cap.
	_, err = push.reader(t).List(ctx, issueops.ListRequest{
		Labels: []string{scope}, SortBy: "priority", Limit: ptrTo(5),
		MaxRows: 2, MaxRowsSource: "--max-rows",
	})
	var tooMany *storageops.ErrTooManyRows
	if !errors.As(err, &tooMany) {
		t.Fatalf("List under a cap below the limit = %v, want *ErrTooManyRows", err)
	}
	if tooMany.Cap != 2 || tooMany.Found != 3 || tooMany.Source != "--max-rows" {
		t.Errorf("the pushdown's cap error is {Found %d, Cap %d, Source %q}, want {3, 2, \"--max-rows\"}: "+
			"the cap plus the single overage row that proves it fired", tooMany.Found, tooMany.Cap, tooMany.Source)
	}
	// And the same request against the reference store, so "the client refuses"
	// is checkably the same refusal local mode makes rather than a private one.
	_, err = local.List(ctx, issueops.ListRequest{
		Labels: []string{scope}, SortBy: "priority", Limit: ptrTo(5),
		MaxRows: 2, MaxRowsSource: "--max-rows",
	})
	var localTooMany *storageops.ErrTooManyRows
	if !errors.As(err, &localTooMany) {
		t.Fatalf("the reference store answered a cap below the limit with %v, want *ErrTooManyRows", err)
	}
	if localTooMany.Found != tooMany.Found || localTooMany.Cap != tooMany.Cap {
		t.Errorf("the pushdown reports {Found %d, Cap %d} and the reference {Found %d, Cap %d}",
			tooMany.Found, tooMany.Cap, localTooMany.Found, localTooMany.Cap)
	}

	// THE BOUNDARY: a result set exactly EQUAL to the cap has not exceeded it.
	// It gets a scope of its own because it needs the match count to BE the
	// cap, and the six rows above cannot supply that alongside a limit wider
	// than the cap — which is the second half of the shape, since a page that
	// the limit truncated would make the cap moot.
	exact := listScope("maxrows-exact")
	const atCapRows = 2
	for i := range atCapRows {
		seedListIssue(t, ctx, c, listID("maxrows-exact", fmt.Sprintf("%02d", i)), exact,
			base.Add(time.Duration(i)*time.Minute), i)
	}
	atCap := issueops.ListRequest{
		Labels: []string{exact}, SortBy: "priority", Limit: ptrTo(5),
		MaxRows: atCapRows, MaxRowsSource: "--max-rows",
	}
	// THE PREMISE, checked out loud: the fixture has to put the match count
	// exactly ON the cap and under the limit. If it drifted either way the
	// assertion below would still pass against an overage check reading `>=`,
	// which is the only defect this arm exists to catch.
	wantAtCap, err := local.List(ctx, atCap)
	if err != nil {
		t.Fatalf("the reference store refused a result set exactly equal to the cap (%v); a cap is a bound the "+
			"result set may reach, so this arm's premise is wrong rather than the client", err)
	}
	if len(wantAtCap.Items) != atCapRows || wantAtCap.HasMore {
		t.Fatalf("the reference answered the at-cap request with %d rows (HasMore %v), want exactly the %d seeded "+
			"ones: without matches == MaxRows < limit this arm cannot tell `>` from `>=`",
			len(wantAtCap.Items), wantAtCap.HasMore, atCapRows)
	}
	gotAtCap, err := push.reader(t).List(ctx, atCap)
	if err != nil {
		t.Fatalf("List over exactly MaxRows matching rows under a wider limit = %v, want the %v the reference "+
			"answered with; a result set that REACHES the cap has not exceeded it, and refusing here turns "+
			"`bd list --limit 5 --max-rows 2` over two rows into exit 2", err, pageIDs(wantAtCap))
	}
	if !slices.Equal(pageIDs(gotAtCap), pageIDs(wantAtCap)) {
		t.Errorf("at the cap the pushdown answered %v and the reference %v", pageIDs(gotAtCap), pageIDs(wantAtCap))
	}
	if gotAtCap.HasMore {
		t.Error("a page holding every matching row reported HasMore")
	}

	// THE MATRIX THIS RAN AGAINST bd-enterprise's listIssues IS NOT RUNNABLE
	// HERE (S3 reconciliation, 2026-10): L15's Go-side half
	// — "a capped Go-side sort has to stay a walk" — needs a published `sort`
	// value sqlbuild.IsGoSideSort accepts (today, `id`), and
	// GET /v0/beads/issues's own vocabulary is CLOSED to the two keyset orders
	// this wire can page at all, `created` and `priority` (see the operation's
	// `sort` parameter doc: "deliberately smaller than the nine values
	// `bd list --sort` and GET /v0/beads/issues:query take" — every value this
	// endpoint accepts is SQL-expressible by construction, so
	// publishedParamEnum(wire.OpListIssues, "sort") can never contain a
	// Go-side member for this matrix to range over).
	//
	// This does not leave L15's Go-side claim unpinned, and it does not weaken
	// what the matrix proved for the wire bd-enterprise ran it against: it
	// narrows the claim to a stronger, unconditional one for the OSS listIssues
	// operation specifically. listSortPushdownEligible refuses pushdown for any
	// SortBy outside {"", wireListSort, flaglessListSort} UNCONDITIONALLY — not
	// only under a cap — so a Go-side order such as "id" never reaches
	// sortedPage at all, capped or not. TestServedReaderListAppliesTheClientSide
	// DisplayOrder already pins that walk-and-client-sort path end to end for
	// SortBy "id", and TestServedReaderListFallsBackToTheWalkWithoutTheCapability
	// pins the no-`sort`-on-the-wire half of it. What is left of this test is
	// everything above: the roomy cap, the tight cap and the exact-cap boundary,
	// all exercised over the one Go-side-free vocabulary this operation actually
	// publishes.
}

// TestServedReaderListOrderMatchesTheReferenceStore is the dual run that makes
// the client comparator checkable rather than plausible: the SAME ListRequest is
// answered by the reference store's own reader and by the client, and the two
// orders have to be identical for every sort the vocabulary publishes —
// INCLUDING the flagless default, which is the one nobody names and everybody
// sees (priority ASC, created DESC, id ASC).
//
// TWO OF THE ROWS ARE CLOSED, and that is what makes the `closed` sort key mean
// something. It is one of the two NULLABLE sort columns, and sqlbuild.Less
// carries a branch of its own for them — MySQL NULL-first semantics, so a NULL
// sorts first ascending and last descending — which a corpus of open rows can
// never reach, because every ClosedAt is nil and the comparator returns on the
// tie. With one closed row the branch is exercised against a real ORDER BY on
// the far side; with two it is exercised against another closed row as well.
// The listing needs AllFlag to see them at all, since a closed row is what the
// default status exclusions exist to hide.
func TestServedReaderListOrderMatchesTheReferenceStore(t *testing.T) {
	ctx := t.Context()
	c, reader := servedReader(t)
	scope := listScope("dual")

	local, err := c.reference.IssueReader()
	if err != nil {
		t.Fatalf("reference IssueReader(): %v", err)
	}

	base := time.Now().UTC().Truncate(time.Second).Add(-7 * time.Hour)
	// Priorities and creation instants deliberately disagree, and two rows share
	// a priority so the tie-break tail is reachable.
	for i, seed := range []struct {
		tag      string
		priority int
	}{
		{"delta", 2}, {"alpha", 0}, {"charlie", 2}, {"bravo", 1},
	} {
		seedListIssue(t, ctx, c, listID("dual", seed.tag), scope, base.Add(time.Duration(i)*time.Minute), seed.priority)
	}
	// The two closed rows, with distinct ClosedAt instants so the `closed` sort
	// orders them against EACH OTHER as well as against the four nil ones.
	for i, seed := range []struct {
		tag      string
		priority int
	}{
		{"echo", 1}, {"foxtrot", 3},
	} {
		at := base.Add(time.Duration(4+i) * time.Minute)
		closedAt := at.Add(time.Duration(i+1) * time.Hour)
		issue := &types.Issue{
			ID: listID("dual", seed.tag), Title: listID("dual", seed.tag),
			Status: types.StatusClosed, Priority: seed.priority, IssueType: types.TypeTask,
			Labels: []string{scope}, CreatedAt: at, UpdatedAt: at, ClosedAt: &closedAt,
		}
		if err := c.seedIssue(ctx, issue, "seed"); err != nil {
			t.Fatalf("seed %s: %v", issue.ID, err)
		}
	}

	// THE PREMISE for the closed leg, checked rather than assumed. If the closed
	// rows were hidden by the default exclusions, or if `closed` did not in fact
	// partition the page by nullity, every comparison below would still pass and
	// the NULL branch would still be unreached — which is the failure mode a
	// seeded row alone does not rule out.
	closedIDs := []string{listID("dual", "echo"), listID("dual", "foxtrot")}
	partitions := map[bool][]string{}
	for _, reverse := range []bool{false, true} {
		page, err := local.List(ctx, issueops.ListRequest{
			Labels: []string{scope}, SortBy: "closed", Reverse: reverse, AllFlag: true, Limit: ptrTo(0),
		})
		if err != nil {
			t.Fatalf("reference List --sort closed (reverse %v): %v", reverse, err)
		}
		ids := pageIDs(page)
		if len(ids) != 6 {
			t.Fatalf("the reference answered --sort closed with %v; this case needs all six seeded rows, "+
				"so AllFlag is not reaching the closed ones and the NULL branch is unreachable", ids)
		}
		partitions[reverse] = ids
	}
	// Compared as SETS, because reversing flips the order WITHIN the partition
	// as well as the partition's position — which is the correct behavior and
	// not what this premise is about.
	sorted := func(ids []string) []string {
		out := append([]string(nil), ids...)
		slices.Sort(out)
		return out
	}
	want := sorted(closedIDs)
	head, tail := partitions[false][:2], partitions[true][len(partitions[true])-2:]
	if !slices.Equal(sorted(head), want) || !slices.Equal(sorted(tail), want) {
		t.Fatalf("--sort closed put %v at the head and %v at the reversed tail, want the two closed rows %v at "+
			"both; this case needs them partitioned to one end so sqlbuild.Less's NULL-first branch decides the order",
			head, tail, closedIDs)
	}

	sorts := []string{"", "priority", "created", "updated", "closed", "status", "id", "title", "type", "assignee"}
	for _, sortBy := range sorts {
		for _, reverse := range []bool{false, true} {
			req := issueops.ListRequest{Labels: []string{scope}, SortBy: sortBy, Reverse: reverse, AllFlag: true, Limit: ptrTo(0)}
			want, err := local.List(ctx, req)
			if err != nil {
				t.Fatalf("reference List (sort %q, reverse %v): %v", sortBy, reverse, err)
			}
			got, err := reader.List(ctx, req)
			if err != nil {
				t.Fatalf("http List (sort %q, reverse %v): %v", sortBy, reverse, err)
			}
			if !slices.Equal(pageIDs(got), pageIDs(want)) {
				t.Errorf("sort %q reverse %v: http answered %v, reference answered %v",
					sortBy, reverse, pageIDs(got), pageIDs(want))
			}
		}
	}

	// THE SAME MATRIX UNDER A TRUNCATING LIMIT, RUN ON BOTH LEGS.
	//
	// Every arm above is unlimited, so all six rows come back and only their
	// ORDER is at stake — which a client that fetched everything and re-sorted
	// satisfies. Under a limit that truncates, the ROW SET is at stake as well,
	// and it is decided by whoever applied the limit. On the pushdown leg that
	// is the server, under the caller's order; on the walk leg it is still this
	// client, after the walk. After S2 those are two code paths through one
	// request, so both are run against the same oracle.
	advertised, downLevel := listSortHandshakes(ctx, t, c)
	legs := map[string]issueops.Reader{
		"pushdown": c.listSpy(t, advertised).reader(t),
		"walk":     c.listSpy(t, downLevel).reader(t),
	}

	// THE PREMISE for the truncation, and it is exactly the ambiguity the
	// unlimited arms do not have: if every sort's top three were the same three
	// rows, "the client returned the reference's rows" would be satisfied by a
	// client that ignored `sort` and truncated the wire's welded order. Counted
	// rather than required per-arm, because some arms coinciding with the
	// created order is correct and expected — `updated` here IS the created
	// order, since nothing has been updated since it was seeded.
	const pageLimit = 3
	welded, err := local.List(ctx, issueops.ListRequest{
		Labels: []string{scope}, SortBy: wireListSort, AllFlag: true, Limit: ptrTo(pageLimit),
	})
	if err != nil {
		t.Fatalf("reference List in the welded order: %v", err)
	}
	weldedTop := sorted(pageIDs(welded))
	discriminating := 0

	for _, sortBy := range sorts {
		for _, reverse := range []bool{false, true} {
			req := issueops.ListRequest{
				Labels: []string{scope}, SortBy: sortBy, Reverse: reverse, AllFlag: true, Limit: ptrTo(pageLimit),
			}
			want, err := local.List(ctx, req)
			if err != nil {
				t.Fatalf("reference List (sort %q, reverse %v, limit %d): %v", sortBy, reverse, pageLimit, err)
			}
			if len(want.Items) != pageLimit || !want.HasMore {
				t.Fatalf("the reference answered (sort %q, reverse %v) with %d of six rows and HasMore %v; "+
					"this leg needs a truncating page", sortBy, reverse, len(want.Items), want.HasMore)
			}
			if !slices.Equal(sorted(pageIDs(want)), weldedTop) {
				discriminating++
			}
			for leg, reader := range legs {
				got, err := reader.List(ctx, req)
				if err != nil {
					t.Fatalf("http %s List (sort %q, reverse %v, limit %d): %v", leg, sortBy, reverse, pageLimit, err)
				}
				if !slices.Equal(pageIDs(got), pageIDs(want)) {
					t.Errorf("sort %q reverse %v limit %d on the %s leg: http answered %v, reference answered %v",
						sortBy, reverse, pageLimit, leg, pageIDs(got), pageIDs(want))
				}
				if got.HasMore != want.HasMore {
					t.Errorf("sort %q reverse %v limit %d on the %s leg: http HasMore = %v, reference = %v",
						sortBy, reverse, pageLimit, leg, got.HasMore, want.HasMore)
				}
			}
		}
	}
	// Exactly two of the twenty arms are expected to coincide with the welded
	// order — `created` and `updated`, both un-reversed, because nothing has
	// been updated since it was seeded — so the count is pinned rather than
	// bounded loosely. A third coincidence is the fixture drifting toward
	// ambiguity, which is the thing this premise exists to catch.
	if want := 2*len(sorts) - 2; discriminating != want {
		t.Errorf("%d of the %d truncated arms chose a different three rows than the welded created order, want %d; "+
			"the fixture has changed how well it discriminates, and every comparison above is only as strong as "+
			"the arms that do", discriminating, 2*len(sorts), want)
	}
}

// TestServedReaderListKeysetPositionSkipsForward is D8 row 1's disposition,
// asserted: a caller-supplied position is honored by paging the created-order
// wire and discarding the rows at or before it. Both halves of the position
// matter — a filter that compared the timestamp alone would drop the
// same-second row, which is exactly how a keyset page loses records.
func TestServedReaderListKeysetPositionSkipsForward(t *testing.T) {
	ctx := t.Context()
	c, reader := servedReader(t)
	scope := listScope("keyset")

	newest, cursor, sameSecond, oldest := listID("keyset", "c"), listID("keyset", "b1"), listID("keyset", "b2"), listID("keyset", "a")
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	cursorAt := base.Add(30 * time.Minute)
	// The priorities run OPPOSITE to the created order on purpose, and the two
	// rows the position discards are the two the priority order would put
	// FIRST. That is what makes the sorted leg below unambiguous: a client that
	// pushed the sort down and dropped the position on the floor answers with
	// the discarded pair, which shares no row with the correct answer.
	for _, seed := range []struct {
		id       string
		at       time.Time
		priority int
	}{
		{newest, base.Add(45 * time.Minute), 0},
		{cursor, cursorAt, 1},
		{sameSecond, cursorAt, 2},
		{oldest, base, 3},
	} {
		seedListIssue(t, ctx, c, seed.id, scope, seed.at, seed.priority)
	}

	page, err := reader.List(ctx, issueops.ListRequest{
		Labels:         []string{scope},
		SortBy:         "created",
		AfterCreatedAt: &cursorAt,
		AfterID:        cursor,
	})
	if err != nil {
		t.Fatalf("List from a keyset position: %v", err)
	}
	if want := []string{sameSecond, oldest}; !slices.Equal(pageIDs(page), want) {
		t.Errorf("List from a keyset position = %v, want %v", pageIDs(page), want)
	}

	// And the position composes with the page bound rather than replacing it:
	// the walk keeps going until it has enough rows PAST the position.
	page, err = reader.List(ctx, issueops.ListRequest{
		Labels: []string{scope}, SortBy: "created",
		AfterCreatedAt: &cursorAt, AfterID: cursor, Limit: ptrTo(1),
	})
	if err != nil {
		t.Fatalf("List from a keyset position --limit 1: %v", err)
	}
	if want := []string{sameSecond}; !slices.Equal(pageIDs(page), want) {
		t.Errorf("List from a keyset position --limit 1 = %v, want %v", pageIDs(page), want)
	}
	if !page.HasMore {
		t.Error("List from a keyset position --limit 1 hid a row without reporting HasMore")
	}

	// AND THE POSITION SURVIVES A SORT. This is the `keep == nil` conjunct of
	// the pushdown gate: the filter is a prefix discard over CREATED-ORDER
	// arrival, so a page some other order truncated would run it against rows
	// whose replacements were never fetched. The request below is bounded and
	// in an order the wire can now be told, which is every other condition the
	// fast leg wants; only the position keeps it a walk.
	//
	// The premise is checked out loud rather than reasoned about: the same
	// request WITHOUT the position answers with the two rows the position
	// discards, so an answer of [sameSecond oldest] cannot be produced by a
	// client that forgot it.
	//
	// It is measured through the REFERENCE store rather than the subject. Asked
	// of the subject, a client-side ordering bug would fire this Fatalf with a
	// message blaming the fixture, and the keyset assertion it guards would
	// never run — a premise that reports the subject's defect as its own drift
	// is worse than no premise.
	sortedFromPosition := issueops.ListRequest{
		Labels: []string{scope}, SortBy: "priority", Limit: ptrTo(2),
		AfterCreatedAt: &cursorAt, AfterID: cursor,
	}
	unpositioned := sortedFromPosition
	unpositioned.AfterCreatedAt, unpositioned.AfterID = nil, ""
	referenceReader, err := c.reference.IssueReader()
	if err != nil {
		t.Fatalf("reference IssueReader(): %v", err)
	}
	ignored, err := referenceReader.List(ctx, unpositioned)
	if err != nil {
		t.Fatalf("reference List --sort priority --limit 2 with no position: %v", err)
	}
	if want := []string{newest, cursor}; !slices.Equal(pageIDs(ignored), want) {
		t.Fatalf("the reference answered --sort priority --limit 2 with no position with %v, want %v; the fixture "+
			"no longer puts the discarded rows first in priority order, so the assertion below could not tell a "+
			"dropped position apart from an honored one", pageIDs(ignored), want)
	}

	page, err = reader.List(ctx, sortedFromPosition)
	if err != nil {
		t.Fatalf("List from a keyset position --sort priority: %v", err)
	}
	if want := []string{sameSecond, oldest}; !slices.Equal(pageIDs(page), want) {
		t.Errorf("List from a keyset position --sort priority = %v, want %v", pageIDs(page), want)
	}
	if page.HasMore {
		t.Error("two rows past the position under a limit of two reported HasMore")
	}
}

// TestServedReaderListWalksMultiplePages drives the cursor loop itself: the
// per-page size is shrunk so three rows take three round trips, and the answer
// still has to be the whole set, once each, in order.
func TestServedReaderListWalksMultiplePages(t *testing.T) {
	ctx := t.Context()
	c, reader := servedReader(t)
	scope := listScope("pages")

	restore := walkPageSize
	walkPageSize = 1
	t.Cleanup(func() { walkPageSize = restore })

	base := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
	var ids []string
	for i, tag := range []string{"a", "b", "c", "d", "e"} {
		id := listID("pages", tag)
		ids = append(ids, id)
		seedListIssue(t, ctx, c, id, scope, base.Add(time.Duration(i)*time.Minute), 2)
	}

	page, err := reader.List(ctx, issueops.ListRequest{Labels: []string{scope}, SortBy: "id", Limit: ptrTo(0)})
	if err != nil {
		t.Fatalf("List across pages: %v", err)
	}
	if want := ids; !slices.Equal(pageIDs(page), want) {
		t.Errorf("List across pages = %v, want %v exactly once each", pageIDs(page), want)
	}
	if page.HasMore {
		t.Error("an exhausted unlimited walk reported HasMore")
	}
}

// TestServedReaderListMultiPageWalkHasNoSnapshotIsolation is ledger row L3's
// owning proof, and it pins a DEGRADATION rather than a promise.
//
// The cursor pins a POSITION, not a snapshot. A row written between page N and
// page N+1 lands ahead of the walk in created-DESC order — where the walk has
// already been — so it is absent from the answer, and local mode's single query
// could never miss it. What the walk still owes is the half that would be a bug
// rather than a documented difference: it terminates, and it returns no row
// twice.
func TestServedReaderListMultiPageWalkHasNoSnapshotIsolation(t *testing.T) {
	ctx := t.Context()
	c, _ := servedReader(t)
	scope := listScope("interleave")

	restore := walkPageSize
	walkPageSize = 1
	t.Cleanup(func() { walkPageSize = restore })

	base := time.Now().UTC().Truncate(time.Second).Add(-4 * time.Hour)
	var seeded []string
	for i, tag := range []string{"a", "b", "c"} {
		id := listID("interleave", tag)
		seeded = append(seeded, id)
		seedListIssue(t, ctx, c, id, scope, base.Add(time.Duration(i)*time.Minute), 2)
	}
	late := listID("interleave", "late")

	var dials int
	interleaving := c.tamperedClient(t, func(req *wire.Request) {
		if req.Op != wire.OpListIssues {
			return
		}
		dials++
		if dials == 2 {
			// Newer than every seeded row, so its place in the created-DESC
			// order is BEHIND the walk: the page it belonged on has already
			// been fetched.
			seedListIssue(t, ctx, c, late, scope, time.Now().UTC().Truncate(time.Second), 2)
		}
	})
	reader, err := interleaving.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}

	page, err := reader.List(ctx, issueops.ListRequest{Labels: []string{scope}, SortBy: "created", Limit: ptrTo(0)})
	if err != nil {
		t.Fatalf("List across an interleaved write: %v", err)
	}
	got := pageIDs(page)
	if dials < 2 {
		t.Fatalf("the walk took %d pages; this case needs at least two for a write to land between them", dials)
	}

	seen := map[string]int{}
	for _, id := range got {
		seen[id]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("the walk returned %s %d times: a keyset walk must not repeat a row", id, n)
		}
	}
	for _, id := range seeded {
		if seen[id] == 0 {
			t.Errorf("the walk lost %s, which existed before it started", id)
		}
	}
	if seen[late] != 0 {
		t.Errorf("the walk returned %s, which was written after it passed that position; "+
			"if the wire has gained snapshot isolation, ledger row L3 is stale rather than this test", late)
	}
}

// TestServedReaderListMaxRowsBoundsWireRowsFetched is D12: the wire publishes no
// max_rows, so the cap is enforced during page accumulation and the exact
// sentinel `bd list` classifies into exit 2 is synthesized here.
func TestServedReaderListMaxRowsBoundsWireRowsFetched(t *testing.T) {
	ctx := t.Context()
	c, reader := servedReader(t)
	scope := listScope("maxrows")

	var ids []string
	for _, tag := range []string{"a", "b", "c"} {
		id := listID("maxrows", tag)
		ids = append(ids, id)
		seedListIssue(t, ctx, c, id, scope, time.Time{}, 2)
	}

	roomy, err := reader.List(ctx, issueops.ListRequest{
		Labels: []string{scope}, SortBy: "created", Limit: ptrTo(0),
		MaxRows: len(ids) + 1, MaxRowsSource: "--max-rows",
	})
	if err != nil {
		t.Fatalf("List under a cap the result set fits inside: %v", err)
	}
	if len(roomy.Items) != len(ids) {
		t.Errorf("List under a roomy cap returned %v, want the three seeded rows", pageIDs(roomy))
	}

	_, err = reader.List(ctx, issueops.ListRequest{
		Labels: []string{scope}, SortBy: "created", Limit: ptrTo(0),
		MaxRows: len(ids) - 1, MaxRowsSource: "--max-rows",
	})
	var tooMany *storageops.ErrTooManyRows
	if !errors.As(err, &tooMany) {
		t.Fatalf("List under a cap the result set exceeds = %v, want *ErrTooManyRows", err)
	}
	if tooMany.Cap != len(ids)-1 {
		t.Errorf("cap error reports Cap = %d, want %d", tooMany.Cap, len(ids)-1)
	}
	if tooMany.Found != tooMany.Cap+1 {
		t.Errorf("cap error reports Found = %d, want the cap plus the one overage row (%d)", tooMany.Found, tooMany.Cap+1)
	}
	if tooMany.Source != "--max-rows" {
		t.Errorf("cap error reports Source = %q, want the attribution the request supplied", tooMany.Source)
	}
}

// TestServedReaderListEmptyPageIsWellFormedUnderALabelScope pins the page shape
// when nothing matches, on both methods that share the type.
func TestServedReaderListEmptyPageIsWellFormedUnderALabelScope(t *testing.T) {
	ctx := t.Context()
	_, reader := servedReader(t)
	scope := listScope("empty-never-seeded")

	listed, err := reader.List(ctx, issueops.ListRequest{Labels: []string{scope}})
	if err != nil {
		t.Fatalf("List matching nothing: %v", err)
	}
	if listed.Items == nil {
		t.Error("an empty page carries a nil Items")
	}
	if len(listed.Items) != 0 || listed.HasMore {
		t.Errorf("List matching nothing = (%v, hasMore %v)", pageIDs(listed), listed.HasMore)
	}

	ready, err := reader.Ready(ctx, issueops.ReadyRequest{Labels: []string{scope}})
	if err != nil {
		t.Fatalf("Ready matching nothing: %v", err)
	}
	if ready.Items == nil || len(ready.Items) != 0 || ready.HasMore {
		t.Errorf("Ready matching nothing = (%v, hasMore %v)", pageIDs(ready), ready.HasMore)
	}
}

// TestServedReaderListStatusORSetUnderALabelScope drives the plural branch of
// ListRequest.Status end to end: a comma-separated set REPLACES the default
// exclusions rather than fighting them, and the wire carries it as one repeated
// `status` parameter the server rejoins. A set of three is deliberate — two
// entries is the smallest IN clause and would pass against a renderer that
// emitted only the first and the last.
func TestServedReaderListStatusORSetUnderALabelScope(t *testing.T) {
	ctx := t.Context()
	c, reader := servedReader(t)
	scope := listScope("status")

	open, inProgress, closed := listID("status", "open"), listID("status", "inprogress"), listID("status", "closed")
	seedListIssue(t, ctx, c, open, scope, time.Time{}, 2)
	for _, seed := range []struct {
		id     string
		status types.Status
	}{{inProgress, types.StatusInProgress}, {closed, types.StatusClosed}} {
		issue := &types.Issue{
			ID: seed.id, Title: seed.id, Status: seed.status, Priority: 2,
			IssueType: types.TypeTask, Labels: []string{scope},
		}
		if err := c.seedIssue(ctx, issue, "seed"); err != nil {
			t.Fatalf("seed %s: %v", seed.id, err)
		}
	}

	for _, test := range []struct {
		name   string
		status string
		want   []string
	}{
		{"a two-status set reaches past the default closed exclusion", "closed,in_progress", []string{closed, inProgress}},
		{"a three-status set is the whole seeded scope", "open,in_progress,closed", []string{open, inProgress, closed}},
		{"whitespace around a member is the caller's, not the query's", " closed , open ", []string{closed, open}},
	} {
		page, err := reader.List(ctx, issueops.ListRequest{Labels: []string{scope}, Status: test.status, Limit: ptrTo(0)})
		if err != nil {
			t.Fatalf("List --status %q (%s): %v", test.status, test.name, err)
		}
		got := append([]string(nil), pageIDs(page)...)
		want := append([]string(nil), test.want...)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("List --status %q = %v, want %v", test.status, pageIDs(page), test.want)
		}
	}
}

// TestServedReaderDoesNotMutateTheCallerRequestOverTheWire is the role's
// request-snapshot tripwire, with the contract's IDFilter dropped. The slices
// are the point: an encoder that normalized labels into the caller's backing
// array would leave the struct header untouched and its CONTENTS changed.
func TestServedReaderDoesNotMutateTheCallerRequestOverTheWire(t *testing.T) {
	ctx := t.Context()
	_, reader := servedReader(t)

	build := func() (issueops.ReadyRequest, issueops.ListRequest, issueops.GetRequest) {
		limit := 5
		return issueops.ReadyRequest{
				Labels:         []string{"Beta", "alpha"},
				LabelsAny:      []string{"gamma", " delta "},
				ExcludeLabels:  []string{"omega"},
				ExcludeTypes:   []string{"chore,epic", " feat "},
				MetadataFields: map[string]string{"kind": "probe"},
				Sort:           "priority",
				Limit:          &limit,
			}, issueops.ListRequest{
				Labels:         []string{"Beta", "alpha"},
				LabelsAny:      []string{"gamma", " delta "},
				ExcludeLabels:  []string{"omega"},
				MetadataFields: map[string]string{"kind": "probe"},
				SortBy:         "id",
				Limit:          &limit,
			}, issueops.GetRequest{ID: listID("nomut", "absent")}
	}
	ready, list, get := build()
	wantReady, wantList, wantGet := build()

	if _, err := reader.Ready(ctx, ready); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if _, err := reader.List(ctx, list); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := reader.Get(ctx, get); !errors.Is(err, issueops.ErrNotFound) {
		t.Fatalf("Get on an absent id: %v", err)
	}

	if !reflect.DeepEqual(ready, wantReady) {
		t.Errorf("Ready mutated the caller's request:\n got %#v\nwant %#v", ready, wantReady)
	}
	if !reflect.DeepEqual(list, wantList) {
		t.Errorf("List mutated the caller's request:\n got %#v\nwant %#v", list, wantList)
	}
	if !reflect.DeepEqual(get, wantGet) {
		t.Errorf("Get mutated the caller's request:\n got %#v\nwant %#v", get, wantGet)
	}
	if *list.Limit != *wantList.Limit {
		t.Errorf("the caller's Limit pointer was written through: %d", *list.Limit)
	}
}

// TestServedReaderRefusesTheFiltersTheWireCannotCarry is refuse-not-drop (L12)
// on the role, and it is the assertion the parked contract cases hand over.
//
// Each of these fields would WIDEN the answer if dropped — the caller asked a
// narrower question than the wire can put — so each has to fail, carry the
// portable sentinel a caller classifies with, and name the field in its ledger
// row so D7's third taxonomy text has something to print.
func TestServedReaderRefusesTheFiltersTheWireCannotCarry(t *testing.T) {
	ctx := t.Context()
	_, reader := servedReader(t)
	cutoff := time.Now().UTC().Add(-time.Hour)
	priority := 1

	for _, test := range []struct {
		field string
		req   issueops.ListRequest
	}{
		{"IDFilter", issueops.ListRequest{IDFilter: listID("refuse", "a")}},
		{"ReadyFlag", issueops.ListRequest{ReadyFlag: true}},
		{"Offset", issueops.ListRequest{Offset: 1}},
		{"PinnedFlag", issueops.ListRequest{PinnedFlag: true}},
		{"PriorityMax", issueops.ListRequest{PriorityMax: &priority}},
		{"UpdatedAfter", issueops.ListRequest{UpdatedAfter: &cutoff}},
		{"TitleContains", issueops.ListRequest{TitleContains: "anything"}},
		{"ExcludeTypes", issueops.ListRequest{ExcludeTypes: []string{"chore"}}},
	} {
		page, err := reader.List(ctx, test.req)
		if err == nil {
			t.Errorf("List with %s answered with %v instead of refusing: a dropped filter widens the set invisibly",
				test.field, pageIDs(page))
			continue
		}
		assertServedRefusal(t, "List with "+test.field, err, test.field)
	}

	if _, err := reader.Ready(ctx, issueops.ReadyRequest{Offset: 1}); err == nil {
		t.Error("Ready with an Offset answered instead of refusing")
	} else {
		assertServedRefusal(t, "Ready with an Offset", err, "Offset")
	}
}

// seedListWisp seeds one EPHEMERAL row through the reference store, carrying
// the same label scope its durable siblings do. The flag is set here rather
// than by the caller for servedEnv.createWisp's reason: the plane is the seed
// hook's decision, not the case's.
func seedListWisp(t *testing.T, ctx context.Context, c *servedComposition, id, scope string, at time.Time, issueType types.IssueType) {
	t.Helper()
	issue := &types.Issue{
		ID: id, Title: id, Status: types.StatusOpen, Priority: 2,
		IssueType: issueType, Labels: []string{scope}, Ephemeral: true,
	}
	if !at.IsZero() {
		issue.CreatedAt = at
		issue.UpdatedAt = at
	}
	if err := c.seedIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
	}
}

// TestServedReaderListIncludeEphemeralMergesThePlanesUnderALabelScope is
// RunReaderListIncludeEphemeralMergesThePlanesIntoOneOrder with the id scope
// swapped for a label one, which is the swap this whole file exists to make:
// the contract case scopes itself with IDFilter and listIssues publishes no id
// parameter (E-ListRequest.IDFilter), so the case is parked next door and its
// behavior is pinned here instead.
//
// WHAT IT IS ABOUT is not admission but ORDER. That the flag admits wisps at
// all is already covered by RunReaderListDefaultExclusionsAndTheirOverrides,
// which reads its answer as a set — so a body that appended one plane after the
// other passes it. Here the planes ALTERNATE second by second, so a
// concatenation is a different SEQUENCE, a Limit landing inside the
// interleaving keeps a different SET, and the keyset walk resumes from
// positions on both sides of the merge.
//
// OVER THIS WIRE the merge is the SERVER's — one query family, merge-sorted
// before the trim — and what this leg adds is that the client's own pager walks
// the merged order without re-segmenting it: the page it accumulates crosses
// the plane boundary, and the position it carries forward names no plane.
//
// THE WISP ROWS KEEP THEIR IDENTITY, asserted separately from the order,
// because a merged page that came back with every row looking durable would
// satisfy every id comparison here. `ephemeral` is the member that says which
// plane a row came from, and it has to survive the JSON round trip.
func TestServedReaderListIncludeEphemeralMergesThePlanesUnderALabelScope(t *testing.T) {
	ctx := t.Context()
	c, reader := servedReader(t)
	scope := listScope("eph")

	firstSecond := time.Now().UTC().Truncate(time.Second).Add(-3 * time.Hour)
	seeds := []struct {
		tag       string
		ephemeral bool
	}{
		{"i1", false}, {"w1", true}, {"i2", false},
		{"w2", true}, {"i3", false}, {"w3", true},
	}
	var merged, durableOnly []string
	wisps := map[string]bool{}
	for i, seed := range seeds {
		id := listID("eph", seed.tag)
		at := firstSecond.Add(time.Duration(i) * time.Second)
		if seed.ephemeral {
			seedListWisp(t, ctx, c, id, scope, at, types.TypeTask)
			wisps[id] = true
		} else {
			seedListIssue(t, ctx, c, id, scope, at, 2)
			durableOnly = append(durableOnly, id)
		}
		merged = append(merged, id)
	}
	// The wire's order is (created_at DESC, id ASC), so both expectations run
	// youngest first — the reverse of the seeding order.
	slices.Reverse(merged)
	slices.Reverse(durableOnly)

	scoped := issueops.ListRequest{Labels: []string{scope}, SortBy: "created", Limit: ptrTo(0)}
	durable, err := reader.List(ctx, scoped)
	if err != nil {
		t.Fatalf("List over the scope without the flag: %v", err)
	}
	if got := pageIDs(durable); !slices.Equal(got, durableOnly) {
		t.Fatalf("List without IncludeEphemeral = %v, want the durable rows %v", got, durableOnly)
	}

	admitted := scoped
	admitted.IncludeEphemeral = true
	all, err := reader.List(ctx, admitted)
	if err != nil {
		t.Fatalf("List --include-ephemeral: %v", err)
	}
	if got := pageIDs(all); !slices.Equal(got, merged) {
		t.Fatalf("List --include-ephemeral = %v, want the two planes merged into one order %v", got, merged)
	}

	// The identity half. A row's plane is a fact about the row, and the merged
	// page is the one place a client can lose it.
	for _, item := range all.Items {
		if item == nil || item.Issue == nil {
			t.Fatalf("List --include-ephemeral returned a nil row")
		}
		if got, want := item.Issue.Ephemeral, wisps[item.ID]; got != want {
			t.Errorf("%s came back with ephemeral = %v, want %v: the merged page lost which plane the row is on",
				item.ID, got, want)
		}
	}

	// Every bound from inside the first plane's run to past the end. The
	// interesting ones are the cuts that land between a wisp and the durable row
	// next to it, which is where a page built one plane at a time keeps the
	// wrong row.
	for limit := 1; limit <= len(merged)+1; limit++ {
		bounded := admitted
		bounded.Limit = ptrTo(limit)
		page, err := reader.List(ctx, bounded)
		if err != nil {
			t.Errorf("List --include-ephemeral --limit %d: %v", limit, err)
			continue
		}
		want := merged[:min(limit, len(merged))]
		if got := pageIDs(page); !slices.Equal(got, want) {
			t.Errorf("List --include-ephemeral --limit %d = %v, want %v", limit, got, want)
		}
		if wantMore := limit < len(merged); page.HasMore != wantMore {
			t.Errorf("List --include-ephemeral --limit %d reported HasMore = %v, want %v", limit, page.HasMore, wantMore)
		}
	}

	// The keyset walk across the merge. The position is a created-order pair and
	// neither half of it names a plane, so a walk that resumes correctly on one
	// plane and skips the other is the failure this looks for.
	const pageSize = 2
	var walked []string
	seen := map[string]bool{}
	var afterCreatedAt *time.Time
	afterID := ""
	for page := 0; page <= len(merged); page++ {
		req := admitted
		req.Limit = ptrTo(pageSize)
		req.AfterCreatedAt = afterCreatedAt
		req.AfterID = afterID
		got, err := reader.List(ctx, req)
		if err != nil {
			t.Fatalf("List --include-ephemeral page %d: %v", page, err)
		}
		if len(got.Items) == 0 {
			break
		}
		if len(got.Items) > pageSize {
			t.Fatalf("List --include-ephemeral page %d answered %d rows over a Limit of %d",
				page, len(got.Items), pageSize)
		}
		for _, item := range got.Items {
			if seen[item.ID] {
				t.Fatalf("List --include-ephemeral page %d repeated %s: a position that crossed the plane "+
					"boundary re-delivered a row it had already handed out", page, item.ID)
			}
			seen[item.ID] = true
			walked = append(walked, item.ID)
		}
		last := got.Items[len(got.Items)-1]
		at := last.CreatedAt.UTC()
		afterCreatedAt = &at
		afterID = last.ID
	}
	if !slices.Equal(walked, merged) {
		t.Errorf("the keyset walk over the merged planes delivered %v, want the one-shot sequence %v "+
			"with nothing dropped and nothing repeated", walked, merged)
	}
}

// TestServedReaderListMessageWispsNeedTheInfraIntentAndNotJustThePlane drives
// the program-founding fact of the external-API store end to end, and it
// SHARPENS it: the four quadrants were measured here rather than assumed, and
// what came back is not quite what the bead that ordered this wave predicted.
//
// MAIL IS A WISP OF AN INFRA TYPE. A message bead is `type: message`, and
// message is one of the three DEFAULT INFRA TYPES (domain/infra_types.go), so a
// default listing hides it twice over: the wisp plane is skipped, and the infra
// types are excluded from the durable leg.
//
// WHAT THE FOUR ARMS MEASURE (workapi.BuildListFilter's plane branch is the
// reason, and it is one branch):
//
//	neither         the durable task alone
//	include_ephemeral   admits the PLANE and drops no type exclusion, so a task
//	                    wisp appears and a MESSAGE wisp does not
//	include_infra       drops the type exclusions AND admits the plane, because
//	                    it is one of the three conditions on that same branch —
//	                    so it answers with everything
//	both                identical to include_infra alone
//
// SO THE LOAD-BEARING MEMBER FOR MAIL IS `include_infra`, not the plane bit: a
// TierBoth read that sent `include_ephemeral` alone reads an EMPTY MAILBOX and
// believes it, and one that sent `include_infra` alone is already correct.
// Sending both is still the honest spelling — it says the plane and the
// vocabulary out loud, and it does not depend on infra's plane admission
// staying a side effect of one branch — but the two are the same ANSWER today,
// and a client that dropped the plane bit would not fail on this fixture. The
// arm that fails on a dropped `include_infra` is the third and fourth, and the
// arm that fails on a dropped `include_ephemeral` is the second.
func TestServedReaderListMessageWispsNeedTheInfraIntentAndNotJustThePlane(t *testing.T) {
	ctx := t.Context()
	c, reader := servedReader(t)
	scope := listScope("mail")

	task := listID("mail", "task")
	durableMessage := listID("mail", "durable-msg")
	taskWisp := listID("mail", "wisp-task")
	mail := listID("mail", "wisp-msg")

	at := time.Now().UTC().Truncate(time.Second).Add(-90 * time.Minute)
	seedListIssue(t, ctx, c, task, scope, at, 2)
	// The durable message row is what separates the TYPE exclusion from the
	// plane one; the task wisp is what separates the plane admission from the
	// type one. Without both, two of the four arms would answer the same set
	// and the quadrant would not be a quadrant.
	durable := &types.Issue{
		ID: durableMessage, Title: durableMessage, Status: types.StatusOpen, Priority: 2,
		IssueType: types.IssueType("message"), Labels: []string{scope},
		CreatedAt: at.Add(time.Second), UpdatedAt: at.Add(time.Second),
	}
	if err := c.seedIssue(ctx, durable, "seed"); err != nil {
		t.Fatalf("seed %s: %v", durableMessage, err)
	}
	seedListWisp(t, ctx, c, taskWisp, scope, at.Add(2*time.Second), types.TypeTask)
	seedListWisp(t, ctx, c, mail, scope, at.Add(3*time.Second), types.IssueType("message"))

	everything := []string{mail, taskWisp, durableMessage, task}
	for _, test := range []struct {
		name  string
		req   issueops.ListRequest
		want  []string
		about string
	}{
		{
			name: "neither intent",
			req:  issueops.ListRequest{},
			want: []string{task},
			about: "a default listing hides the message rows on both legs at once, and the wisp " +
				"plane with them",
		},
		{
			name:  "the plane intent alone",
			req:   issueops.ListRequest{IncludeEphemeral: true},
			want:  []string{taskWisp, task},
			about: "include_ephemeral admits the PLANE and drops no type exclusion, so the task wisp crosses and the message wisp does not — this is the empty mailbox a TierBoth read gets for sending only this",
		},
		{
			name:  "the infra intent alone",
			req:   issueops.ListRequest{IncludeInfra: true},
			want:  everything,
			about: "include_infra drops the type exclusions AND admits the plane, both from the same branch, so it already answers with the mail",
		},
		{
			name:  "both intents",
			req:   issueops.ListRequest{IncludeEphemeral: true, IncludeInfra: true},
			want:  everything,
			about: "the pairing a TierBoth read sends, and the same answer as the infra intent alone at this tip",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := test.req
			req.Labels = []string{scope}
			req.SortBy = "created"
			req.Limit = ptrTo(0)
			page, err := reader.List(ctx, req)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if got := pageIDs(page); !slices.Equal(got, test.want) {
				t.Errorf("List = %v, want %v (%s)", got, test.want, test.about)
			}
		})
	}
}
