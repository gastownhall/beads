package notion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientRetrieveDataSourceSetsHeaders(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/data_sources/ds_123" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("authorization = %q", got)
		}
		if got := r.Header.Get("Notion-Version"); got != DefaultNotionVersion {
			t.Fatalf("notion version = %q", got)
		}
		_, _ = io.WriteString(w, `{"id":"ds_123","url":"https://www.notion.so/source","title":[{"plain_text":"Tasks"}],"properties":{"Name":{"type":"title"}}}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	ds, err := client.RetrieveDataSource(context.Background(), "ds_123")
	if err != nil {
		t.Fatalf("RetrieveDataSource returned error: %v", err)
	}
	if ds.ID != "ds_123" {
		t.Fatalf("id = %q", ds.ID)
	}
	if DataSourceTitle(ds.Title) != "Tasks" {
		t.Fatalf("title = %q", DataSourceTitle(ds.Title))
	}
}

func TestClientQueryDataSourcePaginates(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		switch r.Header.Get("X-Test-Step") {
		default:
		}
		if r.URL.Path != "/data_sources/ds_123/query" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("content type = %q", r.Header.Get("Content-Type"))
		}
		if !strings.Contains(r.URL.RawQuery, "") {
		}
		if strings.Contains(r.Header.Get("X-Page"), "2") {
		}
	}))
	defer server.Close()

	call := 0
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/data_sources/ds_123/query" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if call == 1 {
			if !strings.Contains(string(body), `"page_size":100`) {
				t.Fatalf("request body = %s", body)
			}
			_, _ = io.WriteString(w, `{"results":[{"id":"page-1"},{"id":"page-2"}],"has_more":true,"next_cursor":"cursor-2"}`)
			return
		}
		if !strings.Contains(string(body), `"start_cursor":"cursor-2"`) {
			t.Fatalf("request body = %s", body)
		}
		_, _ = io.WriteString(w, `{"results":[{"id":"page-3"}],"has_more":false}`)
	})

	client := NewClient("secret-token").WithBaseURL(server.URL)
	pages, err := client.QueryDataSource(context.Background(), "ds_123")
	if err != nil {
		t.Fatalf("QueryDataSource returned error: %v", err)
	}
	if len(pages) != 3 {
		t.Fatalf("pages = %d, want 3", len(pages))
	}
}

func TestClientReturnsStructuredAPIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"code":"unauthorized","message":"token is invalid"}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	_, err := client.GetCurrentUser(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "token is invalid") {
		t.Fatalf("error = %q", err)
	}
}

func TestClientCreateDatabaseSendsInitialDataSource(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/databases" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		for _, want := range []string{
			`"page_id":"329e5bf9-7fae-8080-bb4a-d94e1387655d"`,
			`"initial_data_source"`,
			`"Beads ID"`,
			`"Status"`,
			`"Type"`,
		} {
			if !strings.Contains(string(body), want) {
				t.Fatalf("request body missing %q\n%s", want, body)
			}
		}
		_, _ = io.WriteString(w, `{"id":"db_123","url":"https://www.notion.so/db123","data_sources":[{"id":"ds_123","name":"Beads Issues"}]}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	db, err := client.CreateDatabase(context.Background(), "329e5bf9-7fae-8080-bb4a-d94e1387655d", DefaultDatabaseTitle)
	if err != nil {
		t.Fatalf("CreateDatabase returned error: %v", err)
	}
	if db.ID != "db_123" {
		t.Fatalf("id = %q", db.ID)
	}
	if len(db.DataSources) != 1 || db.DataSources[0].ID != "ds_123" {
		t.Fatalf("data_sources = %+v", db.DataSources)
	}
}

func TestClientRetrieveDatabase(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/databases/db_123" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":"db_123","url":"https://www.notion.so/db123","data_sources":[{"id":"ds_123","name":"Beads Issues"}]}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	db, err := client.RetrieveDatabase(context.Background(), "db_123")
	if err != nil {
		t.Fatalf("RetrieveDatabase returned error: %v", err)
	}
	if db.ID != "db_123" {
		t.Fatalf("id = %q", db.ID)
	}
	if len(db.DataSources) != 1 || db.DataSources[0].ID != "ds_123" {
		t.Fatalf("data_sources = %+v", db.DataSources)
	}
}

