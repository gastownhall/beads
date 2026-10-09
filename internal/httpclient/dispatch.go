// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/dispatch.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/steveyegge/beads/internal/httpclient/wire"
)

const (
	// maxIdempotentRetries bounds how many times dispatch comes back to a busy
	// server for one idempotent read. Three attempts total: a 503 is transient
	// slot pressure, and a client that hammered past this would BE the contention
	// it is waiting out.
	maxIdempotentRetries = 2
	// baseRetryBackoff is the wait before the first retry when the server named no
	// Retry-After. It doubles each attempt, capped at maxRetryBackoff.
	baseRetryBackoff = 200 * time.Millisecond
	// maxRetryBackoff caps a COMPUTED backoff. A server's own Retry-After is
	// already bounded by wire.Options.MaxRetryAfter and is honored as it is given.
	maxRetryBackoff = 5 * time.Second
)

// dispatch is this store's ONE door to the wire, and the only place the
// two-speed policy of D6 is spelled.
//
// Every operation goes through Preflight first — the policy itself lives in the
// wire package, which knows that five baseline operations return immediately and
// that every other one forces the handshake and consults the advertised token.
// Putting the call here rather than at each role method is what makes "every
// dispatch site honors the two-speed policy" a property of the code rather than
// of a reviewer's memory: a role that wants to reach the server has no other
// route to it.
//
// It is also the one place that knows an operation's HTTP method, so the bounded
// 503 retry lives here: the wire layer bounds Retry-After and hands it back but
// never sleeps or resends, precisely because whether a 503 is safe to come back
// to is a question only the method answers, and only a GET's answer is yes.
//
// A store with no transport is a build-wiring fault (ErrNoTransport), not the
// user's: the workspace and the server may both be fine.
func (s *Store) dispatch(ctx context.Context, req wire.Request, out any) error {
	if s.wire == nil {
		return fmt.Errorf("%w: cannot dial %s", ErrNoTransport, s.target)
	}
	// Before the preflight, so the tripwire costs no handshake: `limit=0` is
	// refused off loopback, and this client never sends it (unlimited_read.go).
	if err := refuseUnlimitedOnWire(req); err != nil {
		return err
	}
	if err := s.wire.Preflight(ctx, req.Op); err != nil {
		return err
	}

	err := s.wire.Do(ctx, req, out)
	// Only an idempotent GET is retried. A 503 can interrupt a non-idempotent
	// write whose first attempt already committed before the server ran out of
	// slots, so resending a POST/PATCH/DELETE could double-apply it — DELETE
	// included, since forgetMemory is a mutation the wire happens to spell as one.
	if req.Method != http.MethodGet {
		return err
	}
	for attempt := 0; attempt < maxIdempotentRetries; attempt++ {
		wait, retry := retryBackoff(err, attempt)
		if !retry {
			return err
		}
		if sleepErr := sleepWithContext(ctx, wait); sleepErr != nil {
			// The caller's deadline won out mid-backoff. Surface the server's busy
			// answer rather than the context error: "bd serve is busy" is the
			// actionable diagnosis, and the deadline is downstream of it.
			return err
		}
		err = s.wire.Do(ctx, req, out)
	}
	return err
}

// retryBackoff reports whether err is a 503-class busy answer worth coming back
// to, and how long to wait first. It honors the server's already-bounded
// Retry-After when present, and otherwise backs off exponentially from
// baseRetryBackoff. A nil error, a non-ProblemError, or any non-retryable
// problem stops the loop.
func retryBackoff(err error, attempt int) (time.Duration, bool) {
	var prob *wire.ProblemError
	if !errors.As(err, &prob) || !prob.Retryable() {
		return 0, false
	}
	if prob.RetryAfter > 0 {
		return prob.RetryAfter, true
	}
	backoff := baseRetryBackoff << attempt
	if backoff > maxRetryBackoff {
		backoff = maxRetryBackoff
	}
	return backoff, true
}

// sleepWithContext waits for d or until ctx is done, whichever comes first. A
// non-positive d is not a sleep at all, so it returns at once.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
