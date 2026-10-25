// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/credential_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
)

func unauthenticated(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	problemJSON(w, http.StatusUnauthorized, `{"status":401,"code":"unauthenticated","detail":"missing or invalid bearer token","request_id":"r"}`)
}

func TestTheCredentialIsPresentedOnEveryRequest(t *testing.T) {
	creds := &staticToken{tokens: []string{"tok-1"}}
	c, rec := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	})
	for range 2 {
		if err := c.Do(ctx(t), listIssues(), &struct{}{}); err != nil {
			t.Fatalf("Do: %v", err)
		}
	}
	for i := range 2 {
		if got := rec.at(t, i).header.Get("Authorization"); got != "Bearer tok-1" {
			t.Errorf("request %d Authorization = %q", i, got)
		}
	}
}

func TestNoProviderMeansNoAuthorizationHeader(t *testing.T) {
	// The tip OSS server's loopback-trust posture: there is no token file, and
	// sending a header it never configured would be noise at best.
	c, rec := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	})
	if err := c.Do(ctx(t), listIssues(), &struct{}{}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got := rec.at(t, 0).header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
}

func TestA401IsRetriedExactlyOnceThroughRefresh(t *testing.T) {
	// The client half of the server's rotation contract: the token file is
	// re-read on a ~1s gate, so rotation is write {new,old}, roll the clients,
	// drop old — and a client rolled mid-flight 401s once and succeeds on the
	// retry.
	creds := &staticToken{tokens: []string{"stale", "rotated"}, retry: true}
	c, rec := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rotated" {
			unauthenticated(w)
			return
		}
		_, _ = io.WriteString(w, `{"has_more":false,"items":[]}`)
	})

	var page apigen.IssuesPage
	if err := c.Do(ctx(t), listIssues(), &page); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if rec.count() != 2 {
		t.Fatalf("made %d requests, want 2", rec.count())
	}
	if got := rec.at(t, 0).header.Get("Authorization"); got != "Bearer stale" {
		t.Errorf("first attempt used %q", got)
	}
	// The retry goes back through Authorize, which is what makes the rotated
	// credential reach the wire at all.
	if got := rec.at(t, 1).header.Get("Authorization"); got != "Bearer rotated" {
		t.Errorf("retry used %q", got)
	}
	if creds.refreshN != 1 {
		t.Errorf("Refresh called %d times, want 1", creds.refreshN)
	}
}

func TestASecondUnauthenticatedAnswerIsNotRetriedAgain(t *testing.T) {
	// One retry is the whole rotation window. A second 401 is a credential this
	// server does not accept, and retrying it again would turn a clear refusal
	// into a loop against an authentication endpoint.
	creds := &staticToken{tokens: []string{"wrong"}, retry: true}
	c, rec := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, _ *http.Request) {
		unauthenticated(w)
	})

	err := c.Do(ctx(t), listIssues(), &struct{}{})
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
	if rec.count() != 2 {
		t.Errorf("made %d requests, want exactly 2", rec.count())
	}
	if creds.refreshN != 1 {
		t.Errorf("Refresh called %d times, want 1", creds.refreshN)
	}
}

func TestRefreshDecliningSurfacesThe401AsItStands(t *testing.T) {
	// A provider that knows it has nothing new — a static token from the
	// environment — declines, and the refusal reaches the caller unchanged
	// rather than costing a second round trip.
	creds := &staticToken{tokens: []string{"static"}, retry: false}
	c, rec := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, _ *http.Request) {
		unauthenticated(w)
	})

	err := c.Do(ctx(t), listIssues(), &struct{}{})
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
	if rec.count() != 1 {
		t.Errorf("made %d requests, want 1", rec.count())
	}
	if creds.refreshN != 1 {
		t.Errorf("Refresh called %d times, want 1", creds.refreshN)
	}
}

