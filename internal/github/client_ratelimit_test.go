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

func newRateLimitTestClient(baseURL string) *Client {
	c := NewClient("test-token", "owner", "repo")
	c.BaseURL = strings.TrimSuffix(baseURL, "/")
	return c
}

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

// A 403 with no rate-limit signal is auth, not transient — return immediately
// as *AuthError instead of retrying and burying the cause.
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
		t.Errorf("expected 1 attempt for auth-403, got %d", got)
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Errorf("expected *AuthError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("error should preserve GitHub message, got: %v", err)
	}
}

// A 403 with X-RateLimit-Remaining=0 must sleep until X-RateLimit-Reset, not
// fall back to the client's own exponential backoff.
func TestDoRequest_Primary403_HonorsResetHeader(t *testing.T) {
	// X-RateLimit-Reset is whole-seconds Unix epoch; pick a reset far enough
	// out that seconds-precision rounding doesn't put it in the past.
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
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for user ID 12345."}`))
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"login":"octocat"}`))
	}))
	defer srv.Close()

	c := fastRetryTestClient(srv.URL)
	// Force exponential backoff to a no-op so only ResetAt could explain the wait.
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
	if elapsed < 400*time.Millisecond {
		t.Errorf("expected to sleep until reset (~1s), slept %v", elapsed)
	}
}

// A 403 whose body mentions "secondary rate limit" without a Retry-After must
// produce a typed *RateLimitError and respect SecondaryMinDelay between retries.
func TestDoRequest_Secondary403_ReturnsTypedErrorWithMinDelay(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`))
	}))
	defer srv.Close()

	c := fastRetryTestClient(srv.URL)

	start := time.Now()
	_, _, err := c.doRequest(context.Background(), http.MethodPost, srv.URL+"/repos/owner/repo/issues", map[string]string{"title": "x"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected rate-limit error, got nil")
	}
	wantAttempts := int32(c.Retry.MaxRetries + 1)
	if got := atomic.LoadInt32(&attempts); got != wantAttempts {
		t.Errorf("expected %d attempts, got %d", wantAttempts, got)
	}
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
	if min := 3 * c.Retry.SecondaryMinDelay; elapsed < min {
		t.Errorf("elapsed %v below SecondaryMinDelay floor %v", elapsed, min)
	}
}

// When the final retry attempt also hits a rate limit, return the typed
// *RateLimitError immediately instead of sleeping once more.
func TestDoRequest_RateLimitFinalAttemptDoesNotSleep(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit. Please wait before you try again."}`))
	}))
	defer srv.Close()

	c := fastRetryTestClient(srv.URL)
	c.Retry.MaxRetries = 0

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := c.doRequest(ctx, http.MethodPost, srv.URL+"/repos/owner/repo/issues", map[string]string{"title": "x"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected rate-limit error, got nil")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("expected 1 attempt, got %d", got)
	}
	var rlErr *RateLimitError
	if !errors.As(err, &rlErr) {
		t.Fatalf("expected *RateLimitError, got %T: %v", err, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("final attempt slept until context deadline instead of returning rate-limit error: %v", err)
	}
	if elapsed >= 100*time.Millisecond {
		t.Errorf("final rate-limit attempt should return before context timeout, took %v", elapsed)
	}
}
