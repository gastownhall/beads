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

// ErrTransactionConflict marks a transaction the database rolled back whole
// because it lost a write conflict with a concurrent transaction: a
// serialization failure or deadlock (MySQL 1213, what Dolt's commit-time merge
// raises) or a lock-wait timeout (1205). Nothing the transaction wrote is
// durable, and the server's own remedy is to run the WHOLE transaction again.
// Backends attach it with MarkTransactionConflict; it never accompanies
// ErrCommitIndeterminate.
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
// cause chain intact. Only a backend that knows the failure rolled the whole
// transaction back may call it.
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

// The replay policy. Each attempt is a complete transaction, so the attempt
// bound is what caps the work; the elapsed bound only stops a run whose
// attempts are themselves slow. The waits grow exponentially, are capped so
// the later attempts keep arriving, and are randomized by half their length in
// either direction: writers that collide tend to start together, and a fixed
// schedule would have every loser collide again in lockstep.
const (
	replayMaxAttempts         = 20
	replayInitialInterval     = 25 * time.Millisecond
	replayMaxInterval         = time.Second
	replayRandomizationFactor = 0.5
	replayMaxElapsed          = 30 * time.Second
)

// replayTimer supplies the timer for one replay loop; nil uses the backoff
// library's real timer. Tests substitute one that fires immediately.
var replayTimer func() backoff.Timer

// txReplays counts whole-transaction replays, labeled outcome=replayed for each
// replay and outcome=exhausted when a loop runs out of attempts on conflicts:
// the RunReplayableTransaction counterpart of withRetryTx's write-retry
// counter. It is registered against the global delegating provider, so it
// forwards to the real provider once telemetry is initialized.
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
// the WHOLE transaction again: a fresh BEGIN, the callback from the top, and a
// fresh COMMIT. RunInTransaction invokes its callback at most once and leaves
// the decision to replay to the caller, because only the caller knows whether
// its callback is safe to repeat.
//
// Use it only for a callback that touches the database exclusively through tx,
// re-derives every value it hands back on each invocation (plain assignment or
// an explicit reset at its top, never an append into captured state), and has
// no other side effects. Any other failure — a callback error, a non-conflict
// database error, or an ErrCommitIndeterminate outcome that may already have
// landed — is returned after the one attempt that produced it.
//
// The loop makes at most replayMaxAttempts attempts within replayMaxElapsed.
// When it gives up it returns the last conflict, which still matches
// ErrTransactionConflict, annotated with the number of attempts made.
func RunReplayableTransaction(ctx context.Context, s Transactor, commitMsg string, fn func(tx Transaction) error) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = replayInitialInterval
	bo.MaxInterval = replayMaxInterval
	bo.RandomizationFactor = replayRandomizationFactor
	bo.MaxElapsedTime = replayMaxElapsed
	policy := backoff.WithContext(backoff.WithMaxRetries(bo, replayMaxAttempts-1), ctx)

	attempts := 0
	var lastConflict, lastAttempt error
	notify := func(err error, wait time.Duration) {
		txReplays.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "replayed")))
		debug.Logf("transaction %q lost a write conflict on attempt %d; replaying in %v: %v\n", commitMsg, attempts, wait, err)
	}
	var timer backoff.Timer
	if replayTimer != nil {
		timer = replayTimer()
	}
	err := backoff.RetryNotifyWithTimer(func() error {
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
	}, policy, notify, timer)
	if err == nil || lastConflict == nil || !replayableConflict(lastAttempt) {
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
		// stopped and the conflict it was retrying. That is not exhaustion.
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
