// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/client_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/types"
)

func TestEveryRequestCarriesTheUserAgentAndAcceptsBothMediaTypes(t *testing.T) {
	c, rec := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	var out map[string]any
	if err := c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	got := rec.at(t, 0).header
	if ua := got.Get("User-Agent"); ua != DefaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", ua, DefaultUserAgent)
	}
	// problem+json has to be acceptable or a strict server could refuse to send
	// the very document this client dispatches on.
	if accept := got.Get("Accept"); !strings.Contains(accept, "application/problem+json") {
		t.Errorf("Accept = %q, want it to include application/problem+json", accept)
	}
}

func TestTheUserAgentIsOverridable(t *testing.T) {
	// The build version lives in package main, which this package may not
	// import, so the activation layer stamps it in.
	c, rec := newTestClient(t, Options{UserAgent: "bd/1.1.0 (http)"}, nil, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	var out map[string]any
	if err := c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ua := rec.at(t, 0).header.Get("User-Agent"); ua != "bd/1.1.0 (http)" {
		t.Errorf("User-Agent = %q", ua)
	}
}

func TestBodiesRoundTripThroughTheCanonicalTypes(t *testing.T) {
	// apigen's wire structs are ALIASES of internal/types, so decoding a
	// response is decoding into the canonical struct — no second copy of the
	// issue shape exists to drift from it.
	c, rec := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("request Content-Type = %q", ct)
		}
		_, _ = io.WriteString(w, `{"already_claimed":false,"issue":{"id":"ga-1","title":"wire the client","status":"in_progress","assignee":"me","priority":1}}`)
	})

	var out apigen.ClaimResponse
	err := c.Do(ctx(t), Request{
		Op:      OpClaimIssue,
		Method:  http.MethodPost,
		Path:    "/v0/beads/issues/ga-1:claim",
		Body:    apigen.ClaimRequest{Actor: "me"},
		IssueID: "ga-1",
	}, &out)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	// The compiler is half the assertion: this only builds because
	// apigen.Issue IS types.Issue.
	var canonical types.Issue = out.Issue
	if canonical.ID != "ga-1" || canonical.Title != "wire the client" {
		t.Errorf("decoded issue = %+v", canonical)
	}
	if canonical.Assignee != "me" || canonical.Status != types.StatusInProgress {
		t.Errorf("assignee/status did not decode: %q %q", canonical.Assignee, canonical.Status)
	}
	if body := string(rec.at(t, 0).body); !strings.Contains(body, `"actor":"me"`) {
		t.Errorf("request body = %s", body)
	}
}

func TestRedirectsAreRefusedRatherThanFollowed(t *testing.T) {
	// A followed 30x replays the Authorization header at whatever host the
	// Location named. bd serve does not redirect, so a 30x means the URL points
	// at something else and saying so beats leaking a token to find out.
	for _, status := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("the redirect target was dialed")
			}))
			t.Cleanup(elsewhere.Close)

			creds := &staticToken{tokens: []string{"secret-token"}}
			c, rec := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", elsewhere.URL+"/login?token=leaked")
				w.WriteHeader(status)
			})

			err := c.Do(ctx(t), Request{Op: OpListIssues, Method: http.MethodGet, Path: PathIssues}, &struct{}{})
			if !errors.Is(err, ErrRedirected) {
				t.Fatalf("err = %v, want ErrRedirected", err)
			}
			var redirect *RedirectRefusedError
			if !errors.As(err, &redirect) {
				t.Fatalf("err is %T, want *RedirectRefusedError", err)
			}
			if redirect.Status != status {
				t.Errorf("Status = %d, want %d", redirect.Status, status)
			}
			// The Location travels into a log line, so the query — which is
			// where a bounce-through-a-login-page puts its secrets — is
			// stripped.
			if strings.Contains(redirect.Error(), "leaked") {
				t.Errorf("the redirect query survived into the error: %v", redirect)
			}
			if !strings.Contains(redirect.Location, "/login") {
				t.Errorf("Location = %q, want the path kept for diagnosis", redirect.Location)
			}
			if rec.count() != 1 {
				t.Errorf("made %d requests, want 1", rec.count())
			}
		})
	}
}

func TestAnInjectedClientKeepsItsSettingsButNotItsRedirectPolicy(t *testing.T) {
	// Forcing CheckRedirect on the caller's own *http.Client would silently
	// change its behavior everywhere else it is used.
	followed := &http.Client{}
	base, err := url.Parse("http://127.0.0.1:1/")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c, err := New(base, nil, Options{HTTPClient: followed})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if followed.CheckRedirect != nil {
		t.Error("New mutated the caller's http.Client")
	}
	if c.hc.CheckRedirect == nil {
		t.Error("the client's copy follows redirects")
	}
}

