package versioncontrolops

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// remoteTransferTimeoutKey is the context key an embedded remote entry point
// attaches its configured transfer deadline under (GH#7989): the ten
// embeddeddolt entry points wrap their caller context with
// WithRemoteTransferTimeout before reaching the push/fetch statements below.
// The value is deliberately not read from global configuration here — only a
// context that carries it opts in, so every other caller of these statements
// (server-mode transfers run through the dolt CLI, not through SQL) keeps its
// existing behavior.
type remoteTransferTimeoutKey struct{}

// WithRemoteTransferTimeout attaches d, the configured per-statement transfer
// deadline, to ctx. d <= 0 disables the deadline: the statement sites run
// fn on ctx unchanged.
func WithRemoteTransferTimeout(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, remoteTransferTimeoutKey{}, d)
}

// remoteTransferTimeout returns the deadline attached to ctx, 0 when absent
// or non-positive.
func remoteTransferTimeout(ctx context.Context) time.Duration {
	d, _ := ctx.Value(remoteTransferTimeoutKey{}).(time.Duration)
	return d
}

// remoteTimeoutConfigKey / remoteTimeoutEnvKey name the configuration this
// deadline comes from, so a timed-out statement can tell the operator which
// knob to turn (GH#7989 ask 2). They must stay in sync with
// config.DoltRemoteTimeout.
const (
	remoteTimeoutConfigKey = "dolt.remote-timeout"
	remoteTimeoutEnvKey    = "BD_DOLT_REMOTE_TIMEOUT"
)

// TransferTimeoutError reports that an embedded remote transfer statement
// (DOLT_PUSH / DOLT_FETCH) exceeded the configured dolt.remote-timeout
// deadline and was canceled. It preserves the underlying engine error (the
// statement's own failure after cancellation) and satisfies
// errors.Is(_, context.DeadlineExceeded) so callers that classify deadline
// errors keep working. It is only returned when the statement's own deadline
// fired while the caller's context was still live — a caller cancellation or
// an earlier caller deadline keeps its identity and is never blamed on this
// configuration.
type TransferTimeoutError struct {
	// Op names the operation that timed out ("push", "fetch", …).
	Op string
	// Timeout is the configured duration that elapsed.
	Timeout time.Duration
	// Err is the statement's own error, observed after cancellation.
	Err error
}

func (e *TransferTimeoutError) Error() string {
	return fmt.Sprintf("%s: remote transfer exceeded the %s deadline (config %s, env %s): %v",
		e.Op, e.Timeout, remoteTimeoutConfigKey, remoteTimeoutEnvKey, e.Err)
}

func (e *TransferTimeoutError) Unwrap() error { return e.Err }

func (e *TransferTimeoutError) Is(target error) bool { return target == context.DeadlineExceeded }

// withTransferTimeout runs fn — one DOLT_PUSH or DOLT_FETCH statement — under
// the transfer deadline attached to ctx, when there is one. fn receives the
// derived statement context; every other context (the merge and settle steps
// around a pull's fetch, connection opening, blocked-work recomputation)
// keeps the caller's context.
//
// The statement budget starts when fn starts, which the statement sites
// arrange to be after their remote environment guards, so the deadline does
// not burn while env scrubbing runs.
func withTransferTimeout(ctx context.Context, op string, fn func(context.Context) error) error {
	d := remoteTransferTimeout(ctx)
	if d <= 0 {
		// Not opted in: other callers keep their existing behavior.
		return fn(ctx)
	}
	deadline := time.Now().Add(d)
	if caller, ok := ctx.Deadline(); ok && !caller.After(deadline) {
		// An earlier caller deadline wins and keeps its identity.
		return fn(ctx)
	}
	stmtCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	err := fn(stmtCtx)
	// Wrap only when this deadline — not the caller's cancellation — ended
	// the statement, and only for a failure: a success racing the timer is a
	// success.
	if err != nil && ctx.Err() == nil && errors.Is(stmtCtx.Err(), context.DeadlineExceeded) {
		err = &TransferTimeoutError{Op: op, Timeout: d, Err: err}
	}
	return err
}