//go:build cgo

package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
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

// readFn is one of the unlimited reads under test, returning its row count.
type readFn func(t *testing.T, ctx context.Context, u *unlimitedEnv) (int, error)

func unlimitedReads() []struct {
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
		{"list", func(t *testing.T, ctx context.Context, u *unlimitedEnv) (int, error) {
			page, err := u.reader(t).List(ctx, issueops.ListRequest{Labels: []string{u.scope}, Limit: zero()})
			if err == nil && page.HasMore {
				t.Error("an unlimited list reported HasMore")
			}
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
// server's 400, or the dispatch tripwire), or when the list walk is capped
// below the fixture.
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

	for _, tc := range unlimitedReads() {
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

	// The list walk crossed pages rather than asking for everything at once.
	u.spy.reset()
	if _, err := u.reader(t).List(ctx, issueops.ListRequest{Labels: []string{u.scope}, Limit: ptrTo(0)}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := len(u.spy.requests()); got < unlimitedRows/walkPageSize {
		t.Errorf("the unlimited list took %d requests at %d rows a page; it did not walk", got, walkPageSize)
	}
}

// TestServedUnlimitedReadCapRefuses: past BEADS_HTTP_UNLIMITED_READ_CAP every
// unlimited read is refused with *UnlimitedReadCapError, never truncated; at
// the cap exactly, the whole answer comes back.
//
// GOES RED when a path truncates to the cap instead of refusing, or ignores it.
func TestServedUnlimitedReadCapRefuses(t *testing.T) {
	u := newUnlimitedEnv(t)
	ctx := t.Context()

	restore := walkPageSize
	walkPageSize = 40
	t.Cleanup(func() { walkPageSize = restore })

	for _, tc := range unlimitedReads() {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(UnlimitedReadCapEnv, fmt.Sprint(unlimitedRows-1))
			u.spy.reset()
			n, err := tc.read(t, ctx, u)
			var capErr *UnlimitedReadCapError
			if !errors.As(err, &capErr) {
				t.Fatalf("over the cap: got %d rows, err %v; want *UnlimitedReadCapError", n, err)
			}
			if !errors.Is(err, ErrUnlimitedReadCap) {
				t.Errorf("the refusal does not match ErrUnlimitedReadCap: %v", err)
			}
			if capErr.Cap != unlimitedRows-1 || capErr.Found != unlimitedRows || capErr.ServerTruncated {
				t.Errorf("refusal = %+v, want Cap %d Found %d", capErr, unlimitedRows-1, unlimitedRows)
			}
			if !strings.Contains(err.Error(), UnlimitedReadCapEnv) {
				t.Errorf("the refusal does not name %s: %v", UnlimitedReadCapEnv, err)
			}
			u.spy.assertBoundedOnWire(t)

			t.Setenv(UnlimitedReadCapEnv, fmt.Sprint(unlimitedRows))
			n, err = tc.read(t, ctx, u)
			if err != nil || n != unlimitedRows {
				t.Fatalf("at the cap exactly: got %d rows, err %v; want all %d", n, err, unlimitedRows)
			}

			t.Setenv(UnlimitedReadCapEnv, "lots")
			if _, err := tc.read(t, ctx, u); err == nil || !strings.Contains(err.Error(), UnlimitedReadCapEnv) {
				t.Errorf("a malformed cap was not refused: %v", err)
			}
		})
	}
}
