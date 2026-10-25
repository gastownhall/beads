//go:build cgo

package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// Unlimited reads against a server in NON-LOOPBACK mode (S15).
//
// The in-process server binds 127.0.0.1 — tests never listen beyond loopback —
// but is started with AllowNonLoopback, which is exactly the switch the
// server's unlimited-read refusal reads (httpapi allowUnlimited). So every
// `limit=0` this client sent would meet the production 400, and every answer
// below that comes back whole is proof that none was sent.
//
// The fixture is unlimitedRows rows: more than the server's ready default
// (workapi.DefaultReadyLimit, 100), so a read that silently fell back to that
// default — `bd ready --limit 0` before S15 — comes back short and fails the
// count, and more than one walkPageSize, so the list walk crosses pages.

const unlimitedRows = 150

// nonLoopbackMode serves the whole surface as a --allow-non-loopback server
// would. InsecureNoAuth is the posture rule's required waiver for a
// non-loopback server with no token file; the listener itself stays on
// 127.0.0.1.
func nonLoopbackMode(cfg *httpapi.Config) {
	cfg.AllowNonLoopback = true
	cfg.InsecureNoAuth = true
}

// limitSpy records the `limit` of every issue-listing request that reaches the
// wire, after dispatch's own tripwire.
type limitSpy struct {
	mu   sync.Mutex
	sent []string // "op limit=<value>"
}

func (s *limitSpy) record(req *wire.Request) {
	if !unlimitedSpellingOps[req.Op] {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, fmt.Sprintf("%s limit=%s", req.Op, req.Query.Get("limit")))
}

func (s *limitSpy) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = nil
}

func (s *limitSpy) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

// requestsFor is requests narrowed to one operation's.
func (s *limitSpy) requestsFor(op string) []string {
	var got []string
	for _, r := range s.requests() {
		if strings.HasPrefix(r, op+" ") {
			got = append(got, r)
		}
	}
	return got
}

// assertBoundedOnWire fails if any recorded request carried limit=0 or
// no limit at all (the server's default, which is the silent-truncation leg).
func (s *limitSpy) assertBoundedOnWire(t *testing.T) {
	t.Helper()
	got := s.requests()
	if len(got) == 0 {
		t.Fatal("no listing request was recorded; the spy is not on the path")
	}
	for _, r := range got {
		if strings.HasSuffix(r, " limit=0") || strings.HasSuffix(r, " limit=") {
			t.Errorf("request %q went out unbounded (all requests: %v)", r, got)
		}
	}
}

type unlimitedEnv struct {
	*servedEnv
	spy   *limitSpy
	store *Store
	scope string
}

func newUnlimitedEnv(t *testing.T) *unlimitedEnv {
	t.Helper()
	e := newServedEnv(t, "unl", nonLoopbackMode)
	ctx := t.Context()
	scope := "unlimited-scope"
	issues := make([]*types.Issue, 0, unlimitedRows)
	for i := range unlimitedRows {
		id := fmt.Sprintf("unl-%03d", i)
		issues = append(issues, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: i % 4,
			IssueType: types.TypeTask, Labels: []string{scope},
		})
	}
	if err := e.reference.CreateIssues(ctx, issues, "seed"); err != nil {
		t.Fatalf("seed %d rows: %v", unlimitedRows, err)
	}
	spy := &limitSpy{}
	store := New(e.subject.target, tamperedWire{WireClient: e.subject.wire, tamper: spy.record}, nil)
	return &unlimitedEnv{servedEnv: e, spy: spy, store: store, scope: scope}
}

func (u *unlimitedEnv) reader(t *testing.T) issueops.Reader {
	t.Helper()
	r, err := u.store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	return r
}

func (u *unlimitedEnv) querier(t *testing.T) issueops.Querier {
	t.Helper()
	q, err := u.store.Querier()
	if err != nil {
		t.Fatalf("Querier(): %v", err)
	}
	return q
}

// listAll is an unlimited list: a cursor walk that only maxRows bounds (0 for
// none). It returns the row count.
func (u *unlimitedEnv) listAll(t *testing.T, maxRows int) (int, error) {
	t.Helper()
	page, err := u.reader(t).List(t.Context(), issueops.ListRequest{
		Labels: []string{u.scope}, Limit: ptrTo(0), MaxRows: maxRows,
	})
	if err == nil && page.HasMore {
		t.Error("an unlimited list reported HasMore")
	}
	return len(page.Items), err
}

// readFn is one of the unlimited reads under test, returning its row count.
type readFn func(t *testing.T, ctx context.Context, u *unlimitedEnv) (int, error)

