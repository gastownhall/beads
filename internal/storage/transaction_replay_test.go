package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// scriptedTransactor answers each RunInTransaction call with the next scripted
// outcome, invoking the callback first exactly as a real store would.
type scriptedTransactor struct {
	outcomes  []error
	calls     int
	callbacks int
	// conflictForever answers every call past the scripted outcomes with a
	// fresh conflict, a writer that never stops losing.
	conflictForever bool
}

func (s *scriptedTransactor) RunInTransaction(_ context.Context, _ string, fn func(tx Transaction) error) error {
	s.calls++
	if err := fn(nil); err != nil {
		return err
	}
	s.callbacks++
	if len(s.outcomes) == 0 {
		if s.conflictForever {
			return conflict(s.calls)
		}
		return nil
	}
	out := s.outcomes[0]
	s.outcomes = s.outcomes[1:]
	return out
}

// instantTimer fires immediately and records every delay the policy asked for,
// so the replay loop is exercised without sleeping in a unit test. Each wait
// also advances the loop's clock by its length, so the elapsed budget is
// measured as if the loop had really slept.
type instantTimer struct {
	c      chan time.Time
	delays []time.Duration
	clock  *steppingClock
}

func newInstantTimer() *instantTimer {
	return &instantTimer{c: make(chan time.Time, 1), clock: &steppingClock{now: time.Unix(0, 0)}}
}

func (t *instantTimer) Start(d time.Duration) {
	t.delays = append(t.delays, d)
	t.clock.advance(d)
	t.c <- time.Now()
}
func (t *instantTimer) Stop()               {}
func (t *instantTimer) C() <-chan time.Time { return t.c }

// steppingClock is a backoff.Clock that moves only when a wait advances it.
type steppingClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *steppingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *steppingClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func useInstantReplayTimer(t *testing.T) *instantTimer {
	t.Helper()
	timer := newInstantTimer()
	prevTimer, prevClock := replayTimer, replayClock
	replayTimer = func() replayBackoffTimer { return timer }
	replayClock = timer.clock
	t.Cleanup(func() { replayTimer, replayClock = prevTimer, prevClock })
	return timer
}

// conflict is what a backend returns when the server rolled the whole
// transaction back after losing a concurrent-write race.
func conflict(n int) error {
	return MarkTransactionConflict(fmt.Errorf("sql commit (regular): serialization failure #%d", n))
}

func TestMarkTransactionConflictKeepsMessageAndCause(t *testing.T) {
	cause := errors.New("sql commit (regular): Error 1213 (40001): serialization failure")
	marked := MarkTransactionConflict(cause)
	if !errors.Is(marked, ErrTransactionConflict) {
		t.Fatalf("marked error %v does not match ErrTransactionConflict", marked)
	}
	if !errors.Is(marked, cause) {
		t.Fatalf("marked error %v lost its cause", marked)
	}
	if marked.Error() != cause.Error() {
		t.Fatalf("marked message = %q, want the unchanged cause %q", marked.Error(), cause.Error())
	}
	if MarkTransactionConflict(nil) != nil {
		t.Fatal("MarkTransactionConflict(nil) must stay nil")
	}
	if again := MarkTransactionConflict(marked); again != marked {
		t.Fatal("marking an already-marked error must not wrap it twice")
	}
}

func TestRunReplayableTransactionReplaysWholeTransactionOnConflict(t *testing.T) {
	timer := useInstantReplayTimer(t)
	tr := &scriptedTransactor{outcomes: []error{conflict(1), conflict(2), nil}}
	runs := 0
	err := RunReplayableTransaction(context.Background(), tr, "bd: test", func(Transaction) error {
		runs++
		return nil
	})
	if err != nil {
		t.Fatalf("RunReplayableTransaction = %v, want success after two lost conflicts", err)
	}
	if tr.calls != 3 || runs != 3 {
		t.Fatalf("transactions=%d callbacks=%d, want the WHOLE transaction (callback included) run 3 times", tr.calls, runs)
	}
	if len(timer.delays) != 2 {
		t.Fatalf("backoff waits = %v, want one wait before each replay", timer.delays)
	}
}