func TestResponsesAreReadUnderAByteCap(t *testing.T) {
	const cap = 512
	cases := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{"under the cap", cap - 64, false},
		{"exactly the cap", cap, false},
		{"one byte over", cap + 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A JSON string padded to exactly the wanted size, so the "at the
			// cap" case still parses and the assertion is about the cap rather
			// than about the parser.
			pad := strings.Repeat("x", tc.size-len(`{"v":""}`))
			body := fmt.Sprintf(`{"v":"%s"}`, pad)
			c, _ := newTestClient(t, Options{MaxResponseBytes: cap}, nil, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			})
			var out struct {
				V string `json:"v"`
			}
			err := c.Do(ctx(t), Request{Op: OpListIssues, Method: http.MethodGet, Path: PathIssues}, &out)
			if tc.wantErr {
				if !errors.Is(err, ErrResponseTooLarge) {
					t.Fatalf("err = %v, want ErrResponseTooLarge", err)
				}
				var tooLarge *ResponseTooLargeError
				if !errors.As(err, &tooLarge) || tooLarge.Limit != cap {
					t.Errorf("err = %v, want the cap named", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if len(out.V) != len(pad) {
				t.Errorf("body was truncated: got %d bytes, want %d", len(out.V), len(pad))
			}
		})
	}
}

func TestAnOversizedProblemBodyIsStillRefusedRatherThanParsed(t *testing.T) {
	// The cap is on the transport, not on the success path: a hostile or broken
	// server must not be able to spend the client's memory by failing.
	c, _ := newTestClient(t, Options{MaxResponseBytes: 256}, nil, func(w http.ResponseWriter, _ *http.Request) {
		problemJSON(w, http.StatusInternalServerError, `{"status":500,"code":"internal","detail":"`+strings.Repeat("x", 4096)+`"}`)
	})
	err := c.Do(ctx(t), Request{Op: OpGetStats, Method: http.MethodGet, Path: PathStats}, &struct{}{})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
}

func TestTheContextTravelsWithTheRequest(t *testing.T) {
	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	cancelled, cancel := context.WithCancel(context.Background())
	go cancel()
	err := c.Do(cancelled, Request{Op: OpListIssues, Method: http.MethodGet, Path: PathIssues}, &struct{}{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	var connect *ConnectError
	if !errors.As(err, &connect) {
		t.Fatalf("err is %T, want *ConnectError", err)
	}
}

func TestAnUnreachableServerIsItsOwnErrorClass(t *testing.T) {
	// Not a ProblemError: the server said nothing, so there is no code to
	// dispatch on and the refusal taxonomy must not try.
	base, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c, err := New(base, nil, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = c.Do(ctx(t), Request{Op: OpHealth, Method: http.MethodGet, Path: PathHealth}, &struct{}{})
	var connect *ConnectError
	if !errors.As(err, &connect) {
		t.Fatalf("err is %T (%v), want *ConnectError", err, err)
	}
	var problem *ProblemError
	if errors.As(err, &problem) {
		t.Error("a dial failure classified as a server refusal")
	}
	if !strings.Contains(connect.Error(), "cannot reach bd serve at") {
		t.Errorf("Error() = %q", connect.Error())
	}
}

func TestAnUndecodableSuccessBodyFails(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"whitespace", "   \n"},
		{"not json", "<html>hello</html>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			})
			var out apigen.ContextResponse
			err := c.Do(ctx(t), Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, &out)
			if err == nil {
				t.Fatal("a 200 that is not the documented shape was accepted")
			}
			if !strings.Contains(err.Error(), OpGetContext) {
				t.Errorf("err = %q, want the operation named", err)
			}
		})
	}
}

func TestAResponseNobodyReadsNeedsNoBody(t *testing.T) {
	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.Do(ctx(t), Request{Op: OpForgetMemory, Method: http.MethodDelete, Path: PathMemories + "/k"}, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

func TestTheBaseURLIsValidatedAtConstruction(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"no scheme", "//example.invalid/"},
		{"wrong scheme", "ftp://example.invalid/"},
		{"file scheme", "file:///tmp/beads"},
		{"no host", "http:///v0"},
		// A credential in the URL would be echoed by every error naming the
		// server, and the sidecar it comes from is specified to hold no token.
		{"embedded credentials", "https://user:hunter2@example.invalid/"},
		{"query", "https://example.invalid/?db=x"},
		{"fragment", "https://example.invalid/#frag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.raw, err)
			}
			c, err := New(u, nil, Options{})
			if err == nil {
				t.Fatalf("New(%q) returned a client (%v)", tc.raw, c.BaseURL())
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("the password survived into the error: %v", err)
			}
		})
	}
	if _, err := New(nil, nil, Options{}); err == nil {
		t.Error("New(nil) returned no error")
	}
}

func TestATrailingSlashOnTheMountRootDoesNotDoubleTheSeparator(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	for _, suffix := range []string{"", "/", "///"} {
		u, err := url.Parse(srv.URL + suffix)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		c, err := New(u, nil, Options{HTTPClient: srv.Client()})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Do(ctx(t), Request{Op: OpListIssues, Method: http.MethodGet, Path: PathIssues}, &struct{}{}); err != nil {
			t.Fatalf("Do: %v", err)
		}
		if got != "/v0/beads/issues" {
			t.Errorf("base %q produced path %q", srv.URL+suffix, got)
		}
	}
}
