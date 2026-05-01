package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// AuthError indicates a GitHub 403 that is NOT a rate limit — typically a
// missing/expired token or insufficient scopes. Auth errors must not be
// retried: the response will not change without operator intervention.
type AuthError struct {
	StatusCode int    // HTTP status (403 for auth, sometimes 401)
	Message    string // GitHub "message" field from the JSON error body
	URL        string // Request URL that triggered the error
}

func (e *AuthError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("github auth error (status %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("github auth error (status %d)", e.StatusCode)
}

// RateLimitErrorKind distinguishes the GitHub rate-limit flavors that require
// different client responses. Primary limits expose x-ratelimit-* headers and
// reset on a known schedule; secondary limits do not, and require a
// 60-second-minimum backoff per GitHub's published guidance.
type RateLimitErrorKind int

const (
	// RateLimitPrimary is the documented 5000/hr (or similar) per-token cap.
	// Signaled by x-ratelimit-remaining=0; the reset epoch is reliable.
	RateLimitPrimary RateLimitErrorKind = iota

	// RateLimitSecondary covers the undocumented "abuse" / content-creation /
	// concurrent-request limits. Signaled by a 403/429 whose body mentions
	// "secondary rate limit" or "abuse", or by the presence of Retry-After
	// without x-ratelimit-remaining=0. Per GitHub docs, clients should wait
	// at least one minute when no Retry-After is provided.
	RateLimitSecondary
)

func (k RateLimitErrorKind) String() string {
	switch k {
	case RateLimitPrimary:
		return "primary"
	case RateLimitSecondary:
		return "secondary"
	default:
		return "unknown"
	}
}

// RateLimitError represents a GitHub-imposed rate limit. It carries enough
// signal for the bulk-push engine to react appropriately — pause the loop,
// honor a server-mandated delay, or surface a clear message to the user.
type RateLimitError struct {
	Kind       RateLimitErrorKind
	StatusCode int           // 403 or 429
	RetryAfter time.Duration // From Retry-After header; 0 if not present
	ResetAt    time.Time     // From x-ratelimit-reset; zero if not present
	Remaining  int           // From x-ratelimit-remaining; -1 if not present
	Limit      int           // From x-ratelimit-limit; -1 if not present
	Resource   string        // From x-ratelimit-resource (e.g. "core", "search")
	Message    string        // GitHub "message" field from the JSON error body
	URL        string        // Request URL that triggered the error
}

// RateLimitRetryAfter implements the tracker.RateLimitedError interface so
// the bulk-push engine can detect a GitHub rate limit without importing this
// package. Returns the most authoritative wait duration available:
//
//  1. Server-supplied Retry-After (highest priority)
//  2. For primary limits, the time until ResetAt
//  3. For secondary limits, the GitHub-recommended 60s minimum
func (e *RateLimitError) RateLimitRetryAfter() time.Duration {
	if e.RetryAfter > 0 {
		return e.RetryAfter
	}
	if e.Kind == RateLimitPrimary && !e.ResetAt.IsZero() {
		if d := time.Until(e.ResetAt); d > 0 {
			return d
		}
	}
	if e.Kind == RateLimitSecondary {
		return 60 * time.Second
	}
	return 0
}

func (e *RateLimitError) Error() string {
	parts := []string{fmt.Sprintf("github %s rate limit (status %d)", e.Kind, e.StatusCode)}
	if e.RetryAfter > 0 {
		parts = append(parts, fmt.Sprintf("retry-after=%s", e.RetryAfter))
	}
	if !e.ResetAt.IsZero() {
		parts = append(parts, fmt.Sprintf("reset-at=%s", e.ResetAt.UTC().Format(time.RFC3339)))
	}
	if e.Resource != "" {
		parts = append(parts, fmt.Sprintf("resource=%s", e.Resource))
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	return strings.Join(parts, ": ")
}

// RetryConfig controls retry behavior for the GitHub client. Zero values are
// replaced with safe defaults inside NewClient.
type RetryConfig struct {
	// MaxRetries is the maximum number of retries (so the client makes up to
	// MaxRetries+1 attempts total).
	MaxRetries int

	// BaseDelay is the starting exponential-backoff delay. Doubles per attempt.
	BaseDelay time.Duration

	// SecondaryMinDelay is the minimum delay between attempts when GitHub
	// returns a secondary rate limit without a Retry-After header. Per
	// docs.github.com this should be at least 60 seconds.
	SecondaryMinDelay time.Duration

	// MaxBackoff caps the exponential backoff so a sustained outage doesn't
	// extend an individual delay past this value.
	MaxBackoff time.Duration
}

// DefaultRetryConfig returns the production retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:        5,
		BaseDelay:         time.Second,
		SecondaryMinDelay: 60 * time.Second,
		MaxBackoff:        5 * time.Minute,
	}
}