func TestResolveDataSourceReferencePrefersDataSource(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data_sources/329e5bf9-7fae-8080-bb4a-d94e1387655d":
			_, _ = io.WriteString(w, `{"id":"329e5bf9-7fae-8080-bb4a-d94e1387655d","properties":{"Name":{"type":"title"}}}`)
		default:
			t.Fatalf("path = %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	resolved, err := ResolveDataSourceReference(context.Background(), client, "https://www.notion.so/workspace/329e5bf97fae8080bb4ad94e1387655d")
	if err != nil {
		t.Fatalf("ResolveDataSourceReference returned error: %v", err)
	}
	if resolved.DataSourceID != "329e5bf9-7fae-8080-bb4a-d94e1387655d" {
		t.Fatalf("data_source_id = %q", resolved.DataSourceID)
	}
}

func TestResolveDataSourceReferenceFallsBackToDatabase(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data_sources/429e5bf9-7fae-8080-bb4a-d94e1387655d":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"object_not_found","message":"not found"}`)
		case "/databases/429e5bf9-7fae-8080-bb4a-d94e1387655d":
			_, _ = io.WriteString(w, `{"id":"429e5bf9-7fae-8080-bb4a-d94e1387655d","data_sources":[{"id":"529e5bf9-7fae-8080-bb4a-d94e1387655d","name":"Beads Issues"}]}`)
		case "/data_sources/529e5bf9-7fae-8080-bb4a-d94e1387655d":
			_, _ = io.WriteString(w, `{"id":"529e5bf9-7fae-8080-bb4a-d94e1387655d","properties":{"Name":{"type":"title"}}}`)
		default:
			t.Fatalf("path = %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	resolved, err := ResolveDataSourceReference(context.Background(), client, "https://www.notion.so/workspace/429e5bf97fae8080bb4ad94e1387655d")
	if err != nil {
		t.Fatalf("ResolveDataSourceReference returned error: %v", err)
	}
	if resolved.DataSourceID != "529e5bf9-7fae-8080-bb4a-d94e1387655d" {
		t.Fatalf("data_source_id = %q", resolved.DataSourceID)
	}
	if resolved.Database == nil || resolved.Database.ID != "429e5bf9-7fae-8080-bb4a-d94e1387655d" {
		t.Fatalf("database = %+v", resolved.Database)
	}
}

// mustNotWait is the delay hook for a call that must make exactly one attempt.
// It reports the violation and still hands back an elapsed channel: returning a
// nil one would deadlock the select in wait, turning a clean failure into a
// two-minute timeout.
func mustNotWait(t *testing.T, msg string) func(time.Duration) <-chan time.Time {
	t.Helper()
	return func(time.Duration) <-chan time.Time {
		t.Error(msg)
		fired := make(chan time.Time, 1)
		fired <- time.Now()
		return fired
	}
}

// firesImmediately swaps out the retry wait with one that is already elapsed and
// records what was asked for, so backoff coverage costs no wall-clock time. It is
// called from doRequest on the test's own goroutine, never from a handler, so the
// recorded slice needs no synchronization.
func firesImmediately(recorded *[]time.Duration) func(time.Duration) <-chan time.Time {
	return func(d time.Duration) <-chan time.Time {
		*recorded = append(*recorded, d)
		fired := make(chan time.Time, 1)
		fired <- time.Now()
		return fired
	}
}

func TestClientQueryDataSourceRespectsConfiguredPageBound(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.ReadAll(r.Body)
		// Never terminates: the bound is the only thing that can stop this.
		_, _ = io.WriteString(w, `{"results":[{"id":"p"}],"has_more":true,"next_cursor":"c"}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL).WithMaxQueryPages(3)
	_, err := client.QueryDataSource(context.Background(), "ds_123")
	if err == nil {
		t.Fatal("QueryDataSource succeeded, want pagination-bound error")
	}
	if calls.Load() != 3 {
		t.Fatalf("requests = %d, want 3 (the configured bound)", calls.Load())
	}
	// The row figure is the actionable half: it is what a caller compares
	// against the size of their data source.
	if !strings.Contains(err.Error(), "3 pages") || !strings.Contains(err.Error(), "300 rows") {
		t.Fatalf("error should name the bound in pages and rows, got: %v", err)
	}
	// The message reaches CLI operators, who cannot call a Go method — so it has
	// to name a lever they can actually pull.
	if !strings.Contains(err.Error(), "notion.max_query_pages") ||
		!strings.Contains(err.Error(), "NOTION_MAX_QUERY_PAGES") {
		t.Fatalf("error should name the config key and env var, got: %v", err)
	}
}

func TestClientQueryDataSourceDefaultBoundUnchanged(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"results":[{"id":"p"}],"has_more":true,"next_cursor":"c"}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	if _, err := client.QueryDataSource(context.Background(), "ds_123"); err == nil {
		t.Fatal("QueryDataSource succeeded, want pagination-bound error")
	}
	// Hardcoded rather than compared against maxQueryPages: pinned to the
	// constant, this test would still pass if the default were lowered, which is
	// the exact regression it exists to catch.
	if calls.Load() != 50 {
		t.Fatalf("requests = %d, want the unchanged default 50", calls.Load())
	}
}

func TestClientRetriesRateLimitedRequestHonoringRetryAfter(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		// Every attempt must carry the payload. Reusing a drained reader would
		// send an empty second body, which the server sees as valid JSON-less
		// input and nothing else in this test would notice.
		if len(body) == 0 {
			t.Errorf("attempt %d sent an empty body — the reader was not rebuilt", n)
		}
		if n == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"code":"rate_limited","message":"Rate limited."}`)
			return
		}
		_, _ = io.WriteString(w, `{"results":[{"id":"page-1"}],"has_more":false}`)
	}))
	defer server.Close()

	var slept []time.Duration
	client := NewClient("secret-token").WithBaseURL(server.URL)
	client.after = firesImmediately(&slept)

	pages, err := client.QueryDataSource(context.Background(), "ds_123")
	if err != nil {
		t.Fatalf("QueryDataSource returned error: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(pages))
	}
	if calls.Load() != 2 {
		t.Fatalf("requests = %d, want 2 (one rate-limited, one retried)", calls.Load())
	}
	if len(slept) != 1 || slept[0] != 2*time.Second {
		t.Fatalf("slept = %v, want exactly the 2s the Retry-After header asked for", slept)
	}
}

