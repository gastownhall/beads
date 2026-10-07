package uow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestRetryTxBudgetScalesToRemainingDeadline pins the fix for a large apply's
// retry budget: RunTxResult's hardcoded 15s ceiling must not silently disable
// retries for the bulk of a large apply's own (much longer) context deadline
// — but ONLY on the path that explicitly asks for that (marked via
// publicops.WithExtendedRetryBudget). An ordinary request's context also
// carries a real deadline (60s); an earlier version of this fix scaled the
// budget to ANY sufficiently long remaining deadline and silently raised
// ordinary requests from 15s to up to 60s of retrying. That regression is
// exactly what the unmarked subtests below pin against.
func TestRetryTxBudgetScalesToRemainingDeadline(t *testing.T) {
	t.Run("no deadline, no marker gets the default", func(t *testing.T) {
		got := retryTxBudget(context.Background())
		if got != DefaultTxRetryMaxElapsed {
			t.Errorf("retryTxBudget(no deadline) = %s, want %s", got, DefaultTxRetryMaxElapsed)
		}
	})

	t.Run("a deadline shorter than the default never shrinks the budget when unmarked", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		got := retryTxBudget(ctx)
		if got != DefaultTxRetryMaxElapsed {
			t.Errorf("retryTxBudget(1s deadline) = %s, want %s (never below the default when unmarked)", got, DefaultTxRetryMaxElapsed)
		}
	})

	t.Run("an UNMARKED long deadline (an ordinary request's own 60s) stays at exactly the default", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		got := retryTxBudget(ctx)
		if got != DefaultTxRetryMaxElapsed {
			t.Errorf("retryTxBudget(60s deadline, unmarked) = %s, want exactly %s (an ordinary request must not retry longer just because its ctx happens to have a deadline)", got, DefaultTxRetryMaxElapsed)
		}
	})

	t.Run("a MARKED deadline far past the default extends the budget to match it, minus attempt headroom", func(t *testing.T) {
		const ceiling = 5 * time.Minute
		ctx, cancel := context.WithTimeout(context.Background(), ceiling)
		defer cancel()
		ctx = publicops.WithExtendedRetryBudget(ctx)
		got := retryTxBudget(ctx)
		want := ceiling - retryTxAttemptHeadroom
		const slack = 2 * time.Second
		if got < want-slack || got > want {
			t.Errorf("retryTxBudget(%s deadline, marked) = %s, want ~%s (extended to match a large apply's own run budget, minus headroom for the final attempt)", ceiling, got, want)
		}
	})

	t.Run("a MARKED deadline too close to expiry to leave attempt headroom floors at retryTxMinBudget, not the 15s default", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ctx = publicops.WithExtendedRetryBudget(ctx)
		got := retryTxBudget(ctx)
		if got != retryTxMinBudget {
			t.Errorf("retryTxBudget(3s deadline, marked) = %s, want %s (too little headroom to extend; claiming the full 15s default here would be misleading since ctx cancellation ends the loop first regardless)", got, retryTxMinBudget)
		}
	})
}

// alwaysSerializationErrorProvider fails EVERY NewUOW call with a retryable
// serialization error, so a caller through RunTxResultWithin never reaches
// the work function at all. That is what makes it cheap: proving ApplyBatch
// itself (not just the retryTxBudget helper) actually wires retryTxBudget's
// return value into RunTxResultWithin needs no real domain use cases —
// issueops.BatchApplier's huge UnitOfWork surface is never touched, because
// NewUOW itself is the thing failing.
type alwaysSerializationErrorProvider struct {
	calls atomic.Int32
}

func (p *alwaysSerializationErrorProvider) NewUOW(context.Context) (UnitOfWork, error) {
	p.calls.Add(1)
	return nil, newMySQLError(1213) // deadlock: uow.isSerializationError's retryable case
}

func (p *alwaysSerializationErrorProvider) Close(context.Context) error { return nil }

