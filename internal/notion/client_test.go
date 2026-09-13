package notion

import (
	"context"
	"errors"
	"fmt"
	"io"
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

func TestClientDoesNotRetryServerErrorOnPost(t *testing.T) {
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
	client.after = mustNotWait(t, "POST 5xx must not be retried")

	if _, err := client.QueryDataSource(context.Background(), "ds_123"); err == nil {
		t.Fatal("QueryDataSource succeeded, want server error")
	}
	// Replaying a POST that may already have been applied server-side is how
	// duplicates get made; one attempt is the correct behavior.
	if calls.Load() != 1 {
		t.Fatalf("requests = %d, want 1", calls.Load())
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
	// No Retry-After on the 502, so the exponential fallback applies.
	if len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("slept = %v, want the 1s exponential fallback", slept)
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

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"absent", "", 0},
		{"seconds", "7", 7 * time.Second},
		{"surrounding whitespace", "  7 ", 7 * time.Second},
		{"zero", "0", 0},
		{"negative", "-3", 0},
		{"fractional", "2.5", 0},
		// The HTTP-date form is rejected rather than guessed at: a misparsed date
		// yielding zero would retry immediately, the opposite of what was asked.
		{"http-date", "Wed, 21 Oct 2026 07:28:00 GMT", 0},
		{"garbage", "soon", 0},
		{"long", "3600", time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseRetryAfter(tc.value); got != tc.want {
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