// A 500 from a creating POST may have been reported after the row was already
// written, so the request is not replayed. This is aimed at CreatePage rather
// than the query endpoint on purpose: the duplicate-row rationale is only true of
// a call site that creates something, and the query endpoint creates nothing (see
// TestClientRetriesServerErrorOnQueryPost, which pins the opposite behavior
// there).
func TestClientDoesNotRetryServerErrorOnCreatingPost(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"code":"internal_server_error","message":"boom"}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	client.after = mustNotWait(t, "a creating POST must not be replayed on 5xx")

	if _, err := client.CreatePage(context.Background(), "ds_123", map[string]interface{}{}); err == nil {
		t.Fatal("CreatePage succeeded, want server error")
	}
	if calls.Load() != 1 {
		t.Fatalf("requests = %d, want 1", calls.Load())
	}
}

// The query endpoint is a read-only POST — it applies nothing, so replaying it is
// exactly as safe as replaying a GET. It is also the endpoint that needs the
// retry most: one sync issues up to MaxQueryPages sequential requests, and
// without this a single transient 502 or 529 on any page kills the whole sync.
func TestClientRetriesServerErrorOnQueryPost(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		status int
	}{
		{"overloaded", statusNotionOverloaded},
		{"bad gateway", http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				_, _ = io.ReadAll(r.Body)
				if n == 1 {
					w.WriteHeader(tc.status)
					return
				}
				_, _ = io.WriteString(w, `{"results":[{"id":"page-1"}],"has_more":false}`)
			}))
			defer server.Close()

			var slept []time.Duration
			client := NewClient("secret-token").WithBaseURL(server.URL)
			client.after = firesImmediately(&slept)

			pages, err := client.QueryDataSource(context.Background(), "ds_123")
			if err != nil {
				t.Fatalf("QueryDataSource returned error: %v", err)
			}
			if len(pages) != 1 {
				t.Fatalf("pages = %d, want 1", len(pages))
			}
			if calls.Load() != 2 {
				t.Fatalf("requests = %d, want 2 — the query POST applies nothing, so %d retries", calls.Load(), tc.status)
			}
		})
	}
}