func TestRunReplayableTransactionDoesNotReplayOtherFailures(t *testing.T) {
	useInstantReplayTimer(t)
	for name, failure := range map[string]error{
		"plain error":          errors.New("dependency would create a cycle"),
		"indeterminate commit": fmt.Errorf("stage and commit after regular SQL commit: %w", ErrCommitIndeterminate),
		// A conflict marker must never override indeterminacy: the write may
		// already have landed, so replaying it could double-apply.
		"marked but indeterminate": MarkTransactionConflict(fmt.Errorf("lost response: %w", ErrCommitIndeterminate)),
	} {
		t.Run(name, func(t *testing.T) {
			tr := &scriptedTransactor{outcomes: []error{failure, nil}}
			err := RunReplayableTransaction(context.Background(), tr, "bd: test", func(Transaction) error { return nil })
			if !errors.Is(err, failure) {
				t.Fatalf("err = %v, want the original failure", err)
			}
			if tr.calls != 1 {
				t.Fatalf("transactions = %d, want exactly one attempt for a non-conflict failure", tr.calls)
			}
		})
	}
}

func TestRunReplayableTransactionCallbackErrorIsNotReplayed(t *testing.T) {
	useInstantReplayTimer(t)
	tr := &scriptedTransactor{}
	want := errors.New("issue not found")
	runs := 0
	err := RunReplayableTransaction(context.Background(), tr, "bd: test", func(Transaction) error {
		runs++
		return want
	})
	if !errors.Is(err, want) || runs != 1 {
		t.Fatalf("err=%v runs=%d, want the callback's own error after one run", err, runs)
	}
}

// TestRunReplayableTransactionStopsAtTheSharedBudget: a writer that never
// stops losing is replayed until the conflict-replay budget the proxied routes
// share (TxConflictReplayMaxElapsed) runs out, with each wait capped near
// replayMaxInterval so attempts keep arriving through the whole budget.
func TestRunReplayableTransactionStopsAtTheSharedBudget(t *testing.T) {
	for run := 0; run < 20; run++ {
		timer := useInstantReplayTimer(t)
		tr := &scriptedTransactor{conflictForever: true}
		err := RunReplayableTransaction(context.Background(), tr, "bd: test", func(Transaction) error { return nil })
		if err == nil {
			t.Fatal("RunReplayableTransaction succeeded while every attempt lost a conflict")
		}
		if !errors.Is(err, ErrTransactionConflict) {
			t.Fatalf("exhausted error %v must still classify as a transaction conflict", err)
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("serialization failure #%d", tr.calls)) {
			t.Fatalf("exhausted error %q must carry the LAST attempt's failure (#%d)", err, tr.calls)
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("%d attempts", tr.calls)) {
			t.Fatalf("exhausted error %q must say how many attempts were made (%d)", err, tr.calls)
		}
		var waited time.Duration
		for i, d := range timer.delays {
			if d <= 0 || d > replayMaxInterval*3/2 {
				t.Fatalf("delay %d = %v, want a positive jittered delay capped near replayMaxInterval (%v)", i, d, replayMaxInterval)
			}
			waited += d
		}
		if waited > TxConflictReplayMaxElapsed {
			t.Fatalf("the loop waited %v, past the shared %v budget", waited, TxConflictReplayMaxElapsed)
		}
		// Even with every wait jittered to its longest, the capped schedule
		// makes at least 17 attempts in 15s (uncapped it makes about 14).
		if tr.calls < 17 {
			t.Fatalf("a writer that keeps losing got %d attempts in %v, want at least 17", tr.calls, TxConflictReplayMaxElapsed)
		}
	}
}

func TestRunReplayableTransactionBackoffIsJittered(t *testing.T) {
	// Writers that collide tend to retry in lockstep; the waits must not be a
	// fixed schedule or every loser re-collides at the same instant.
	distinct := map[time.Duration]bool{}
	for run := 0; run < 8; run++ {
		timer := useInstantReplayTimer(t)
		tr := &scriptedTransactor{outcomes: []error{conflict(1), nil}}
		if err := RunReplayableTransaction(context.Background(), tr, "bd: test", func(Transaction) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if len(timer.delays) != 1 {
			t.Fatalf("delays = %v, want one", timer.delays)
		}
		distinct[timer.delays[0]] = true
	}
	if len(distinct) < 2 {
		t.Fatalf("first-retry delays were identical across runs (%v); the backoff is not jittered", distinct)
	}
}

func TestRunReplayableTransactionStopsWhenContextEnds(t *testing.T) {
	useInstantReplayTimer(t)
	ctx, cancel := context.WithCancel(context.Background())
	tr := &scriptedTransactor{outcomes: []error{conflict(1), conflict(2), nil}}
	runs := 0
	err := RunReplayableTransaction(ctx, tr, "bd: test", func(Transaction) error {
		runs++
		cancel()
		return nil
	})
	if err == nil {
		t.Fatal("RunReplayableTransaction kept going after its context was canceled")
	}
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("err = %v, want both the cancellation and the conflict that was being retried", err)
	}
	if runs != 1 {
		t.Fatalf("callback runs = %d, want 1 (no replay after cancellation)", runs)
	}
}