// cursorlessReads are the unlimited reads that are ONE request — ready and
// query publish no cursor — and so the ones the client cap bounds.
func cursorlessReads() []struct {
	name string
	read readFn
} {
	zero := func() *int { z := 0; return &z }
	return []struct {
		name string
		read readFn
	}{
		{"ready via the Reader role", func(t *testing.T, ctx context.Context, u *unlimitedEnv) (int, error) {
			page, err := u.reader(t).Ready(ctx, issueops.ReadyRequest{Labels: []string{u.scope}, Limit: zero()})
			return len(page.Items), err
		}},
		{"bd ready via the WorkFilter bridge", func(t *testing.T, ctx context.Context, u *unlimitedEnv) (int, error) {
			filter, err := workapi.BuildReadyFilter(issueops.ReadyRequest{
				Labels: []string{u.scope}, Sort: string(types.SortPolicyPriority), Limit: zero(),
			})
			if err != nil {
				t.Fatalf("BuildReadyFilter: %v", err)
			}
			rows, err := u.store.GetReadyWork(ctx, filter)
			return len(rows), err
		}},
		{"bd ready --json via the WorkFilter bridge with a total", func(t *testing.T, ctx context.Context, u *unlimitedEnv) (int, error) {
			filter, err := workapi.BuildReadyFilter(issueops.ReadyRequest{
				Labels: []string{u.scope}, Sort: string(types.SortPolicyPriority), Limit: zero(),
			})
			if err != nil {
				t.Fatalf("BuildReadyFilter: %v", err)
			}
			rows, total, err := u.store.GetReadyWorkWithCountsAndTotal(ctx, filter)
			if err == nil && total != len(rows) {
				t.Errorf("total = %d for %d rows; an unlimited page is the whole set", total, len(rows))
			}
			return len(rows), err
		}},
		{"query", func(t *testing.T, ctx context.Context, u *unlimitedEnv) (int, error) {
			page, err := u.querier(t).Query(ctx, issueops.QueryRequest{Expression: "label=" + u.scope, Limit: zero()})
			return len(page.Items), err
		}},
	}
}

// TestServedUnlimitedReadsOffLoopback: ready, query and list at limit 0 return
// every row from a non-loopback-mode server, and never put `limit=0` (or a
// missing limit) on the wire.
//
// GOES RED when the bridge sends no limit for Limit 0 (the server's 100-row
// default: 100 != 150), when the role or the querier forwards `limit=0` (the
// server's 400, or the dispatch tripwire), or when the list walk stops short of
// the fixture.
func TestServedUnlimitedReadsOffLoopback(t *testing.T) {
	u := newUnlimitedEnv(t)
	ctx := t.Context()

	restore := walkPageSize
	walkPageSize = 40
	t.Cleanup(func() { walkPageSize = restore })

	// PREMISE 1: the server really is in non-loopback mode — a raw `limit=0`
	// that bypasses the client's dispatch is refused.
	var raw apigen.ReadyPage
	err := u.subject.wire.Do(ctx, wire.Request{
		Op: wire.OpListReadyWork, Method: "GET", Path: wire.PathReady,
		Query: url.Values{"limit": {"0"}},
	}, &raw)
	if err == nil || !strings.Contains(err.Error(), "loopback-only") {
		t.Fatalf("a raw limit=0 was not refused by the server (err %v); the fixture is not in non-loopback mode", err)
	}

	// PREMISE 2: the fixture exceeds the server's ready default, so a read
	// that fell back to it cannot pass the count below.
	page, err := u.reader(t).Ready(ctx, issueops.ReadyRequest{Labels: []string{u.scope}})
	if err != nil {
		t.Fatalf("default-limit ready: %v", err)
	}
	if len(page.Items) != workapi.DefaultReadyLimit || !page.HasMore {
		t.Fatalf("default-limit ready returned %d rows (has_more %v), want the server default %d with more",
			len(page.Items), page.HasMore, workapi.DefaultReadyLimit)
	}

	for _, tc := range cursorlessReads() {
		t.Run(tc.name, func(t *testing.T) {
			u.spy.reset()
			n, err := tc.read(t, ctx, u)
			if err != nil {
				t.Fatalf("unlimited read: %v", err)
			}
			if n != unlimitedRows {
				t.Errorf("unlimited read returned %d rows, want all %d", n, unlimitedRows)
			}
			u.spy.assertBoundedOnWire(t)
		})
	}

	// The list walk returns every row, and crosses pages rather than asking
	// for everything at once.
	t.Run("list", func(t *testing.T) {
		u.spy.reset()
		n, err := u.listAll(t, 0)
		if err != nil {
			t.Fatalf("unlimited list: %v", err)
		}
		if n != unlimitedRows {
			t.Errorf("unlimited list returned %d rows, want all %d", n, unlimitedRows)
		}
		u.spy.assertBoundedOnWire(t)
		if got := len(u.spy.requests()); got < unlimitedRows/walkPageSize {
			t.Errorf("the unlimited list took %d requests at %d rows a page; it did not walk", got, walkPageSize)
		}
	})
}