// A 529 is Notion's overload status, and its edge can return it after the origin
// has already accepted the write. Replaying a creating POST on it is how one bd
// issue becomes two Notion rows.
func TestClientDoesNotRetryOverloadedOnCreatingPost(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(statusNotionOverloaded)
		_, _ = io.WriteString(w, `{"code":"service_unavailable","message":"overloaded"}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	client.after = mustNotWait(t, "a creating POST must not be replayed on 529")

	if _, err := client.CreatePage(context.Background(), "ds_123", map[string]interface{}{}); err == nil {
		t.Fatal("CreatePage succeeded, want overload error")
	}
	if calls.Load() != 1 {
		t.Fatalf("requests = %d, want 1", calls.Load())
	}
}

func TestClientRetriesOverloadedOnGet(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(statusNotionOverloaded)
			return
		}
		_, _ = io.WriteString(w, `{"id":"user-1","name":"Ada"}`)
	}))
	defer server.Close()

	var slept []time.Duration
	client := NewClient("secret-token").WithBaseURL(server.URL)
	client.after = firesImmediately(&slept)

	if _, err := client.GetCurrentUser(context.Background()); err != nil {
		t.Fatalf("GetCurrentUser returned error: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests = %d, want 2 (a GET has no side effects, so 529 retries)", calls.Load())
	}
}

// 429 stays verb-agnostic: Notion rejects a rate-limited request before
// processing it, so replaying even a creating POST cannot duplicate anything.
func TestClientRetriesRateLimitedCreatingPost(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		_, _ = io.ReadAll(r.Body)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"code":"rate_limited","message":"Rate limited."}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"page-1"}`)
	}))
	defer server.Close()

	var slept []time.Duration
	client := NewClient("secret-token").WithBaseURL(server.URL)
	client.after = firesImmediately(&slept)

	if _, err := client.CreatePage(context.Background(), "ds_123", map[string]interface{}{}); err != nil {
		t.Fatalf("CreatePage returned error: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests = %d, want 2 (429 is safe to replay on any verb)", calls.Load())
	}
}

func TestClientRetriesServerErrorOnGet(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, `{"id":"user-1","name":"Ada"}`)
	}))
	defer server.Close()

	var slept []time.Duration
	client := NewClient("secret-token").WithBaseURL(server.URL)
	client.after = firesImmediately(&slept)

	if _, err := client.GetCurrentUser(context.Background()); err != nil {
		t.Fatalf("GetCurrentUser returned error: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests = %d, want 2 (GET has no side effects, so it retries)", calls.Load())
	}
	// No Retry-After on the 502, so the jittered exponential fallback applies:
	// the 1s base plus up to half of it. An exact compare here would forbid the
	// jitter; see TestRetryDelay for the bounds and for the server-mandated case
	// that must stay exact.
	if len(slept) != 1 || slept[0] < time.Second || slept[0] >= 1500*time.Millisecond {
		t.Fatalf("slept = %v, want one wait in [1s, 1.5s) — the jittered 1s fallback", slept)
	}
}

