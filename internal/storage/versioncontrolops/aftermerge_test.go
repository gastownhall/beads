package versioncontrolops

import (
	"context"
	"testing"
	"time"
)

type graceValueKey struct{}

// waitDone returns how long ctx took to end, measured from start, and fails
// the test if it does not end within a bound far above any grace used here.
func waitDone(t *testing.T, ctx context.Context, start time.Time) time.Duration {
	t.Helper()
	select {
	case <-ctx.Done():
		return time.Since(start)
	case <-time.After(10 * time.Second):
		t.Fatal("context did not end within 10s")
		return 0
	}
}

// assertLive fails the test if ctx ends within d.
func assertLive(t *testing.T, ctx context.Context, d time.Duration) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatalf("context ended: %v", ctx.Err())
	case <-time.After(d):
	}
}

// TestWithCancelGrace pins the context the work after a merge runs on
// (GH#7989): it keeps the caller's values, adds no bound while the caller is
// live, and ends a grace after the caller ends, by cancellation or deadline.
func TestWithCancelGrace(t *testing.T) {
	const grace = 50 * time.Millisecond

	t.Run("live caller", func(t *testing.T) {
		parent, cancelParent := context.WithTimeout(context.WithValue(context.Background(), graceValueKey{}, "kept"), time.Hour)
		defer cancelParent()
		ctx, cancel := WithCancelGrace(parent, grace)
		defer cancel()
		if got := ctx.Value(graceValueKey{}); got != "kept" {
			t.Errorf("value = %v, want the caller's", got)
		}
		if d, ok := ctx.Deadline(); ok {
			t.Errorf("deadline = %v, want none while the caller is live", d)
		}
		assertLive(t, ctx, 4*grace)
		cancel()
		if ctx.Err() == nil {
			t.Error("cancel did not end the context")
		}
	})

	t.Run("canceled caller", func(t *testing.T) {
		parent, cancelParent := context.WithCancel(context.Background())
		ctx, cancel := WithCancelGrace(parent, time.Hour)
		defer cancel()
		cancelParent()
		assertLive(t, ctx, 100*time.Millisecond)

		parent, cancelParent = context.WithCancel(context.Background())
		ctx, cancel = WithCancelGrace(parent, grace)
		defer cancel()
		start := time.Now()
		cancelParent()
		if took := waitDone(t, ctx, start); took < grace {
			t.Errorf("ended %s after the caller's cancellation, before the %s grace", took, grace)
		}
	})

	t.Run("caller deadline", func(t *testing.T) {
		parent, cancelParent := context.WithTimeout(context.Background(), grace)
		defer cancelParent()
		ctx, cancel := WithCancelGrace(parent, time.Hour)
		defer cancel()
		<-parent.Done()
		assertLive(t, ctx, 100*time.Millisecond)

		start := time.Now()
		parent, cancelParent = context.WithTimeout(context.Background(), grace)
		defer cancelParent()
		ctx, cancel = WithCancelGrace(parent, grace)
		defer cancel()
		if took := waitDone(t, ctx, start); took < 2*grace {
			t.Errorf("ended %s after it was made, before the caller's %s deadline plus the %s grace", took, grace, grace)
		}
	})

	t.Run("caller already ended", func(t *testing.T) {
		parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), graceValueKey{}, "kept"))
		cancelParent()
		start := time.Now()
		ctx, cancel := WithCancelGrace(parent, grace)
		defer cancel()
		if got := ctx.Value(graceValueKey{}); got != "kept" {
			t.Errorf("value = %v, want the caller's", got)
		}
		if d, ok := ctx.Deadline(); !ok || d.Before(start.Add(grace)) {
			t.Errorf("deadline = %v (set %v), want the grace from now", d, ok)
		}
		if took := waitDone(t, ctx, start); took < grace {
			t.Errorf("ended after %s, before the %s grace", took, grace)
		}

		ctx, cancel = WithCancelGrace(parent, 0)
		defer cancel()
		if ctx.Err() == nil {
			t.Error("no grace left a context that had already ended live")
		}
	})
}
