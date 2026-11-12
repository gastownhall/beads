package storage

import (
	"fmt"
	"time"
)

// Configuration names an operator can use to change the pooled read deadline.
// They are spelled out in ReadTimeoutError's message so the error names its
// own remedy.
const (
	PoolReadTimeoutEnv    = "BEADS_DOLT_POOL_READ_TIMEOUT"
	PoolReadTimeoutConfig = "dolt.pool-read-timeout"
)

// ReadTimeoutError reports a pooled read that failed at or past the store's
// per-I/O read deadline and was therefore not retried: re-sending a read that
// the server may still be running only stacks another session behind it
// (GH#5258).
//
// Confirmed is true only when the failure still carries a network read-timeout
// cause. The MySQL driver usually replaces that cause with a bare "invalid
// connection" when it closes the socket, and then the deadline can only be
// inferred from elapsed time: Confirmed is false and the message says
// "possible". A slow attempt can also fail for another reason (a pool wait, an
// earlier statement, a reset late in a long read), so an unconfirmed error is
// a diagnosis, not proof that the server is stuck.
//
// Err is the attempt's own error, with the operation that failed in its text;
// errors.Is and errors.As reach it through Unwrap.
type ReadTimeoutError struct {
	// Timeout is the effective per-I/O read deadline of the pooled connection.
	Timeout time.Duration
	// Confirmed reports that a network read-timeout cause was retained.
	Confirmed bool
	// Err is the failed attempt's error.
	Err error
}

func (e *ReadTimeoutError) Error() string {
	diagnosis := "possible pool read timeout"
	if e.Confirmed {
		diagnosis = "pool read timed out"
	}
	cause := "read failed"
	if e.Err != nil {
		cause = e.Err.Error()
	}
	return fmt.Sprintf("%s: %s (configured %s); read not retried; check %s or %s",
		cause, diagnosis, e.Timeout, PoolReadTimeoutEnv, PoolReadTimeoutConfig)
}

func (e *ReadTimeoutError) Unwrap() error { return e.Err }