// The retry wait must observe cancellation. A plain time.Sleep here would ignore
// it for up to maxRetryDelay per attempt, and QueryDataSource pays that per page
// — so the worst case scales with the very bound this client lets callers raise.
func TestClientRetryWaitObservesContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"code":"rate_limited","message":"Rate limited."}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	// Cancel from inside the delay hook, and hand back a channel that never
	// elapses. That pins the cancellation to the one moment this test is about:
	// the client is provably inside the wait, and only ctx can end the call.
	// Canceling from the handler instead would abort the response read, and the
	// call would return context.Canceled having never reached the wait at all —
	// passing for a reason that has nothing to do with the fix.
	client.after = func(time.Duration) <-chan time.Time {
		cancel()
		return make(chan time.Time)
	}

	done := make(chan error, 1)
	go func() {
		_, err := client.QueryDataSource(ctx, "ds_123")
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		// Cancellation is what the caller acts on, but on its own it says nothing
		// about why the call was in a backoff at all. The API error that triggered
		// the wait has to survive alongside it.
		if !strings.Contains(err.Error(), "Rate limited.") {
			t.Fatalf("error should also carry the API failure that triggered the wait, got: %v", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("requests = %d, want 1 — the retry must not be attempted", calls.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("QueryDataSource did not return after its context was canceled — the wait is not observing ctx.Done()")
	}
}

// A Retry-After longer than the client is willing to wait is refused outright.
// Clamping it down would spend the remaining attempts inside the window the
// server asked us to stay out of, which is how that window gets extended.
func TestClientRefusesRetryAfterLongerThanItWillWait(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"code":"rate_limited","message":"Rate limited."}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	client.after = mustNotWait(t, "a Retry-After beyond maxRetryDelay must not be clamped and retried")

	_, err := client.QueryDataSource(context.Background(), "ds_123")
	if err == nil {
		t.Fatal("QueryDataSource succeeded, want a refusal")
	}
	if calls.Load() != 1 {
		t.Fatalf("requests = %d, want 1 — no retry inside the window the server asked for", calls.Load())
	}
	// The operator needs the number the server actually asked for; without it
	// there is nothing to act on.
	if !strings.Contains(err.Error(), "1h0m0s") {
		t.Fatalf("error should name the delay the server asked for, got: %v", err)
	}
}

// The HTTP-date twin of the refusal above, driven through the client rather than
// asserted by TestParseRetryAfterHTTPDate's own arithmetic: that test proves the
// parsed value exceeds maxRetryDelay, and only this one proves the loop then
// refuses it. A change that routed the date form around the refusal — waiting
// out a clamped delay instead — would pass that test and fail this one.
func TestClientRefusesLongRetryAfterInHTTPDateForm(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.ReadAll(r.Body)
		// Computed per response, as in TestParseRetryAfterHTTPDate, because
		// time.Until makes any fixed date in the fixture expire.
		w.Header().Set("Retry-After", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"code":"rate_limited","message":"Rate limited."}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	client.after = mustNotWait(t, "a date-form Retry-After beyond maxRetryDelay must not be clamped and retried")

	_, err := client.QueryDataSource(context.Background(), "ds_123")
	if err == nil {
		t.Fatal("QueryDataSource succeeded, want a refusal")
	}
	if calls.Load() != 1 {
		t.Fatalf("requests = %d, want 1 — no retry inside the window the server asked for", calls.Load())
	}
	// The date form's delay moves with the clock, so the refusal is identified
	// by its ceiling rather than by the number the server asked for.
	if want := fmt.Sprintf("longer than the %s this client will wait", maxRetryDelay); !strings.Contains(err.Error(), want) {
		t.Fatalf("error should be the long-Retry-After refusal, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Rate limited.") {
		t.Fatalf("error should still carry the API failure, got: %v", err)
	}
}

// Every retry path eventually exhausts. This pins the loop's exit: exactly
// maxRequestAttempts requests, exactly one fewer wait, and the last attempt's
// error handed back rather than a nil one. Without it the break-and-fall-through
// restructure could go off by one with nothing to catch it.
func TestClientExhaustsRetriesAndReturnsTheLastError(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Each attempt answers differently, so the assertion below can tell the
		// LAST attempt's error from a stale one carried over from an earlier
		// round. With identical responses a stale lastErr reads as correct.
		n := calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprintf(w, `{"code":"bad_gateway","message":"failure on attempt %d"}`, n)
	}))
	defer server.Close()

	var slept []time.Duration
	client := NewClient("secret-token").WithBaseURL(server.URL)
	client.after = firesImmediately(&slept)

	_, err := client.GetCurrentUser(context.Background())
	if err == nil {
		t.Fatal("GetCurrentUser succeeded, want the last attempt's error")
	}
	if calls.Load() != maxRequestAttempts {
		t.Fatalf("requests = %d, want %d", calls.Load(), maxRequestAttempts)
	}
	if len(slept) != maxRequestAttempts-1 {
		t.Fatalf("waits = %d, want %d — one fewer than attempts", len(slept), maxRequestAttempts-1)
	}
	if !strings.Contains(err.Error(), "failure on attempt 5") {
		t.Fatalf("error should carry the LAST attempt's body, got: %v", err)
	}
}

