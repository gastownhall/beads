package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/steveyegge/beads/internal/httpclient/wire"
)

// TestDispatchRefusesUnlimitedOnTheWire pins the tripwire: `limit=0` on an
// operation that reads it as unlimited never leaves dispatch, and the
// refusal is made before any transport is consulted.
//
// GOES RED when refuseUnlimitedOnWire is removed from dispatch.
func TestDispatchRefusesUnlimitedOnTheWire(t *testing.T) {
	s := New(Target{BaseURL: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}}, failingWire{t: t}, nil)
	for _, op := range []string{wire.OpListReadyWork, wire.OpListIssues, wire.OpQueryIssues} {
		err := s.dispatch(t.Context(), wire.Request{
			Op: op, Method: http.MethodGet, Path: "/x", Query: url.Values{"limit": {"0"}},
		}, nil)
		if !errors.Is(err, errUnlimitedOnWire) {
			t.Errorf("%s with limit=0: err = %v, want errUnlimitedOnWire", op, err)
		}
	}
	// A bounded limit, and an operation whose limit means something else, pass.
	for _, req := range []wire.Request{
		{Op: wire.OpListIssues, Query: url.Values{"limit": {"10"}}},
		{Op: wire.OpCountReadyWork, Query: url.Values{"limit": {"0"}}},
	} {
		if err := refuseUnlimitedOnWire(req); err != nil {
			t.Errorf("%s %v: unexpected refusal %v", req.Op, req.Query, err)
		}
	}
}

// failingWire fails the test if dispatch ever reaches it.
type failingWire struct {
	WireClient
	t *testing.T
}

func (w failingWire) Preflight(_ context.Context, op string) error {
	w.t.Errorf("dispatch reached Preflight for %s; the tripwire must fire first", op)
	return errors.New("unreachable")
}

// TestCheckUnlimitedPage covers the two refusals of a cursorless unlimited
// read, including the one no served fixture can produce: a server that
// truncates below the requested bound and says so with has_more. A full
// cap+1 page that also reports has_more is how a real workspace overflows
// the cap, and it is the cap's refusal, not a truncation.
func TestCheckUnlimitedPage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		readCap   int
		rows      int
		hasMore   bool
		truncated bool
		refused   bool
	}{
		{"bounded read passes as shaped", 0, 5, true, false, false},
		{"within the cap", 10, 10, false, false, false},
		{"over the cap", 10, 11, false, false, true},
		{"over the cap with more remaining", 10, 11, true, false, true},
		{"server truncated below the bound", 10, 4, true, true, true},
	} {
		err := checkUnlimitedPage(wire.OpQueryIssues, tc.readCap, tc.rows, tc.hasMore)
		var capErr *UnlimitedReadCapError
		if got := errors.As(err, &capErr); got != tc.refused {
			t.Fatalf("%s: refused = %v (%v), want %v", tc.name, got, err, tc.refused)
		}
		if tc.refused && capErr.ServerTruncated != tc.truncated {
			t.Errorf("%s: ServerTruncated = %v, want %v", tc.name, capErr.ServerTruncated, tc.truncated)
		}
	}
}

// TestTightenUnlimitedPage pins when a caller's MaxRows sizes the request of
// an unlimited read: only when it is tighter than the cap. A bounded read
// (readCap 0) goes out exactly as the caller shaped it.
func TestTightenUnlimitedPage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		readCap   int
		maxRows   int
		sent      string // the limit before the call
		wantBound int
		wantLimit string
	}{
		{"bounded read", 0, 5, "7", 0, "7"},
		{"no MaxRows", 10, 0, "11", 10, "11"},
		{"MaxRows above the cap", 10, 50, "11", 10, "11"},
		{"MaxRows at the cap", 10, 10, "11", 10, "11"},
		{"MaxRows below the cap", 10, 5, "11", 5, "6"},
	} {
		params := url.Values{"limit": {tc.sent}}
		if got := tightenUnlimitedPage(params, tc.readCap, tc.maxRows); got != tc.wantBound {
			t.Errorf("%s: bound = %d, want %d", tc.name, got, tc.wantBound)
		}
		if got := params.Get("limit"); got != tc.wantLimit {
			t.Errorf("%s: limit = %q, want %q", tc.name, got, tc.wantLimit)
		}
	}
}

func TestUnlimitedReadCapEnv(t *testing.T) {
	t.Setenv(UnlimitedReadCapEnv, "")
	if n, err := unlimitedReadCap(); err != nil || n != DefaultUnlimitedReadCap {
		t.Errorf("unset: %d, %v; want the default %d", n, err, DefaultUnlimitedReadCap)
	}
	t.Setenv(UnlimitedReadCapEnv, " 250 ")
	if n, err := unlimitedReadCap(); err != nil || n != 250 {
		t.Errorf("250: %d, %v", n, err)
	}
	t.Setenv(UnlimitedReadCapEnv, "1000000")
	if n, err := unlimitedReadCap(); err != nil || n != maxUnlimitedReadCap {
		t.Errorf("the inclusive maximum: %d, %v; want %d", n, err, maxUnlimitedReadCap)
	}
	for _, bad := range []string{"0", "-1", "ten", "1000001"} {
		t.Setenv(UnlimitedReadCapEnv, bad)
		if _, err := unlimitedReadCap(); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