// classifyRateLimit inspects a response and returns a *RateLimitError if the
// response represents a rate limit, or nil otherwise. It does not look at the
// status code — caller is responsible for restricting this to 403/429.
func classifyRateLimit(headers http.Header, body []byte, statusCode int, urlStr string) *RateLimitError {
	if !isRateLimited(headers, body) {
		return nil
	}

	rlErr := &RateLimitError{
		StatusCode: statusCode,
		Remaining:  parseHeaderInt(headers, "X-RateLimit-Remaining", -1),
		Limit:      parseHeaderInt(headers, "X-RateLimit-Limit", -1),
		Resource:   headers.Get("X-RateLimit-Resource"),
		Message:    extractGitHubMessage(body),
		URL:        urlStr,
	}

	if reset := parseHeaderInt(headers, "X-RateLimit-Reset", 0); reset > 0 {
		rlErr.ResetAt = time.Unix(int64(reset), 0)
	}
	if ra := headers.Get("Retry-After"); ra != "" {
		if seconds, err := strconv.Atoi(ra); err == nil {
			rlErr.RetryAfter = time.Duration(seconds) * time.Second
		} else if t, err := http.ParseTime(ra); err == nil {
			if d := time.Until(t); d > 0 {
				rlErr.RetryAfter = d
			}
		}
	}

	// Disambiguate primary vs. secondary. Primary is the only kind that sets
	// x-ratelimit-remaining=0 — secondary limits never expose remaining=0.
	if rlErr.Remaining == 0 {
		rlErr.Kind = RateLimitPrimary
	} else {
		rlErr.Kind = RateLimitSecondary
	}
	return rlErr
}

// parseHeaderInt parses a header value as an int, returning fallback on error
// or absence.
func parseHeaderInt(headers http.Header, key string, fallback int) int {
	v := headers.Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// rateLimitBodyMarkers are case-insensitive substrings GitHub uses in the
// JSON error body to indicate a secondary / abuse rate limit. Per
// https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api,
// these are the only reliable signal — secondary limits do NOT set
// x-ratelimit-remaining=0.
var rateLimitBodyMarkers = []string{
	"secondary rate limit",
	"abuse",
	"rate limit",
}

// isRateLimited reports whether a response carries any GitHub-recognized
// rate-limit signal. It returns true when ANY of the following hold:
//   - the Retry-After header is present (server explicitly asks us to wait),
//   - x-ratelimit-remaining is "0" (primary limit exhausted),
//   - the JSON error body matches one of rateLimitBodyMarkers.
//
// Used to disambiguate a rate-limited 403 (retryable) from an auth-failure
// 403 (not retryable).
func isRateLimited(headers http.Header, body []byte) bool {
	if headers.Get("Retry-After") != "" {
		return true
	}
	if headers.Get("X-RateLimit-Remaining") == "0" {
		return true
	}
	if len(body) == 0 {
		return false
	}
	lower := bytes.ToLower(body)
	for _, marker := range rateLimitBodyMarkers {
		if bytes.Contains(lower, []byte(marker)) {
			return true
		}
	}
	return false
}

// extractGitHubMessage returns the "message" field from a GitHub JSON error
// body, or an empty string if it cannot be parsed. GitHub's standard error
// shape is {"message": "...", "documentation_url": "..."}.
func extractGitHubMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var env struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return strings.TrimSpace(string(body))
	}
	return env.Message
}
