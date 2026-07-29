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

// HTTP header names. Defined as constants to avoid silent typos in
// rate-limit detection.
const (
	headerAccept             = "Accept"
	headerAPIVersion         = "X-GitHub-Api-Version"
	headerContentType        = "Content-Type"
	headerRetryAfter         = "Retry-After"
	headerRateLimitRemaining = "X-RateLimit-Remaining"
	headerRateLimitReset     = "X-RateLimit-Reset"
	headerRateLimitLimit     = "X-RateLimit-Limit"
	headerRateLimitResource  = "X-RateLimit-Resource"
)

// AuthError indicates a GitHub 403/401 that is not a rate limit (bad token,
// missing scopes, IP allowlist, etc.). Auth errors are not retried.
type AuthError struct {
	StatusCode int
	Message    string
	URL        string
}

func (e *AuthError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("github auth error (status %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("github auth error (status %d)", e.StatusCode)
}

// RateLimitErrorKind distinguishes primary (header-driven) from secondary
// (body-driven) GitHub rate limits, which require different backoff strategies.
type RateLimitErrorKind int

const (
	// RateLimitPrimary is the documented per-token cap, signaled by
	// X-RateLimit-Remaining=0 with a reliable reset epoch.
	RateLimitPrimary RateLimitErrorKind = iota
	// RateLimitSecondary covers abuse / content-creation / concurrency limits.
	// GitHub recommends a 60-second minimum backoff when no Retry-After is sent.
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

// RateLimitError represents a GitHub-imposed rate limit.
type RateLimitError struct {
	Kind       RateLimitErrorKind
	StatusCode int
	RetryAfter time.Duration // 0 if Retry-After was not present
	ResetAt    time.Time     // zero if X-RateLimit-Reset was not present
	Remaining  int           // -1 if absent
	Limit      int           // -1 if absent
	Resource   string
	Message    string
	URL        string
}

// RateLimitRetryAfter implements tracker.RateLimitedError. Returns
// Retry-After if set, else time-until-reset for primary limits, else the
// 60-second secondary minimum.
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

// RetryConfig controls retry behavior for the GitHub client.
type RetryConfig struct {
	MaxRetries int
	BaseDelay  time.Duration
	// SecondaryMinDelay is the floor when GitHub returns a secondary rate
	// limit without a Retry-After header. Per docs.github.com this should be
	// at least 60 seconds.
	SecondaryMinDelay time.Duration
	MaxBackoff        time.Duration
}

func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:        5,
		BaseDelay:         time.Second,
		SecondaryMinDelay: 60 * time.Second,
		MaxBackoff:        5 * time.Minute,
	}
}

// classifyRateLimit returns a *RateLimitError if the response carries any
// GitHub rate-limit signal. Caller must restrict invocation to 403/429.
func classifyRateLimit(headers http.Header, body []byte, statusCode int, urlStr string) *RateLimitError {
	if !isRateLimited(headers, body) {
		return nil
	}

	rlErr := &RateLimitError{
		StatusCode: statusCode,
		Remaining:  parseHeaderInt(headers, headerRateLimitRemaining, -1),
		Limit:      parseHeaderInt(headers, headerRateLimitLimit, -1),
		Resource:   headers.Get(headerRateLimitResource),
		Message:    extractGitHubMessage(body),
		URL:        urlStr,
	}

	if reset := parseHeaderInt(headers, headerRateLimitReset, 0); reset > 0 {
		rlErr.ResetAt = time.Unix(int64(reset), 0)
	}
	if ra := headers.Get(headerRetryAfter); ra != "" {
		if seconds, err := strconv.Atoi(ra); err == nil {
			rlErr.RetryAfter = time.Duration(seconds) * time.Second
		} else if t, err := http.ParseTime(ra); err == nil {
			if d := time.Until(t); d > 0 {
				rlErr.RetryAfter = d
			}
		}
	}

	// Primary limits set Remaining=0 and a reset epoch. Secondary limits
	// never expose Remaining=0; everything else routes there.
	if rlErr.Remaining == 0 {
		rlErr.Kind = RateLimitPrimary
	} else {
		rlErr.Kind = RateLimitSecondary
	}
	return rlErr
}

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

// rateLimitBodyMarkers are the documented substrings GitHub uses in its JSON
// error message to indicate a secondary / abuse rate limit. Required because
// secondary limits do NOT set X-RateLimit-Remaining=0, so headers alone are
// not sufficient.
var rateLimitBodyMarkers = []string{
	"secondary rate limit",
	"abuse",
}

// scanBytes caps body inspection to avoid lowercasing a 50MB response just
// to detect a marker that would only ever appear in the first few hundred
// bytes of a JSON error envelope.
const scanBytes = 4096

func isRateLimited(headers http.Header, body []byte) bool {
	if headers.Get(headerRetryAfter) != "" {
		return true
	}
	if headers.Get(headerRateLimitRemaining) == "0" {
		return true
	}
	if len(body) == 0 {
		return false
	}
	head := body
	if len(head) > scanBytes {
		head = head[:scanBytes]
	}
	lower := bytes.ToLower(head)
	for _, marker := range rateLimitBodyMarkers {
		if bytes.Contains(lower, []byte(marker)) {
			return true
		}
	}
	return false
}

// extractGitHubMessage returns the "message" field from a GitHub JSON error
// body, or a clamped raw-body fallback for non-JSON responses (e.g. CDN
// error pages).
func extractGitHubMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if body[0] == '{' {
		var env struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &env); err == nil {
			return env.Message
		}
	}
	const maxRaw = 200
	s := strings.TrimSpace(string(body))
	if len(s) > maxRaw {
		return s[:maxRaw] + "…"
	}
	return s
}