// TestServedUnlimitedReadCapRefuses: past BEADS_HTTP_UNLIMITED_READ_CAP every
// unlimited ready or query read is refused with *UnlimitedReadCapError, never
// truncated; at the cap exactly, the whole answer comes back. An unlimited list
// is a cursor walk, and the cap does not apply to it: only MaxRows bounds it.
//
// GOES RED when a cursorless path truncates to the cap instead of refusing, or
// ignores it; when a full cap+1 page that reports has_more is taken for a
// server truncation; or when the list walk is capped.
func TestServedUnlimitedReadCapRefuses(t *testing.T) {
	u := newUnlimitedEnv(t)
	ctx := t.Context()

	for _, tc := range cursorlessReads() {
		t.Run(tc.name, func(t *testing.T) {
			// At unlimitedRows-1 the cap+1 page is the whole set; at
			// unlimitedRows-2 it is a full page with has_more set, which is how
			// a real workspace overflows the cap. Both are the cap's refusal.
			for _, readCap := range []int{unlimitedRows - 1, unlimitedRows - 2} {
				t.Setenv(UnlimitedReadCapEnv, fmt.Sprint(readCap))
				u.spy.reset()
				n, err := tc.read(t, ctx, u)
				var capErr *UnlimitedReadCapError
				if !errors.As(err, &capErr) {
					t.Fatalf("cap %d: got %d rows, err %v; want *UnlimitedReadCapError", readCap, n, err)
				}
				if !errors.Is(err, ErrUnlimitedReadCap) {
					t.Errorf("cap %d: the refusal does not match ErrUnlimitedReadCap: %v", readCap, err)
				}
				if capErr.Cap != readCap || capErr.Found != readCap+1 || capErr.ServerTruncated {
					t.Errorf("cap %d: refusal = %+v, want Cap %d Found %d and no server truncation",
						readCap, capErr, readCap, readCap+1)
				}
				if !strings.Contains(err.Error(), UnlimitedReadCapEnv) {
					t.Errorf("cap %d: the refusal does not name %s: %v", readCap, UnlimitedReadCapEnv, err)
				}
				u.spy.assertBoundedOnWire(t)
			}

			t.Setenv(UnlimitedReadCapEnv, fmt.Sprint(unlimitedRows))
			n, err := tc.read(t, ctx, u)
			if err != nil || n != unlimitedRows {
				t.Fatalf("at the cap exactly: got %d rows, err %v; want all %d", n, err, unlimitedRows)
			}

			t.Setenv(UnlimitedReadCapEnv, "lots")
			if _, err := tc.read(t, ctx, u); err == nil || !strings.Contains(err.Error(), UnlimitedReadCapEnv) {
				t.Errorf("a malformed cap was not refused: %v", err)
			}
		})
	}

	t.Run("list is not capped", func(t *testing.T) {
		restore := walkPageSize
		walkPageSize = 40
		t.Cleanup(func() { walkPageSize = restore })

		for _, readCap := range []string{"10", "lots"} {
			t.Setenv(UnlimitedReadCapEnv, readCap)
			if n, err := u.listAll(t, 0); err != nil || n != unlimitedRows {
				t.Errorf("cap %q: the unlimited list returned %d rows, err %v; want all %d", readCap, n, err, unlimitedRows)
			}
		}

		// MaxRows is the walk's only bound, and past it the refusal is the
		// caller's own.
		t.Setenv(UnlimitedReadCapEnv, "10")
		_, err := u.listAll(t, 100)
		var tooMany *storageops.ErrTooManyRows
		if !errors.As(err, &tooMany) || tooMany.Cap != 100 || tooMany.Found != 101 || errors.Is(err, ErrUnlimitedReadCap) {
			t.Errorf("the unlimited list past MaxRows 100: err %v; want *ErrTooManyRows Cap 100 Found 101", err)
		}
		if n, err := u.listAll(t, unlimitedRows); err != nil || n != unlimitedRows {
			t.Errorf("MaxRows at the fixture: %d rows, err %v; want all %d", n, err, unlimitedRows)
		}
	})
}

