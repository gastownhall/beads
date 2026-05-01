package tracker

import (
	"errors"
	"fmt"
	"time"
)

// RateLimitedError is implemented by provider errors when the upstream API
// has rate-limited the request. The push loop uses errors.As against this
// interface to detect rate limiting without importing any provider package.
type RateLimitedError interface {
	error
	// RateLimitRetryAfter returns the wait before retrying. Zero means
	// "the server didn't say".
	RateLimitRetryAfter() time.Duration
}

func isRateLimitedErr(err error) bool {
	var rl RateLimitedError
	return errors.As(err, &rl)
}

// warnRateLimitAbort emits the standard "we hit a provider rate limit, the
// rest of the queue is left for next sync" message.
func (e *Engine) warnRateLimitAbort(err error, remaining int) {
	e.warn("Aborting push to %s: provider rate limit hit (%s); %d issue(s) skipped — retry after the cooldown",
		e.Tracker.DisplayName(), formatRateLimitWait(err), remaining)
}

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
