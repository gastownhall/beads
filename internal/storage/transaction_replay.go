package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cenkalti/backoff/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/steveyegge/beads/internal/debug"
)

// ErrTransactionConflict marks a transaction the database rolled back because
// it lost a write conflict with a concurrent transaction: a serialization
// failure or deadlock (MySQL 1213, what Dolt's commit-time merge raises), or a
// lock-wait timeout (1205). The transaction is rolled back whole, so nothing it
// wrote is durable, and the server's own remedy is to restart the WHOLE
// transaction. Backends attach it with MarkTransactionConflict; it never
// accompanies ErrCommitIndeterminate.
var ErrTransactionConflict = errors.New("transaction lost a write conflict with a concurrent transaction and was rolled back")

// transactionConflictError tags a backend error as ErrTransactionConflict
// without changing the message the user sees.
type transactionConflictError struct{ err error }

func (e *transactionConflictError) Error() string { return e.err.Error() }

func (e *transactionConflictError) Unwrap() []error {
	return []error{e.err, ErrTransactionConflict}
}

// MarkTransactionConflict returns err tagged so that
// errors.Is(err, ErrTransactionConflict) holds, keeping its message and its
// cause chain intact. Only a backend that knows the failure guarantees a
// rollback of the whole transaction may call it.
func MarkTransactionConflict(err error) error {
	if err == nil || errors.Is(err, ErrTransactionConflict) {
		return err
	}
	return &transactionConflictError{err: err}
}

// Transactor is the one store capability RunReplayableTransaction needs.
type Transactor interface {
	RunInTransaction(ctx context.Context, commitMsg string, fn func(tx Transaction) error) error
}

// The conflict-replay budget. The TOTAL time a loop keeps replaying a
// transaction that lost a write conflict is shared: the proxied routes'
// unit-of-work loop (uow.RunTx*, whose DefaultTxRetryMaxElapsed is
// TxConflictReplayMaxElapsed), RunReplayableTransaction on the direct routes,
// and withRetryTx on a Dolt sql-server all stop after it, so a command waits
// the same total time for its conflicts to clear whichever route reached the
// database. The schedule inside the budget is not shared: RunReplayableTransaction
// and withRetryTx cap each wait at one second, while uow.RunTx leaves its
// exponential waits uncapped. A backend that replays inside its own
// RunInTransaction keeps its own loop and never marks ErrTransactionConflict,
// so RunReplayableTransaction runs it once.
const (
	// TxConflictReplayMaxElapsed bounds how long a loop keeps replaying a
	// transaction that keeps losing write conflicts before it gives up.
	TxConflictReplayMaxElapsed = 15 * time.Second
	// TxConflictReplayInitialInterval is the first wait before a replay; the
	// waits then grow exponentially.
	TxConflictReplayInitialInterval = 25 * time.Millisecond
)

// Within that budget RunReplayableTransaction caps each wait at
// replayMaxInterval so the later attempts keep arriving at a useful rate, and
// randomizes each wait by half its length in either direction: writers that
// collide tend to start within microseconds of each other, and a fixed
// schedule would have every loser re-collide in lockstep. There is no attempt
// cap: each attempt is a complete transaction, and the elapsed budget is what
// bounds the work, as in uow.RunTx.
const (
	replayMaxInterval         = time.Second
	replayRandomizationFactor = 0.5
)

// replayBackoffTimer is the backoff library's timer, named here so tests can
// substitute one that fires immediately.
type replayBackoffTimer = backoff.Timer

// replayTimer supplies the timer for one replay loop; nil uses the library's
// real timer.
var replayTimer func() replayBackoffTimer

// replayClock is the clock the replay loop measures its elapsed budget on; nil
// uses the system clock. Tests substitute one that advances with each wait.
var replayClock backoff.Clock

// txReplays counts whole-transaction replays, labeled outcome=replayed for
// each replay and outcome=exhausted when a loop runs out of attempts on
// conflicts (its budget ran out) — the RunReplayableTransaction counterpart of withRetryTx's
// write-retry counter. It is registered against the global delegating
// provider, so it forwards to the real provider once telemetry is initialized.
var txReplays = newTxReplaysCounter(otel.Meter("github.com/steveyegge/beads/storage"))

func newTxReplaysCounter(meter metric.Meter) metric.Int64Counter {
	counter, _ := meter.Int64Counter("bd.tx_replays_total",
		metric.WithDescription("Whole-transaction replays after a lost write conflict (label: outcome=replayed|exhausted)"),
		metric.WithUnit("{replay}"),
	)
	return counter
}

