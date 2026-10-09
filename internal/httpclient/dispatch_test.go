// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/dispatch_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
)

// busyThenOK answers with a 503 for the first busyCount Do calls and succeeds
// after, recording how many times it was dialed. It is the dispatch retry's
// double: a real server's slot pressure clears the same way.
type busyThenOK struct {
	busyCount  int
	retryAfter time.Duration
	calls      int
}

func (b *busyThenOK) do(_ context.Context, req wire.Request, _ any) error {
	b.calls++
	if b.calls <= b.busyCount {
		return &wire.ProblemError{
			Op:         req.Op,
			Status:     http.StatusServiceUnavailable,
			Err:        wire.ErrBusy,
			RetryAfter: b.retryAfter,
		}
	}
	return nil
}

func retryTestStore(t *testing.T, do func(context.Context, wire.Request, any) error) *Store {
	t.Helper()
	w := &fakeWire{
		res:       &apigen.ContextResponse{},
		preflight: func(context.Context, string) error { return nil },
		do:        do,
	}
	return New(testTarget(t), w, nil)
}

func readRequest() wire.Request {
	return wire.Request{Op: wire.OpListReadyWork, Method: http.MethodGet, Path: wire.PathReady}
}

// TestDispatchRetriesIdempotentReadsOnBusy: a GET that a 503 turned away comes
// back, honors the server's Retry-After, and succeeds once the slot frees.
func TestDispatchRetriesIdempotentReadsOnBusy(t *testing.T) {
	server := &busyThenOK{busyCount: 2, retryAfter: time.Millisecond}
	s := retryTestStore(t, server.do)

	if err := s.dispatch(context.Background(), readRequest(), nil); err != nil {
		t.Fatalf("dispatch: %v, want the retry to succeed", err)
	}
	if server.calls != 3 {
		t.Errorf("Do called %d times, want 3 (one dial plus two retries)", server.calls)
	}
}

// TestDispatchCapsIdempotentRetries: a server that stays busy is not dialed
// forever. The bound is one dial plus maxIdempotentRetries, and the 503 is what
// the caller finally sees.
func TestDispatchCapsIdempotentRetries(t *testing.T) {
	server := &busyThenOK{busyCount: 100, retryAfter: time.Millisecond}
	s := retryTestStore(t, server.do)

	err := s.dispatch(context.Background(), readRequest(), nil)
	if !errors.Is(err, wire.ErrBusy) {
		t.Fatalf("dispatch = %v, want the busy error once the retries are spent", err)
	}
	if server.calls != 1+maxIdempotentRetries {
		t.Errorf("Do called %d times, want %d (one dial plus the capped retries)", server.calls, 1+maxIdempotentRetries)
	}
}

// TestDispatchDoesNotRetryWrites: a non-idempotent write is dialed exactly once
// on a 503, because its first attempt may have committed before the server ran
// out of slots and a resend would double-apply it.
func TestDispatchDoesNotRetryWrites(t *testing.T) {
	server := &busyThenOK{busyCount: 1, retryAfter: time.Millisecond}
	s := retryTestStore(t, server.do)

	write := wire.Request{Op: wire.OpClaimIssue, Method: http.MethodPost, Path: "/v0/beads/issues/bd-1:claim"}
	err := s.dispatch(context.Background(), write, nil)
	if !errors.Is(err, wire.ErrBusy) {
		t.Fatalf("dispatch = %v, want the busy error unretried", err)
	}
	if server.calls != 1 {
		t.Errorf("a write was dialed %d times on a 503, want exactly 1", server.calls)
	}
}

// TestDispatchDoesNotRetryNonBusyReads: a read is retried only for the 503 busy
// class. A 500 fault is the same request earning the same answer, so it is not
// retried.
func TestDispatchDoesNotRetryNonBusyReads(t *testing.T) {
	var calls int
	s := retryTestStore(t, func(_ context.Context, req wire.Request, _ any) error {
		calls++
		return &wire.ProblemError{Op: req.Op, Status: http.StatusInternalServerError, Err: wire.ErrServerFault}
	})

	err := s.dispatch(context.Background(), readRequest(), nil)
	if !errors.Is(err, wire.ErrServerFault) {
		t.Fatalf("dispatch = %v, want the server fault unretried", err)
	}
	if calls != 1 {
		t.Errorf("a 500 was dialed %d times, want exactly 1", calls)
	}
}

// TestDispatchRetryStopsWhenTheContextIsDone: a caller whose deadline expires
// mid-backoff stops retrying and gets the server's busy answer, not a wait that
// outlives the request.
func TestDispatchRetryStopsWhenTheContextIsDone(t *testing.T) {
	server := &busyThenOK{busyCount: 100, retryAfter: time.Hour} // never clears in time
	s := retryTestStore(t, server.do)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := s.dispatch(ctx, readRequest(), nil)
	if !errors.Is(err, wire.ErrBusy) {
		t.Fatalf("dispatch = %v, want the busy error when the deadline cut the wait short", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("dispatch honored the hour-long Retry-After past the caller's deadline (%s)", elapsed)
	}
	if server.calls != 1 {
		t.Errorf("Do called %d times; the deadline should have stopped the first backoff", server.calls)
	}
}
