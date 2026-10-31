package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// scriptedTransactor answers each RunInTransaction call with the next scripted
// outcome, invoking the callback first as a real store would.
type scriptedTransactor struct {
	outcomes []error
	calls    int
}

func (s *scriptedTransactor) RunInTransaction(_ context.Context, _ string, fn func(tx Transaction) error) error {
	s.calls++
	if err := fn(nil); err != nil {
		return err
	}
	if len(s.outcomes) == 0 {
		return nil
	}
	out := s.outcomes[0]
	s.outcomes = s.outcomes[1:]
	return out
}

// instantTimer fires immediately and records every wait the policy asked for,
// so the replay loop runs without sleeping.
type instantTimer struct {
	c      chan time.Time
	delays []time.Duration
}

func (t *instantTimer) Start(d time.Duration) {
	t.delays = append(t.delays, d)
	t.c <- time.Now()
}
func (t *instantTimer) Stop()               {}
func (t *instantTimer) C() <-chan time.Time { return t.c }

func useInstantReplayTimer(t *testing.T) *instantTimer {
	t.Helper()
	timer := &instantTimer{c: make(chan time.Time, 1)}
	prev := replayTimer
	replayTimer = func() backoff.Timer { return timer }
	t.Cleanup(func() { replayTimer = prev })
	return timer
}

// conflict is what a backend returns when the server rolled the whole
// transaction back after losing a concurrent-write race.
func conflict(n int) error {
	return MarkTransactionConflict(fmt.Errorf("sql commit (regular): serialization failure #%d", n))
}

func conflicts(n int) []error {
	out := make([]error, n)
	for i := range out {
		out[i] = conflict(i)
	}
	return out
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
		t.Fatalf("marked message = %q, want the unchanged %q", marked.Error(), cause.Error())
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
		t.Fatalf("transactions=%d callbacks=%d, want the WHOLE transaction, callback included, run 3 times", tr.calls, runs)
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
		// A conflict marker never overrides indeterminacy: the write may
		// already have landed, so replaying it could apply it twice.
		"marked but indeterminate": MarkTransactionConflict(fmt.Errorf("lost response: %w", ErrCommitIndeterminate)),
	} {
		t.Run(name, func(t *testing.T) {
			tr := &scriptedTransactor{outcomes: []error{failure, nil}}
			err := RunReplayableTransaction(context.Background(), tr, "bd: test", func(Transaction) error { return nil })
			if !errors.Is(err, failure) {
				t.Fatalf("err = %v, want the original failure", err)
			}
			if tr.calls != 1 {
				t.Fatalf("transactions = %d, want exactly one attempt for a failure that is not a conflict", tr.calls)
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

func TestRunReplayableTransactionBoundsAttempts(t *testing.T) {
	timer := useInstantReplayTimer(t)
	tr := &scriptedTransactor{outcomes: conflicts(replayMaxAttempts + 5)}
	err := RunReplayableTransaction(context.Background(), tr, "bd: test", func(Transaction) error { return nil })
	if err == nil {
		t.Fatal("RunReplayableTransaction succeeded while every attempt lost a conflict")
	}
	if tr.calls != replayMaxAttempts {
		t.Fatalf("transactions = %d, want exactly replayMaxAttempts (%d)", tr.calls, replayMaxAttempts)
	}
	if !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("exhausted error %v must still classify as a transaction conflict", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("serialization failure #%d", replayMaxAttempts-1)) {
		t.Fatalf("exhausted error %q must carry the LAST attempt's failure", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d attempts", replayMaxAttempts)) {
		t.Fatalf("exhausted error %q must say how many attempts were made", err)
	}
	longest := time.Duration(float64(replayMaxInterval) * (1 + replayRandomizationFactor))
	for i, d := range timer.delays {
		if d <= 0 || d > longest {
			t.Fatalf("wait %d = %v, want a positive wait no longer than the jittered cap %v", i, d, longest)
		}
	}
}

func TestRunReplayableTransactionBackoffIsJittered(t *testing.T) {
	// Writers that collide tend to retry together; a fixed schedule would
	// have every loser collide again at the same instant.
	distinct := map[time.Duration]bool{}
	for run := 0; run < 8; run++ {
		timer := useInstantReplayTimer(t)
		tr := &scriptedTransactor{outcomes: []error{conflict(1), nil}}
		if err := RunReplayableTransaction(context.Background(), tr, "bd: test", func(Transaction) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if len(timer.delays) != 1 {
			t.Fatalf("waits = %v, want one", timer.delays)
		}
		distinct[timer.delays[0]] = true
	}
	if len(distinct) < 2 {
		t.Fatalf("first waits were identical across runs (%v); the backoff is not jittered", distinct)
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
// withRetryTx's retries are: each replay and each loop that gives up is
// counted on bd.tx_replays_total, labeled by outcome. A loop that stops on a
// different failure after a replay did not give up on a conflict.
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
	if err := RunReplayableTransaction(context.Background(), &scriptedTransactor{outcomes: []error{conflict(1), conflict(2), nil}}, "bd: replayed", noop); err != nil {
		t.Fatal(err)
	}
	if err := RunReplayableTransaction(context.Background(), &scriptedTransactor{outcomes: conflicts(replayMaxAttempts)}, "bd: exhausted", noop); err == nil {
		t.Fatal("the exhausted loop succeeded")
	}
	if err := RunReplayableTransaction(context.Background(), &scriptedTransactor{outcomes: []error{conflict(1), errors.New("constraint violated")}}, "bd: failed", noop); err == nil {
		t.Fatal("the loop that ended on a plain failure succeeded")
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
	if want := int64(2 + replayMaxAttempts - 1 + 1); got["replayed"] != want {
		t.Fatalf("replayed count = %d, want %d (two, then every replay of the exhausted loop, then one)", got["replayed"], want)
	}
	if got["exhausted"] != 1 {
		t.Fatalf("exhausted count = %d, want 1 (only the loop that ran out of attempts)", got["exhausted"])
	}
}

// TestRunReplayableTransactionKeepsTheLastAttemptsOwnOutcome: once a replay
// has run, the loop's answer is the LAST attempt's outcome. An attempt that
// ends in an indeterminate commit (its COMMIT cut by the context, so the write
// may have landed) or in a plain failure is returned unchanged even when the
// context ended during it — never replaced by the earlier, rolled-back
// conflict, which would tell the caller nothing was written.
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
