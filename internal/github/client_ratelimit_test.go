package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newRateLimitTestClient returns a Client wired to the given test-server URL
// with the given personal access token. Caller may further mutate the returned
// client (e.g. to shorten retry delays).
func newRateLimitTestClient(baseURL string) *Client {
	c := NewClient("test-token", "owner", "repo")
	c.BaseURL = strings.TrimSuffix(baseURL, "/")
	return c
}

// fastRetryTestClient returns a client whose retry delays are short enough
// for unit tests but still let us assert "minimum delay was applied".
func fastRetryTestClient(baseURL string) *Client {
	c := newRateLimitTestClient(baseURL)
	c.Retry = RetryConfig{
		MaxRetries:        3,
		BaseDelay:         5 * time.Millisecond,
		SecondaryMinDelay: 50 * time.Millisecond,
		MaxBackoff:        time.Second,
	}
	return c
}

// TestDoRequest_Auth403_DoesNotRetry asserts that a 403 with no rate-limit
// signals (no x-ratelimit-remaining=0, no Retry-After, no "secondary rate
// limit" / "abuse" body) is treated as an auth failure and surfaced
// immediately without retrying.
//
// Today the client retries every 403 four times, which (a) wastes time on
// permanent failures and (b) hides the real cause behind "transient error
// 403 (attempt 4/4)".
func TestDoRequest_Auth403_DoesNotRetry(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest"}`))
	}))
	defer srv.Close()

	c := newRateLimitTestClient(srv.URL)

	_, _, err := c.doRequest(context.Background(), http.MethodGet, srv.URL+"/repos/owner/repo/issues", nil)
	if err == nil {
		t.Fatal("expected auth error, got nil")
	}

	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("expected exactly 1 attempt for auth-403, got %d", got)
	}

	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Errorf("expected *AuthError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("error should preserve GitHub message, got: %v", err)
	}
}

// TestDoRequest_Primary403_HonorsResetHeader asserts that a 403 carrying the
// x-ratelimit-remaining=0 / x-ratelimit-reset signals is detected as a primary
// rate limit, the client sleeps until the reset epoch (rather than its own
// exponential backoff), and the typed error preserves Limit/Remaining/Reset
// for callers to inspect.
func TestDoRequest_Primary403_HonorsResetHeader(t *testing.T) {
	// X-RateLimit-Reset is whole-seconds Unix epoch; pick a reset far enough
	// in the future that seconds-precision rounding doesn't put it in the past.
	resetAt := time.Now().Add(1500 * time.Millisecond)

	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))
		w.Header().Set("X-RateLimit-Resource", "core")
		if n == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for user ID 12345.","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api"}`))
			return
		}
		// Second call succeeds.
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"login":"octocat"}`))
	}))
	defer srv.Close()

	c := fastRetryTestClient(srv.URL)
	// Make sure exponential backoff alone wouldn't sleep this long, so the
	// only way the test passes is if ResetAt is honored.
	c.Retry.BaseDelay = time.Microsecond
	c.Retry.SecondaryMinDelay = time.Microsecond

	start := time.Now()
	body, _, err := c.doRequest(context.Background(), http.MethodGet, srv.URL+"/user", nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected success after retry, got %v", err)
	}
	if !strings.Contains(string(body), "octocat") {
		t.Errorf("expected octocat in body, got %s", body)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("expected 2 attempts, got %d", got)
	}
	// Reset is ~1.5s in the future but rounds down to a whole-second epoch,
	// so the actual sleep is somewhere in [500ms, 1500ms]. Assert at least
	// 400ms — anything less means we ignored the reset header.
	if elapsed < 400*time.Millisecond {
		t.Errorf("expected to sleep until reset (~1s), slept %v", elapsed)
	}
}

// TestDoRequest_Secondary403_ReturnsTypedErrorWithMinDelay asserts the two
// behaviors GitHub explicitly requires for secondary rate limits when no
// Retry-After header is present:
//
//  1. The final error (after retries are exhausted) is a *RateLimitError
//     with Kind=Secondary, not a generic "transient error 403". Without a
//     typed error the bulk-push engine has no way to react differently to
//     a rate limit vs. any other failure.
//  2. The client waits at least SecondaryMinDelay between attempts. Per
//     docs.github.com: "wait for at least one minute before retrying"
//     when Retry-After is missing on a secondary limit.
func TestDoRequest_Secondary403_ReturnsTypedErrorWithMinDelay(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again.","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api"}`))
	}))
	defer srv.Close()

	c := fastRetryTestClient(srv.URL)

	start := time.Now()
	_, _, err := c.doRequest(context.Background(), http.MethodPost, srv.URL+"/repos/owner/repo/issues", map[string]string{"title": "x"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected rate-limit error, got nil")
	}

	// Should have retried up to MaxRetries+1 times.
	wantAttempts := int32(c.Retry.MaxRetries + 1)
	if got := atomic.LoadInt32(&attempts); got != wantAttempts {
		t.Errorf("expected %d attempts, got %d", wantAttempts, got)
	}

	// Must surface as a typed *RateLimitError, not a generic "transient" error.
	var rlErr *RateLimitError
	if !errors.As(err, &rlErr) {
		t.Fatalf("expected *RateLimitError, got %T: %v", err, err)
	}
	if rlErr.Kind != RateLimitSecondary {
		t.Errorf("expected Kind=RateLimitSecondary, got %v", rlErr.Kind)
	}
	if !strings.Contains(rlErr.Error(), "secondary rate limit") {
		t.Errorf("error should preserve GitHub message, got: %v", rlErr)
	}

	// Must have honored SecondaryMinDelay between retries. With MaxRetries=3
	// there are 3 inter-attempt waits; total elapsed must be at least
	// 3 * SecondaryMinDelay (modulo a small slack for handler scheduling).
	minExpected := 3 * c.Retry.SecondaryMinDelay
	if elapsed < minExpected {
		t.Errorf("elapsed %v is below minimum %v — SecondaryMinDelay not enforced", elapsed, minExpected)
	}
}