func TestAFailingRefreshFailsClosed(t *testing.T) {
	// A configured source that errored is not a license to send the stale
	// credential again, and certainly not to send none.
	creds := &staticToken{tokens: []string{"stale"}, err: errors.New("token file is unreadable")}
	c, rec := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, _ *http.Request) {
		unauthenticated(w)
	})

	err := c.Do(ctx(t), listIssues(), &struct{}{})
	if err == nil {
		t.Fatal("a failing credential refresh was swallowed")
	}
	if !strings.Contains(err.Error(), "token file is unreadable") {
		t.Errorf("err = %q, want the source's own failure named", err)
	}
	if strings.Contains(err.Error(), "stale") {
		t.Errorf("the credential survived into the error: %v", err)
	}
	if rec.count() != 1 {
		t.Errorf("made %d requests, want 1", rec.count())
	}
}

func TestNoProviderMeansNoRotationWindow(t *testing.T) {
	// A 401 from a server this client has no credential for is a deployment
	// mismatch, not a rotation: there is nothing to refresh and nothing to retry.
	c, rec := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
		unauthenticated(w)
	})
	if err := c.Do(ctx(t), listIssues(), &struct{}{}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
	if rec.count() != 1 {
		t.Errorf("made %d requests, want 1", rec.count())
	}
}

func TestOnlyA401OpensTheRotationWindow(t *testing.T) {
	// Every other refusal is about the request, so re-sending it with a fresh
	// credential would just earn the same answer twice.
	for _, status := range []int{400, 403, 404, 409, 500, 503} {
		creds := &staticToken{tokens: []string{"tok"}, retry: true}
		c, rec := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, _ *http.Request) {
			problemJSON(w, status, `{"status":0,"code":"internal"}`)
		})
		_ = c.Do(ctx(t), listIssues(), &struct{}{})
		if rec.count() != 1 {
			t.Errorf("status %d made %d requests, want 1", status, rec.count())
		}
		if creds.refreshN != 0 {
			t.Errorf("status %d consulted Refresh", status)
		}
	}
}

func TestTheRetriedRequestReplaysItsBody(t *testing.T) {
	// The retry re-issues the SAME request. A body streamed straight from the
	// caller would be spent by the first attempt and the second would post
	// nothing — which on a write is a silent half-application.
	creds := &staticToken{tokens: []string{"stale", "rotated"}, retry: true}
	c, rec := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rotated" {
			unauthenticated(w)
			return
		}
		_, _ = io.WriteString(w, `{"already_claimed":false,"issue":{"id":"ga-1"}}`)
	})

	var out apigen.ClaimResponse
	err := c.Do(ctx(t), Request{
		Op:      OpClaimIssue,
		Method:  http.MethodPost,
		Path:    "/v0/beads/issues/ga-1:claim",
		Body:    apigen.ClaimRequest{Actor: "agent-7"},
		IssueID: "ga-1",
	}, &out)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if rec.count() != 2 {
		t.Fatalf("made %d requests, want 2", rec.count())
	}
	first, second := string(rec.at(t, 0).body), string(rec.at(t, 1).body)
	if first != second || !strings.Contains(second, `"actor":"agent-7"`) {
		t.Errorf("bodies differ across the retry: %q then %q", first, second)
	}
}

func TestAnAuthorizeFailureNeverReachesTheWire(t *testing.T) {
	// Fail closed at the earliest point: a request that could not be authorized
	// must not be sent unauthenticated to find out what happens.
	creds := &failingAuthorize{err: errors.New("no token source is configured")}
	c, rec := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an unauthorized request was dialed")
		_, _ = io.WriteString(w, `{}`)
	})
	err := c.Do(ctx(t), listIssues(), &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "no token source is configured") {
		t.Fatalf("err = %v, want the provider's failure", err)
	}
	if rec.count() != 0 {
		t.Errorf("made %d requests, want 0", rec.count())
	}
}

type failingAuthorize struct{ err error }

func (f *failingAuthorize) Authorize(context.Context, *http.Request) error { return f.err }
func (f *failingAuthorize) Refresh(context.Context) (bool, error)          { return false, nil }