// The refusal is evaluated before the exhaustion break, so a long Retry-After
// arriving on the FINAL attempt still produces the operator-facing message rather
// than the bare API error. The retry behavior is identical either way — the loop
// is over regardless — so what this pins is the diagnostic, on the one path the
// refusal test above cannot reach (it sends the header on attempt 1).
//
// Filed as a sibling rather than a case inside
// TestClientExhaustsRetriesAndReturnsTheLastError, as the review suggested, so
// that test stays a single-purpose pin on the loop's exit arithmetic.
func TestClientRefusesLongRetryAfterArrivingOnTheFinalAttempt(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		_, _ = io.ReadAll(r.Body)
		// Only the last attempt carries the long header. Earlier attempts must
		// retry normally, or the test would pass without ever reaching the final
		// attempt at all.
		if n == maxRequestAttempts {
			w.Header().Set("Retry-After", "3600")
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprintf(w, `{"code":"rate_limited","message":"rate limited on attempt %d"}`, n)
	}))
	defer server.Close()

	var slept []time.Duration
	client := NewClient("secret-token").WithBaseURL(server.URL)
	client.after = firesImmediately(&slept)

	_, err := client.GetCurrentUser(context.Background())
	if err == nil {
		t.Fatal("GetCurrentUser succeeded, want a refusal")
	}
	if calls.Load() != maxRequestAttempts {
		t.Fatalf("requests = %d, want %d", calls.Load(), maxRequestAttempts)
	}
	if len(slept) != maxRequestAttempts-1 {
		t.Fatalf("waits = %d, want %d — the refusal must not add a wait", len(slept), maxRequestAttempts-1)
	}
	if !strings.Contains(err.Error(), "1h0m0s") {
		t.Fatalf("error should name the delay the server asked for, got: %v", err)
	}
	// The refusal wraps the attempt's own error rather than replacing it.
	if !strings.Contains(err.Error(), "rate limited on attempt 5") {
		t.Fatalf("error should still carry the last attempt's body, got: %v", err)
	}
}

