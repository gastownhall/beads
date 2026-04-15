// Package retry provides a small exponential-backoff retry helper with jitter
// and context cancellation. It replaces the project's direct dependency on
// github.com/cenkalti/backoff/v4.
//
// The ExponentialBackOff type is structurally compatible with
// cenkalti/backoff/v4's BackOff interface (Reset + NextBackOff), so it can
// still be passed to third-party APIs that accept that interface (notably
// dolthub/driver's Config.BackOff field) without importing cenkalti here.
package retry

import (
	"context"
	"errors"
	"math/rand"
	"time"
)

// Stop mirrors cenkalti/backoff's Stop sentinel: returning it from NextBackOff
// tells a retry loop that no further retries should be attempted.
const Stop time.Duration = -1

// Default values match cenkalti/backoff/v4's ExponentialBackOff so that
// behavior is unchanged after the swap.
const (
	defaultInitialInterval     = 500 * time.Millisecond
	defaultMultiplier          = 1.5
	defaultMaxInterval         = 60 * time.Second
	defaultMaxElapsedTime      = 15 * time.Minute
	defaultRandomizationFactor = 0.5
)

// ExponentialBackOff is a stateful exponential-backoff policy with jitter.
//
// Fields can be tuned after construction. MaxElapsedTime of 0 means no overall
// deadline — retries continue until NextBackOff is told to stop via context
// cancellation at the caller's retry loop.
type ExponentialBackOff struct {
	InitialInterval     time.Duration
	Multiplier          float64
	MaxInterval         time.Duration
	MaxElapsedTime      time.Duration
	RandomizationFactor float64

	start           time.Time
	currentInterval time.Duration
}

// NewExponentialBackOff returns an ExponentialBackOff with defaults that match
// cenkalti/backoff/v4's NewExponentialBackOff.
func NewExponentialBackOff() *ExponentialBackOff {
	b := &ExponentialBackOff{
		InitialInterval:     defaultInitialInterval,
		Multiplier:          defaultMultiplier,
		MaxInterval:         defaultMaxInterval,
		MaxElapsedTime:      defaultMaxElapsedTime,
		RandomizationFactor: defaultRandomizationFactor,
	}
	b.Reset()
	return b
}

// Reset rewinds the backoff to its initial state and restarts the elapsed-time
// clock. Callers should Reset between independent retry sequences.
func (b *ExponentialBackOff) Reset() {
	b.currentInterval = b.InitialInterval
	b.start = time.Now()
}

// NextBackOff returns the duration to sleep before the next retry, or Stop if
// the MaxElapsedTime budget has been exhausted. The current interval is
// advanced by Multiplier (capped at MaxInterval) on every call, matching
// cenkalti/backoff's behavior.
func (b *ExponentialBackOff) NextBackOff() time.Duration {
	if b.currentInterval <= 0 {
		b.currentInterval = b.InitialInterval
		if b.currentInterval <= 0 {
			b.currentInterval = defaultInitialInterval
		}
	}
	if b.start.IsZero() {
		b.start = time.Now()
	}
	if b.MaxElapsedTime > 0 && time.Since(b.start) >= b.MaxElapsedTime {
		return Stop
	}

	interval := b.currentInterval
	// Apply symmetric randomization around the current interval.
	if b.RandomizationFactor > 0 {
		delta := b.RandomizationFactor * float64(interval)
		min := float64(interval) - delta
		//nolint:gosec // jitter does not need cryptographic randomness
		interval = time.Duration(min + rand.Float64()*(2*delta))
		if interval < 0 {
			interval = 0
		}
	}

	// Advance the current interval for the next call, capped at MaxInterval.
	next := time.Duration(float64(b.currentInterval) * b.Multiplier)
	if b.MaxInterval > 0 && next > b.MaxInterval {
		next = b.MaxInterval
	}
	b.currentInterval = next

	return interval
}

// permanentError marks an error as non-retryable. Retry unwraps it and returns
// the underlying error to the caller.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent wraps err so that Retry treats it as non-retryable and returns the
// underlying error immediately. A nil input returns nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// Retry runs op with exponential backoff until it returns nil, returns a
// Permanent error, exhausts the backoff's MaxElapsedTime, or ctx is cancelled.
//
// Retry calls bo.Reset before the first attempt so callers can reuse a
// single ExponentialBackOff across independent operations.
func Retry(ctx context.Context, op func() error, bo *ExponentialBackOff) error {
	bo.Reset()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := op()
		if err == nil {
			return nil
		}
		var perm *permanentError
		if errors.As(err, &perm) {
			return perm.err
		}
		wait := bo.NextBackOff()
		if wait == Stop {
			return err
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