// RunReplayableTransaction runs fn through s.RunInTransaction and, while an
// attempt fails with ErrTransactionConflict, waits a jittered backoff and runs
// the WHOLE transaction again — a fresh BEGIN, the callback from the top, and
// a fresh COMMIT. A store's RunInTransaction invokes its callback at most once
// and leaves the decision to replay to the caller, because only the caller
// knows whether its callback is safe to repeat.
//
// Use it only for a callback that touches the database exclusively through tx,
// re-derives every value it hands back on each invocation (plain assignment or
// an explicit reset at its top, never an append into captured state), and has
// no other side effects. A callback error that wraps a rollback-guaranteed
// conflict (a statement inside the transaction that lost with 1213 or 1205) is
// marked by the backend and replayed like a conflict at commit, by design: the
// failed callback rolls the whole transaction back. Any other failure — a
// callback error of its own, a non-conflict database error, or an
// ErrCommitIndeterminate outcome that may already have landed — is returned
// after the single attempt that produced it.
//
// A create through tx writes what it mints (the ID, defaulted timestamps,
// inline edge sources and comment IDs) back into the caller's issues, so a
// callback that creates issues built outside it must restore them at its top
// (issueops.SnapshotCreateInputs, taken before this call). Otherwise a replay
// takes the rolled-back attempt's ID as one the caller named, and in counter
// mode upserts over the issue the winning writer committed under that number.
//
// The loop is bounded by TxConflictReplayMaxElapsed, the total budget the
// proxied routes' uow.RunTx shares. When it gives up it returns the last
// conflict, which still matches ErrTransactionConflict, annotated with the
// number of attempts made. A replay is counted when it starts, so a loop whose
// context ends while it waits counts none.
func RunReplayableTransaction(ctx context.Context, s Transactor, commitMsg string, fn func(tx Transaction) error) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = TxConflictReplayInitialInterval
	bo.MaxInterval = replayMaxInterval
	bo.RandomizationFactor = replayRandomizationFactor
	bo.MaxElapsedTime = TxConflictReplayMaxElapsed
	if replayClock != nil {
		bo.Clock = replayClock
	}
	policy := backoff.WithContext(bo, ctx)

	attempts := 0
	var lastConflict, lastAttempt error
	notify := func(err error, wait time.Duration) {
		debug.Logf("transaction %q lost a write conflict on attempt %d; replaying in %v: %v\n", commitMsg, attempts, wait, err)
	}
	err := backoff.RetryNotifyWithTimer(func() error {
		if attempts > 0 {
			// The backoff library may still fire the timer after the
			// context ended during the wait; such a loop stops here, before
			// starting (or counting) a replay.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return backoff.Permanent(ctxErr)
			}
			txReplays.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "replayed")))
		}
		attempts++
		err := s.RunInTransaction(ctx, commitMsg, fn)
		lastAttempt = err
		if err == nil {
			return nil
		}
		if replayableConflict(err) {
			lastConflict = err
			return err
		}
		return backoff.Permanent(err)
	}, policy, notify, newReplayTimer())
	if err == nil {
		return nil
	}
	if lastConflict == nil || !replayableConflict(lastAttempt) {
		// The last attempt's own outcome is the answer, unchanged: a callback
		// error, a non-conflict database error, or an indeterminate commit
		// that may have landed — even when the context ended during it.
		// Replacing it with an earlier, rolled-back conflict would tell the
		// caller nothing was written.
		return err
	}
	if errors.Is(err, lastConflict) {
		// The loop ended on the conflict it was replaying: its budget ran out.
		txReplays.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "exhausted")))
		return fmt.Errorf("%w (gave up after %d attempts: the transaction kept losing write conflicts with concurrent writers)", err, attempts)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		// The context ended while waiting to replay: report both why the loop
		// stopped and the conflict it was retrying.
		return fmt.Errorf("%w (stopped retrying after %d attempt(s): %w)", lastConflict, attempts, ctxErr)
	}
	return err
}

// replayableConflict reports whether err proves the attempt rolled back
// because of a concurrent-write conflict. Indeterminacy always wins: a write
// whose commit response was lost may have landed, so it is never replayed even
// if something also marked it as a conflict.
func replayableConflict(err error) bool {
	return errors.Is(err, ErrTransactionConflict) && !errors.Is(err, ErrCommitIndeterminate)
}

func newReplayTimer() backoff.Timer {
	if replayTimer == nil {
		return nil
	}
	return replayTimer()
}
