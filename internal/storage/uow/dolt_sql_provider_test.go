package uow

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/go-sql-driver/mysql"
)

// fakePinger is a pinger test double that returns a scripted sequence of
// responses and fails the test if called more times than scripted — that
// over-call case is exactly the retry-budget bug this test suite guards
// against.
type fakePinger struct {
	t     *testing.T
	errs  []error
	calls int
}

func (p *fakePinger) PingContext(context.Context) error {
	p.t.Helper()
	if p.calls >= len(p.errs) {
		p.t.Fatalf("PingContext called more times than expected (call #%d, only %d responses scripted)", p.calls+1, len(p.errs))
	}
	err := p.errs[p.calls]
	p.calls++
	return err
}

// blockingPinger never answers on its own: it stalls until the context it was
// handed is cancelled, then reports that context's error. This is the shape of
// a Dolt server that accepts the TCP connection and then stalls without ever
// completing the MySQL handshake — the DSN sets a dial Timeout but no
// ReadTimeout, so nothing below pingWithRetry bounds it.
type blockingPinger struct {
	calls   atomic.Int32
	release chan struct{}
}

func newBlockingPinger(t *testing.T) *blockingPinger {
	t.Helper()
	p := &blockingPinger{release: make(chan struct{})}
	// Unblock any attempt still parked when the test ends, so a failure here
	// does not leak a goroutine into the rest of the package's tests.
	t.Cleanup(func() { close(p.release) })
	return p
}

func (p *blockingPinger) PingContext(ctx context.Context) error {
	p.calls.Add(1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
		return errors.New("blockingPinger released at test cleanup")
	}
}

func testPingBackOff() *backoff.ExponentialBackOff {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = time.Millisecond
	bo.MaxElapsedTime = time.Second
	return bo
}

// testPingAttemptTimeout is generous: the subtests that use it script their
// ping responses and never actually stall, so the per-attempt cap must not be
// what ends them.
const testPingAttemptTimeout = time.Second

func TestPingWithRetryRecoversFromATransientBadConn(t *testing.T) {
	t.Run("transient error retries then recovers", func(t *testing.T) {
		p := &fakePinger{t: t, errs: []error{mysql.ErrInvalidConn, mysql.ErrInvalidConn, nil}}

		err := pingWithRetry(context.Background(), p, testPingBackOff(), testPingAttemptTimeout)

		if err != nil {
			t.Fatalf("pingWithRetry() error = %v, want nil", err)
		}
		if p.calls != 3 {
			t.Fatalf("PingContext called %d times, want exactly 3", p.calls)
		}
	})

	t.Run("non-transient error fails without retrying", func(t *testing.T) {
		wantErr := errors.New("Access denied")
		p := &fakePinger{t: t, errs: []error{wantErr}}

		err := pingWithRetry(context.Background(), p, testPingBackOff(), testPingAttemptTimeout)

		if !errors.Is(err, wantErr) {
			t.Fatalf("pingWithRetry() error = %v, want %v", err, wantErr)
		}
		if p.calls != 1 {
			t.Fatalf("PingContext called %d times, want exactly 1 (non-transient errors must not retry)", p.calls)
		}
	})

	t.Run("already-cancelled context fails promptly", func(t *testing.T) {
		p := &fakePinger{t: t, errs: []error{mysql.ErrInvalidConn}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := pingWithRetry(ctx, p, testPingBackOff(), testPingAttemptTimeout)

		if err == nil {
			t.Fatal("pingWithRetry() error = nil, want non-nil for an already-cancelled context")
		}
		if p.calls != 1 {
			t.Fatalf("PingContext called %d times, want exactly 1 (cancelled context must not retry)", p.calls)
		}
	})

	t.Run("connection-level net error retries then recovers", func(t *testing.T) {
		// A boot ping cannot fail at the application level before the
		// handshake completes, so any *net.OpError here is connection-level:
		// connection refused (server restarting), reset, or broken pipe.
		refused := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
		p := &fakePinger{t: t, errs: []error{refused, nil}}

		err := pingWithRetry(context.Background(), p, testPingBackOff(), testPingAttemptTimeout)

		if err != nil {
			t.Fatalf("pingWithRetry() error = %v, want nil (a connection-level net error is transient at boot)", err)
		}
		if p.calls != 2 {
			t.Fatalf("PingContext called %d times, want exactly 2", p.calls)
		}
	})

	t.Run("unresolvable host fails without retrying", func(t *testing.T) {
		// The narrowing that pays for classifying *net.OpError broadly: a name
		// that does not resolve will not start resolving, so retrying only
		// spends the budget before reporting the same misconfiguration.
		dnsErr := &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{
			Err: "no such host", Name: "no-such-dolt-host.invalid", IsNotFound: true,
		}}
		p := &fakePinger{t: t, errs: []error{dnsErr}}

		err := pingWithRetry(context.Background(), p, testPingBackOff(), testPingAttemptTimeout)

		if !errors.Is(err, dnsErr) {
			t.Fatalf("pingWithRetry() error = %v, want %v", err, dnsErr)
		}
		if p.calls != 1 {
			t.Fatalf("PingContext called %d times, want exactly 1 (a permanent DNS failure must not retry)", p.calls)
		}
	})
}

// TestPingWithRetryBoundsEachAttempt covers the half of the retry budget that
// MaxElapsedTime cannot reach: backoff.Retry bounds the gap *between*
// attempts, but it cannot interrupt an attempt already in flight.
func TestPingWithRetryBoundsEachAttempt(t *testing.T) {
	t.Run("a hung ping is capped per attempt and retried", func(t *testing.T) {
		p := newBlockingPinger(t)
		bo := backoff.NewExponentialBackOff()
		bo.InitialInterval = time.Millisecond
		bo.MaxElapsedTime = 200 * time.Millisecond

		// Run off-goroutine: before the per-attempt cap exists this call never
		// returns at all, and the assertion we want is "it returned", not a
		// 10-minute package timeout.
		done := make(chan error, 1)
		go func() {
			done <- pingWithRetry(context.Background(), p, bo, 20*time.Millisecond)
		}()

		select {
		case err := <-done:
			if err == nil {
				t.Fatal("pingWithRetry() error = nil, want non-nil once the retry budget is exhausted")
			}
			if got := p.calls.Load(); got < 2 {
				t.Fatalf("PingContext called %d times, want >= 2 (each hung attempt must be capped and retried)", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("pingWithRetry() never returned: a hung ping is not bounded per attempt, so it blocks for the caller's whole context")
		}
	})

	t.Run("the caller's own deadline fails without retrying", func(t *testing.T) {
		// The regression guard for the trap #6000 fell into: once a
		// per-attempt deadline exists, context.DeadlineExceeded becomes
		// retryable — but only when it came from *that* deadline. The
		// caller's own expiry must still stop immediately.
		p := newBlockingPinger(t)
		bo := backoff.NewExponentialBackOff()
		bo.InitialInterval = time.Millisecond
		bo.MaxElapsedTime = 10 * time.Second

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		start := time.Now()
		// Per-attempt cap far beyond the caller's deadline, so the only thing
		// that can end this attempt is the caller's context.
		err := pingWithRetry(ctx, p, bo, time.Hour)
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("pingWithRetry() error = nil, want non-nil once the caller's context expires")
		}
		if got := p.calls.Load(); got != 1 {
			t.Fatalf("PingContext called %d times, want exactly 1 (the caller's own deadline must not be retried as transient)", got)
		}
		if elapsed > 5*time.Second {
			t.Fatalf("pingWithRetry() took %v after a 20ms caller deadline, want it to stop promptly", elapsed)
		}
	})
}