// The jitter split: a delay the server asked for is used exactly, and only the
// client's own exponential fallback is spread out. Jittering a server-mandated
// delay would push the retry past the window the server named; not jittering our
// own leaves N processes that tripped the same rate limit retrying in lockstep.
func TestRetryDelay(t *testing.T) {
	t.Parallel()

	t.Run("server-mandated delay is exact", func(t *testing.T) {
		t.Parallel()
		for attempt := 0; attempt < maxRequestAttempts; attempt++ {
			if got := retryDelay(attempt, 2*time.Second, true); got != 2*time.Second {
				t.Fatalf("retryDelay(%d, 2s, true) = %v, want exactly 2s", attempt, got)
			}
		}
	})

	t.Run("present zero means retry now", func(t *testing.T) {
		t.Parallel()
		// Distinct from an absent header, which falls back to the ladder below.
		if got := retryDelay(3, 0, true); got != 0 {
			t.Fatalf("retryDelay(3, 0, true) = %v, want 0 — a present Retry-After: 0 means retry now", got)
		}
	})

	t.Run("fallback is jittered within half its base", func(t *testing.T) {
		t.Parallel()
		for attempt := 0; attempt < 4; attempt++ {
			base := time.Duration(1<<attempt) * time.Second
			seen := map[time.Duration]bool{}
			for i := 0; i < 64; i++ {
				got := retryDelay(attempt, 0, false)
				if got < base || got >= base+base/2 {
					t.Fatalf("retryDelay(%d, 0, false) = %v, want [%v, %v)", attempt, got, base, base+base/2)
				}
				seen[got] = true
			}
			// 64 draws over a ≥1s window collapsing to one value means the jitter
			// is not actually being applied.
			if len(seen) < 2 {
				t.Fatalf("attempt %d produced a single delay %v over 64 draws — no jitter applied", attempt, seen)
			}
		}
	})

	t.Run("clamped to maxRetryDelay", func(t *testing.T) {
		t.Parallel()
		if got := retryDelay(0, maxRetryDelay+time.Second, true); got != maxRetryDelay {
			t.Fatalf("retryDelay = %v, want the %v ceiling", got, maxRetryDelay)
		}
		// The jittered fallback is clamped too: attempt 6 is 64s before jitter.
		if got := retryDelay(6, 0, false); got != maxRetryDelay {
			t.Fatalf("retryDelay(6, 0, false) = %v, want the %v ceiling", got, maxRetryDelay)
		}
	})
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		value  string
		want   time.Duration
		wantOK bool
	}{
		{"absent", "", 0, false},
		{"seconds", "7", 7 * time.Second, true},
		{"surrounding whitespace", "  7 ", 7 * time.Second, true},
		// Legal delta-seconds meaning "retry now" — reported as present, so the
		// caller can tell it from an absent header and not wait out a ladder the
		// server did not ask for.
		{"zero", "0", 0, true},
		{"negative", "-3", 0, false},
		{"fractional", "2.5", 0, false},
		{"garbage", "soon", 0, false},
		{"long", "3600", time.Hour, true},
		// The largest delta-seconds a Duration holds exactly, then one past it.
		// There the multiply would wrap negative and slip under the refusal, so it
		// saturates instead, as the date form's time.Until already does.
		{"largest representable", "9223372036", 9223372036 * time.Second, true},
		{"past the Duration range saturates", "9223372037", time.Duration(math.MaxInt64), true},
		// An HTTP-date already past is the date form of "retry now": understood,
		// so present, with no wait owed.
		{"http-date in the past", "Wed, 21 Oct 2020 07:28:00 GMT", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseRetryAfter(tc.value)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("parseRetryAfter(%q) = (%v, %t), want (%v, %t)", tc.value, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// The HTTP-date form is accepted, not guessed at, and that is what routes a long
// date-form wait into the same maxRetryDelay refusal as its delta-seconds twin.
// Parsed as zero — the old behavior — "Retry-After: <date +1h>" was instead
// retried at 1/2/4/8s inside the window the server asked us to stay out of.
//
// The deadline is computed rather than a literal, because time.Until makes any
// fixed date in the fixture expire.
func TestParseRetryAfterHTTPDate(t *testing.T) {
	t.Parallel()

	value := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	got, ok := parseRetryAfter(value)
	if !ok {
		t.Fatalf("parseRetryAfter(%q) reported the header absent, want the HTTP-date form understood", value)
	}
	// http.TimeFormat has one-second resolution and the clock moves between the
	// two calls, so the window is the assertion.
	if got <= 55*time.Minute || got > time.Hour {
		t.Fatalf("parseRetryAfter(%q) = %v, want ~1h", value, got)
	}
	// The whole point: this now lands in the refusal instead of around it.
	if got <= maxRetryDelay {
		t.Fatalf("parseRetryAfter(%q) = %v, want a value above the %v refusal threshold", value, got, maxRetryDelay)
	}
}
