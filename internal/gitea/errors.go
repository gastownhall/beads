package gitea

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
)

const (
	headerAccept             = "Accept"
	headerContentType        = "Content-Type"
	headerRetryAfter         = "Retry-After"
	headerRateLimitRemaining = "X-RateLimit-Remaining"
	headerRateLimitReset     = "X-RateLimit-Reset"
	headerRateLimitLimit     = "X-RateLimit-Limit"
)

// APIError is a non-retryable Gitea/Forgejo API failure carrying the HTTP status code.
type APIError struct {
	StatusCode int
	Body       string
	URL        string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error: %s (status %d)", e.Body, e.StatusCode)
}

// IsNotFound reports whether err is, or wraps, an HTTP 404 APIError.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// IsForbidden reports whether err is, or wraps, an HTTP 403 APIError.
func IsForbidden(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden
}

// RateLimitError represents a Gitea/Forgejo HTTP 429 rate-limit response.
type RateLimitError struct {
	StatusCode int
	RetryAfter time.Duration
	ResetAt    time.Time
	Remaining  int
	Limit      int
	Message    string
	URL        string
}

var _ tracker.RateLimitedError = (*RateLimitError)(nil)

// RateLimitRetryAfter implements tracker.RateLimitedError.
func (e *RateLimitError) RateLimitRetryAfter() time.Duration {
	if e.RetryAfter > 0 {
		return e.RetryAfter
	}
	if !e.ResetAt.IsZero() {
		if d := time.Until(e.ResetAt); d > 0 {
			return d
		}
	}
	return 0
}

func (e *RateLimitError) Error() string {
	parts := []string{fmt.Sprintf("gitea rate limit (status %d)", e.StatusCode)}
	if e.RetryAfter > 0 {
		parts = append(parts, fmt.Sprintf("retry-after=%s", e.RetryAfter))
	}
	if !e.ResetAt.IsZero() {
		parts = append(parts, fmt.Sprintf("reset-at=%s", e.ResetAt.UTC().Format(time.RFC3339)))
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	return strings.Join(parts, ": ")
}

// RetryConfig controls retry behavior for the Gitea/Forgejo client.
type RetryConfig struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxBackoff time.Duration
}

// DefaultRetryConfig returns the default retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries: MaxRetries,
		BaseDelay:  RetryDelay,
		MaxBackoff: 5 * time.Minute,
	}
}

func parseRateLimitError(headers http.Header, body []byte, statusCode int, urlStr string) *RateLimitError {
	rlErr := &RateLimitError{
		StatusCode: statusCode,
		Remaining:  parseHeaderInt(headers, headerRateLimitRemaining, -1),
		Limit:      parseHeaderInt(headers, headerRateLimitLimit, -1),
		Message:    extractAPIMessage(body),
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

func extractAPIMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if body[0] == '{' {
		var env struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &env); err == nil && env.Message != "" {
			return env.Message
		}
	}
	const maxRaw = 200
	s := strings.TrimSpace(string(body))
	if len(s) > maxRaw {
		return s[:maxRaw] + "..."
	}
	return s
}
