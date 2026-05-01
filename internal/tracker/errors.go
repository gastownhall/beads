package tracker

import (
	"errors"
	"fmt"
	"time"
)

// RateLimitedError is implemented by errors returned from a Tracker when the
// underlying provider has rate-limited the request. The push loop uses it to
// distinguish "the server told us to slow down" from any other failure, so
// it can stop hammering the API instead of cascading the same failure across
// every remaining issue.
//
// Provider-specific error types (e.g. github.RateLimitError) implement this
// interface via a method on their concrete type. The tracker package does not
// import any provider package — it only relies on this duck-typed contract.
type RateLimitedError interface {
	error

	// RateLimitRetryAfter returns how long the client should wait before
	// retrying. Zero means "the server didn't tell us; assume default".
	RateLimitRetryAfter() time.Duration
}

// isRateLimitedErr reports whether err (or any wrapped cause) implements
// RateLimitedError.
func isRateLimitedErr(err error) bool {
	var rl RateLimitedError
	return errors.As(err, &rl)
}

// formatRateLimitWait returns a human-readable description of how long the
// caller should wait before retrying the rate-limited operation. Returns
// "unknown" when the underlying error did not specify.
func formatRateLimitWait(err error) string {
	var rl RateLimitedError
	if !errors.As(err, &rl) {
		return "unknown"
	}
	d := rl.RateLimitRetryAfter()
	if d <= 0 {
		return "unknown"
	}
	return fmt.Sprintf("retry after %s", d.Round(time.Second))
}