// TestServedReadyBridgeMaxRowsSizesTheRequest: when an unlimited bridge read
// carries a MaxRows as well, the tighter of it and the cap sizes the request,
// so a small MaxRows never makes the server buffer up to the cap, and an answer
// past it is refused in the name of the bound that fired. `bd ready` reads
// through the external-dependency decorator, which drops an externally
// blocked row itself against this server; the same holds through it, with the
// request grown by the rows it may drop and the cap still in force.
//
// GOES RED when the bridge sends the cap's limit under a tighter MaxRows, or
// reports an answer past MaxRows as the read cap's refusal; or when the
// decorator takes MaxRows off an unlimited read, does not grow it by the
// blocked rows, reports the refusal against the grown bound, or lets the
// growth lift the request past the cap.
func TestServedReadyBridgeMaxRowsSizesTheRequest(t *testing.T) {
	u := newUnlimitedEnv(t)
	read := func(t *testing.T, ready func(context.Context, types.WorkFilter) ([]*types.Issue, error), maxRows int) (int, error) {
		t.Helper()
		filter, err := workapi.BuildReadyFilter(issueops.ReadyRequest{
			Labels: []string{u.scope}, Sort: string(types.SortPolicyPriority), Limit: ptrTo(0),
		})
		if err != nil {
			t.Fatalf("BuildReadyFilter: %v", err)
		}
		filter.MaxRows = maxRows
		u.spy.reset()
		rows, err := ready(t.Context(), filter)
		return len(rows), err
	}
	sent := func(limit int) []string { return []string{fmt.Sprintf("%s limit=%d", wire.OpListReadyWork, limit)} }

	t.Setenv(UnlimitedReadCapEnv, "10")
	_, err := read(t, u.store.GetReadyWork, 5)
	var tooMany *storageops.ErrTooManyRows
	if !errors.As(err, &tooMany) || tooMany.Cap != 5 || tooMany.Found != 6 || errors.Is(err, ErrUnlimitedReadCap) {
		t.Errorf("MaxRows 5 under cap 10: err %v; want *ErrTooManyRows Cap 5 Found 6", err)
	}
	if got := u.spy.requests(); !slices.Equal(got, sent(6)) {
		t.Errorf("MaxRows 5 under cap 10 sent %v, want %v", got, sent(6))
	}

	_, err = read(t, u.store.GetReadyWork, 50)
	var capErr *UnlimitedReadCapError
	if !errors.As(err, &capErr) || capErr.Cap != 10 || capErr.Found != 11 || capErr.ServerTruncated {
		t.Errorf("MaxRows 50 over cap 10: err %v; want *UnlimitedReadCapError Cap 10 Found 11", err)
	}
	if got := u.spy.requests(); !slices.Equal(got, sent(11)) {
		t.Errorf("MaxRows 50 over cap 10 sent %v, want %v", got, sent(11))
	}

	// Both bounds clear the fixture: every row, in the request MaxRows sized.
	t.Setenv(UnlimitedReadCapEnv, "1000")
	if n, err := read(t, u.store.GetReadyWork, unlimitedRows+10); err != nil || n != unlimitedRows {
		t.Errorf("MaxRows %d under cap 1000: %d rows, err %v; want all %d", unlimitedRows+10, n, err, unlimitedRows)
	}
	if got := u.spy.requests(); !slices.Equal(got, sent(unlimitedRows+11)) {
		t.Errorf("MaxRows %d under cap 1000 sent %v, want %v", unlimitedRows+10, got, sent(unlimitedRows+11))
	}

	// Through the decorator, with unl-000 externally blocked: the request asks
	// for one row more per blocked issue, and past that bound more than MaxRows
	// rows would still be kept, so the refusal is still the caller's own. The
	// decorator's blocking state is loaded with a list walk, which is not the
	// read's.
	if enforced, err := u.store.ServerEnforcesExternalDependencyPolicy(t.Context()); err != nil || enforced {
		t.Fatalf("ServerEnforcesExternalDependencyPolicy = %v, %v; this test needs a server that leaves the policy to the client", enforced, err)
	}
	if err := u.addDependency(t.Context(), &types.Dependency{
		IssueID: "unl-000", DependsOnID: "external:otherproj:cap", Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("seed external dependency: %v", err)
	}
	decorated := externaldeps.New(u.store, nil, nil).GetReadyWork

	t.Setenv(UnlimitedReadCapEnv, "10")
	_, err = read(t, decorated, 5)
	if !errors.As(err, &tooMany) || tooMany.Cap != 5 || tooMany.Found != 6 || errors.Is(err, ErrUnlimitedReadCap) {
		t.Errorf("decorated, MaxRows 5 under cap 10: err %v; want *ErrTooManyRows Cap 5 Found 6", err)
	}
	if got := u.spy.requestsFor(wire.OpListReadyWork); !slices.Equal(got, sent(7)) {
		t.Errorf("decorated, MaxRows 5 under cap 10 sent %v, want %v", got, sent(7))
	}

	// MaxRows and the blocked row together reach past the cap, which sizes
	// the request and refuses, as it does for the bridge alone.
	_, err = read(t, decorated, 50)
	if !errors.As(err, &capErr) || capErr.Cap != 10 || capErr.Found != 11 || capErr.ServerTruncated {
		t.Errorf("decorated, MaxRows 50 over cap 10: err %v; want *UnlimitedReadCapError Cap 10 Found 11", err)
	}
	if got := u.spy.requestsFor(wire.OpListReadyWork); !slices.Equal(got, sent(11)) {
		t.Errorf("decorated, MaxRows 50 over cap 10 sent %v, want %v", got, sent(11))
	}

	t.Setenv(UnlimitedReadCapEnv, "1000")
	if n, err := read(t, decorated, unlimitedRows+10); err != nil || n != unlimitedRows-1 {
		t.Errorf("decorated, MaxRows %d under cap 1000: %d rows, err %v; want all %d but unl-000", unlimitedRows+10, n, err, unlimitedRows-1)
	}
	if got := u.spy.requestsFor(wire.OpListReadyWork); !slices.Equal(got, sent(unlimitedRows+12)) {
		t.Errorf("decorated, MaxRows %d under cap 1000 sent %v, want %v", unlimitedRows+10, got, sent(unlimitedRows+12))
	}
}

// TestServedReadyCountFallbackStaysBounded: text-mode `bd ready` counts through
// the external-dependency decorator, and against a server that does not apply
// that policy itself (every released server, and this harness) the decorator
// counts over a store with no indexed counter. It asks for one row and lets
// the server count the rest, so a ready set larger than
// BEADS_HTTP_UNLIMITED_READ_CAP is counted rather than refused, and never
// downloaded to report one number.
//
// GOES RED when the count's fallback asks for the whole ready set again: past
// the cap that read is refused, and the spy sees more than the one row.
func TestServedReadyCountFallbackStaysBounded(t *testing.T) {
	u := newUnlimitedEnv(t)
	ctx := t.Context()
	t.Setenv(UnlimitedReadCapEnv, "10")

	if enforced, err := u.store.ServerEnforcesExternalDependencyPolicy(ctx); err != nil || enforced {
		t.Fatalf("ServerEnforcesExternalDependencyPolicy = %v, %v; this test needs a server that leaves the policy to the client", enforced, err)
	}
	counter, err := externaldeps.New(u.store, nil, nil).ReadyCounter()
	if err != nil {
		t.Fatalf("ReadyCounter: %v", err)
	}
	// count returns the total and the count's ready listings; the decorator's
	// blocking state is loaded with a list walk, which is not the count's.
	count := func(t *testing.T) (int64, []string) {
		t.Helper()
		u.spy.reset()
		got, err := counter.CountReady(ctx, issueops.ReadyRequest{Labels: []string{u.scope}})
		if err != nil {
			t.Fatalf("CountReady over %d ready rows at cap 10: %v", unlimitedRows, err)
		}
		return got.Total, u.spy.requestsFor(wire.OpListReadyWork)
	}

	total, ready := count(t)
	if total != unlimitedRows {
		t.Errorf("count = %d, want %d", total, unlimitedRows)
	}
	if want := []string{wire.OpListReadyWork + " limit=1"}; !slices.Equal(ready, want) {
		t.Errorf("the count listed %v, want %v: one row, the server counting the rest", ready, want)
	}

	// An externally blocked issue makes the decorator size the filtered set
	// itself, at an explicit limit the cap does not apply to.
	if err := u.addDependency(ctx, &types.Dependency{
		IssueID: "unl-000", DependsOnID: "external:otherproj:cap", Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("seed external dependency: %v", err)
	}
	if total, _ := count(t); total != unlimitedRows-1 {
		t.Errorf("count with unl-000 externally blocked = %d, want %d", total, unlimitedRows-1)
	}
	u.spy.assertBoundedOnWire(t)
}
