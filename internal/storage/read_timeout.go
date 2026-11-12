package storage

import (
	"fmt"
	"time"
)

// ReadTimeoutError reports a pooled read that died at — or was cut off
// after — the server pool's per-I/O read deadline (default 10s; the
// BEADS_DOLT_POOL_READ_TIMEOUT / dolt.pool-read-timeout knobs). It is raised
// by the storage layer's pooled read helpers only, never for writes or for
// dedicated long-timeout connections, and it is terminal: the failed read is
// NOT resubmitted, because replaying the same statement against the same
// deadline re-runs the same doomed read.
//
// Confirmed is true only when the error chain still carries a retained
// network read timeout; then the deadline provably fired. The common failure
// does not retain one: the MySQL driver converts a dead read into
// "invalid connection" and drops the cause, so the classification is inferred
// from elapsed time and says so — "possible pool read timeout" — rather than
// claiming certainty the evidence does not carry. Either way the message
// names the configured deadline and both override knobs so an operator can
// act without reading the source.
//
// The message deliberately exposes no DSN, address or credentials: Err carries
// the cause (and its operation context, where the caller wrapped one), which
// is what Error() prints. It also does not assert that the server is wedged or
// needs a restart — a read that outlives the deadline on an overloaded but
// healthy server is the ordinary case the knobs exist for.
type ReadTimeoutError struct {
	// Timeout is the effective per-I/O read deadline that was in force.
	Timeout time.Duration
	// Confirmed reports a retained network read-timeout cause. False means
	// the classification is inferred and the message says "possible".
	Confirmed bool
	// Err is the classified cause. Never nil.
	Err error
}

// Error renders a single line: the cause first (preserving the operation
// context the cause chain already carries), then the qualifier, the
// configured deadline, the no-retry statement, and both override knobs.
func (e *ReadTimeoutError) Error() string {
	qualifier := "possible pool read timeout"
	if e.Confirmed {
		qualifier = "pool read timed out"
	}
	cause := ""
	if e.Err != nil {
		cause = e.Err.Error() + ": "
	}
	return fmt.Sprintf("%s%s (configured %s); read not retried; check BEADS_DOLT_POOL_READ_TIMEOUT or dolt.pool-read-timeout",
		cause, qualifier, e.Timeout)
}

// Unwrap returns the classified cause, so errors.Is/As on the driver's error
// identity (and on context cancellation, which is never classified) keeps
// working through the typed error.
func (e *ReadTimeoutError) Unwrap() error { return e.Err }