// minimalValidApplyBatchRequest is the smallest request storage.PlanApplyBatch
// accepts: one actor, one create item. It is never actually applied in the
// tests below — alwaysSerializationErrorProvider fails before uowApplyRun.apply
// ever runs — so its content only has to pass static plan validation.
func minimalValidApplyBatchRequest() publicops.ApplyBatchRequest {
	return publicops.ApplyBatchRequest{
		Actor: "tester",
		Items: []publicops.ApplyItem{
			{
				Kind: publicops.ItemCreate,
				Create: &publicops.CreateItem{
					Issue: &types.Issue{
						Title:     "retry budget wiring probe",
						Status:    types.StatusOpen,
						Priority:  2,
						IssueType: types.TypeTask,
					},
				},
			},
		},
	}
}

// TestApplyBatchHonorsAnExtendedRetryBudgetFromContext tests ApplyBatch
// ITSELF, not just the retryTxBudget helper — the gap the coordinator's
// review flagged: a mutation that hardcodes ApplyBatch's call to
// RunTxResultWithin(ctx, o.provider, DefaultTxRetryMaxElapsed, ...) instead of
// retryTxBudget(ctx) passed every existing test, because nothing exercised
// ApplyBatch with a marked context and actually observed which budget
// governed the retry loop.
//
// Both subtests share the SAME 2-second outer ctx deadline, so a mutation
// that ignores the marker makes them behave IDENTICALLY:
//
//   - unmarked: retryTxBudget must return the full 15s default. Since 15s
//     vastly exceeds the 2s ctx deadline, ctx's OWN cancellation ends the
//     loop first, so the observed error is ctx's DeadlineExceeded, never a
//     serialization error, and it takes close to the full 2s.
//   - marked: retryTxBudget must return retryTxMinBudget (1s, per the
//     "too close to expiry to extend" case above, since 2s of remaining
//     deadline is less than retryTxAttemptHeadroom). That 1s budget is
//     SHORTER than the 2s ctx deadline, so the retry loop exhausts on its
//     own well under 2s, and the observed error is the serialization error
//     itself.
//
// If ApplyBatch ignored retryTxBudget's return value and always used the 15s
// default, the marked case would look exactly like the unmarked one: it
// would run nearly the full 2s and end on ctx's DeadlineExceeded instead of
// exhausting fast on the serialization error, and this test would fail.
func TestApplyBatchHonorsAnExtendedRetryBudgetFromContext(t *testing.T) {
	const outerDeadline = 2 * time.Second

	t.Run("unmarked: the 15s default outlasts the 2s ctx, so ctx itself ends the loop", func(t *testing.T) {
		provider := &alwaysSerializationErrorProvider{}
		applier, err := NewBatchApplier(provider)
		if err != nil {
			t.Fatalf("NewBatchApplier: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), outerDeadline)
		defer cancel()

		start := time.Now()
		_, err = applier.ApplyBatch(ctx, minimalValidApplyBatchRequest())
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("expected an error: NewUOW never succeeds")
		}
		if IsSerializationError(err) {
			t.Errorf("err = %v is a serialization error; want ctx's own DeadlineExceeded, because the 15s default budget should still be governing and outlast this 2s ctx", err)
		}
		if elapsed < 1500*time.Millisecond {
			t.Errorf("elapsed = %s, want close to %s: the loop ended too early for the 15s default to have been the governing budget", elapsed, outerDeadline)
		}
		if provider.calls.Load() < 2 {
			t.Errorf("NewUOW calls = %d, want several retries before ctx ended the loop", provider.calls.Load())
		}
	})

	t.Run("marked: the too-short-to-extend 1s floor governs and exhausts well under the 2s ctx", func(t *testing.T) {
		provider := &alwaysSerializationErrorProvider{}
		applier, err := NewBatchApplier(provider)
		if err != nil {
			t.Fatalf("NewBatchApplier: %v", err)
		}
		ctx, cancel := context.WithTimeout(publicops.WithExtendedRetryBudget(context.Background()), outerDeadline)
		defer cancel()

		start := time.Now()
		_, err = applier.ApplyBatch(ctx, minimalValidApplyBatchRequest())
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("expected an error: NewUOW never succeeds")
		}
		if !IsSerializationError(err) {
			t.Errorf("err = %v, want the last serialization failure itself: an exhausted retry budget, not ctx cancellation", err)
		}
		if elapsed > 1500*time.Millisecond {
			t.Errorf("elapsed = %s, want well under %s: the marked path's ~1s floor should have governed, not the 2s outer ctx", elapsed, outerDeadline)
		}
	})
}
