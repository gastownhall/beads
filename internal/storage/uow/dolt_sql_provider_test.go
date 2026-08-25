package uow

import (
	"context"
	"errors"
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

func testPingBackOff() *backoff.ExponentialBackOff {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = time.Millisecond
	bo.MaxElapsedTime = time.Second
	return bo
}

func TestPingWithRetryRecoversFromATransientBadConn(t *testing.T) {
	t.Run("transient error retries then recovers", func(t *testing.T) {
		p := &fakePinger{t: t, errs: []error{mysql.ErrInvalidConn, mysql.ErrInvalidConn, nil}}

		err := pingWithRetry(context.Background(), p, testPingBackOff())

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

		err := pingWithRetry(context.Background(), p, testPingBackOff())

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

		err := pingWithRetry(ctx, p, testPingBackOff())

		if err == nil {
			t.Fatal("pingWithRetry() error = nil, want non-nil for an already-cancelled context")
		}
		if p.calls != 1 {
			t.Fatalf("PingContext called %d times, want exactly 1 (cancelled context must not retry)", p.calls)
		}
	})
}