// TestRunReplayableTransactionCountsReplays makes replays observable the way
// withRetryTx's are: every replay is counted on bd.tx_replays_total with
// outcome=replayed, and outcome=exhausted counts only a loop whose budget ran
// out on conflicts — not one that stopped on another error or because its
// context ended.
func TestRunReplayableTransactionCountsReplays(t *testing.T) {
	useInstantReplayTimer(t)
	// The counter is swapped for one built by the same constructor on a
	// test-local provider, so the global meter provider is never touched.
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := txReplays
	txReplays = newTxReplaysCounter(provider.Meter("test"))
	t.Cleanup(func() {
		txReplays = prev
		_ = provider.Shutdown(context.Background())
	})
	noop := func(Transaction) error { return nil }

	// Two replays, then success.
	if err := RunReplayableTransaction(context.Background(), &scriptedTransactor{outcomes: []error{conflict(1), conflict(2), nil}}, "bd: counted", noop); err != nil {
		t.Fatal(err)
	}
	// Every attempt loses until the budget runs out: exhausted.
	exhausted := &scriptedTransactor{conflictForever: true}
	if err := RunReplayableTransaction(context.Background(), exhausted, "bd: exhausted", noop); err == nil {
		t.Fatal("the exhausted loop succeeded")
	}
	// One replay, then a failure that is not a conflict: not exhausted.
	permanent := errors.New("dependency would create a cycle")
	if err := RunReplayableTransaction(context.Background(), &scriptedTransactor{outcomes: []error{conflict(1), permanent}}, "bd: permanent", noop); !errors.Is(err, permanent) {
		t.Fatalf("err = %v, want the non-conflict failure", err)
	}
	// The context ends while the loop waits to replay: not exhausted.
	ctx, cancel := context.WithCancel(context.Background())
	if err := RunReplayableTransaction(ctx, &scriptedTransactor{outcomes: []error{conflict(1), nil}}, "bd: canceled", func(Transaction) error {
		cancel()
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "bd.tx_replays_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("bd.tx_replays_total is %T, want an int64 sum", m.Data)
			}
			for _, dp := range sum.DataPoints {
				outcome, _ := dp.Attributes.Value(attribute.Key("outcome"))
				got[outcome.AsString()] += dp.Value
			}
		}
	}
	// 2 + (attempts-1) + 1; the canceled loop never reached its replay.
	if want := int64(2 + exhausted.calls - 1 + 1); got["replayed"] != want {
		t.Fatalf("replayed count = %d, want %d", got["replayed"], want)
	}
	if got["exhausted"] != 1 {
		t.Fatalf("exhausted count = %d, want 1 (only the loop whose budget ran out)", got["exhausted"])
	}
}

// TestRunReplayableTransactionKeepsTheLastAttemptsOwnOutcome: once a replay
// has run, the loop's answer is the LAST attempt's outcome. An attempt that
// ends in an indeterminate commit (its COMMIT cut by the context, so the write
// may have landed) or in a plain failure is returned unchanged even when the
// context ended during it — never replaced by the earlier, rolled-back
// conflict, which would tell the caller nothing was written. Contributed in
// review.
func TestRunReplayableTransactionKeepsTheLastAttemptsOwnOutcome(t *testing.T) {
	for name, last := range map[string]error{
		"indeterminate commit": fmt.Errorf("sql commit (regular): %w: %w", context.Canceled, ErrCommitIndeterminate),
		"plain failure":        errors.New("validation: dependency would create a cycle"),
	} {
		t.Run(name, func(t *testing.T) {
			useInstantReplayTimer(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tr := &scriptedTransactor{outcomes: []error{conflict(1), last}}
			calls := 0
			err := RunReplayableTransaction(ctx, tr, "bd: last outcome", func(Transaction) error {
				calls++
				if calls == 2 {
					cancel() // the context ends during the replayed attempt
				}
				return nil
			})
			if !errors.Is(err, last) {
				t.Fatalf("err = %v, want the last attempt's own outcome %v", err, last)
			}
			if errors.Is(last, ErrCommitIndeterminate) && errors.Is(err, ErrTransactionConflict) {
				t.Fatalf("an indeterminate outcome was reported as a rolled-back conflict: %v", err)
			}
		})
	}
}
