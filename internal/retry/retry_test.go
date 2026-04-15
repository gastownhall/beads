package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetrySucceedsOnThirdAttempt(t *testing.T) {
	bo := NewExponentialBackOff()
	bo.InitialInterval = 1 * time.Millisecond
	bo.MaxInterval = 2 * time.Millisecond
	bo.MaxElapsedTime = 500 * time.Millisecond

	attempts := 0
	err := Retry(context.Background(), func() error {
		attempts++
		if attempts < 3 {
			return errors.New("transient")
		}
		return nil
	}, bo)

	if err != nil {
		t.Fatalf("Retry returned error: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestRetryPermanentStopsImmediately(t *testing.T) {
	bo := NewExponentialBackOff()
	bo.InitialInterval = 1 * time.Millisecond
	bo.MaxElapsedTime = 500 * time.Millisecond

	fatal := errors.New("fatal")
	attempts := 0
	err := Retry(context.Background(), func() error {
		attempts++
		return Permanent(fatal)
	}, bo)

	if !errors.Is(err, fatal) {
		t.Fatalf("err = %v, want fatal", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (Permanent should stop immediately)", attempts)
	}
}

func TestRetryStopsOnMaxElapsed(t *testing.T) {
	bo := NewExponentialBackOff()
	bo.InitialInterval = 10 * time.Millisecond
	bo.MaxInterval = 10 * time.Millisecond
	bo.MaxElapsedTime = 30 * time.Millisecond
	bo.RandomizationFactor = 0

	transient := errors.New("nope")
	attempts := 0
	start := time.Now()
	err := Retry(context.Background(), func() error {
		attempts++
		return transient
	}, bo)
	elapsed := time.Since(start)

	if !errors.Is(err, transient) {
		t.Fatalf("err = %v, want transient", err)
	}
	if attempts < 2 {
		t.Errorf("attempts = %d, want >= 2", attempts)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v, expected bounded by MaxElapsedTime", elapsed)
	}
}

func TestRetryCancelledContext(t *testing.T) {
	bo := NewExponentialBackOff()
	bo.InitialInterval = 50 * time.Millisecond
	bo.MaxElapsedTime = 0

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	attempts := 0
	err := Retry(ctx, func() error {
		attempts++
		return errors.New("ignored")
	}, bo)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if attempts != 0 {
		t.Errorf("attempts = %d, want 0 (pre-cancelled ctx should skip op)", attempts)
	}
}

func TestExponentialBackOffSatisfiesCenkaltiInterface(t *testing.T) {
	// Compile-time check that *ExponentialBackOff satisfies the same shape
	// as cenkalti/backoff/v4's BackOff interface. Any type with Reset()
	// and NextBackOff() time.Duration satisfies it structurally.
	var bo interface {
		NextBackOff() time.Duration
		Reset()
	} = NewExponentialBackOff()
	bo.Reset()
	if d := bo.NextBackOff(); d <= 0 && d != Stop {
		t.Errorf("NextBackOff returned %v, want positive duration or Stop", d)
	}
}
