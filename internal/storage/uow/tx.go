package uow

import (
	"context"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

type Tx interface {
	Runner() db.Runner
	Commit(ctx context.Context, message string) error
	Rollback(ctx context.Context) error
	RollbackUnlessCommitted(ctx context.Context)
}

type TxProvider interface {
	BeginTx(ctx context.Context) (Tx, error)
}

const (
	txRetryInitialInterval = 25 * time.Millisecond
	// txCloseTimeout bounds the detached per-attempt close below.
	txCloseTimeout = 5 * time.Second
)

// DefaultTxRetryMaxElapsed is how long the retry loop keeps redoing an attempt
// that loses Dolt's commit-time merge before giving up. Exported so callers
// that pass their own budget to RunTxResultWithin can derive it from this one
// instead of restating the number and drifting from it.
const DefaultTxRetryMaxElapsed = 15 * time.Second

// closeAttempt rolls an attempt's unit of work back on a context that outlives
// the caller's.
//
// Close sends ROLLBACK on the pinned connection, and the transaction layer
// POISONS that connection when the send fails (doltserver_tx.go) — go-sql-driver's
// session reset does not clear an open transaction, so a session that may still
// be in one must never go back to the pool. Correctness is safe either way; what
// is not safe is closing with an ALREADY-CANCELED context, which fails the
// ROLLBACK immediately and burns one pinned session every time a caller's
// context is done — an HTTP client that hangs up mid-write, or an expired
// deadline. The timeout keeps a hung rollback from blocking the caller forever.
//
// All three entry points below close through here — RunTx and RunTxResult by
// way of RunTxResultWithin, plus RunTxRead. The hazard is not specific to the
// HTTP claim: the ~nine proxied CLI commands that reach storage through RunTx
// and RunTxRead burn a session the same way when a user interrupts one
// mid-write.
func closeAttempt(ctx context.Context, uw UnitOfWork) {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), txCloseTimeout)
	defer cancel()
	uw.Close(closeCtx)
}

type TxFunc func(ctx context.Context, uw UnitOfWork) (commitMsg string, err error)

type TxFuncResult[T any] func(ctx context.Context, uw UnitOfWork) (result T, commitMsg string, err error)

type TxReadFunc[T any] func(ctx context.Context, uw UnitOfWork) (T, error)

// RunTx is RunTxResult for work that produces no result.
func RunTx(ctx context.Context, p UnitOfWorkProvider, work TxFunc) error {
	_, err := RunTxResult(ctx, p, func(ctx context.Context, uw UnitOfWork) (struct{}, string, error) {
		commitMsg, err := work(ctx, uw)
		return struct{}{}, commitMsg, err
	})
	return err
}

func RunTxResult[T any](ctx context.Context, p UnitOfWorkProvider, work TxFuncResult[T]) (T, error) {
	return RunTxResultWithin(ctx, p, DefaultTxRetryMaxElapsed, work)
}

// RunTxResultWithin is RunTxResult with an explicit retry budget, for callers
// whose deadline differs from the default and for tests that need the
// conflict-exhaustion path to arrive in milliseconds. When the budget runs out
// the last serialization failure is returned unwrapped, so callers can tell an
// exhausted write conflict from a permanent error.
func RunTxResultWithin[T any](ctx context.Context, p UnitOfWorkProvider, maxElapsed time.Duration, work TxFuncResult[T]) (T, error) {
	var result T
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = txRetryInitialInterval
	bo.MaxElapsedTime = maxElapsed

	err := backoff.Retry(func() error {
		uw, err := p.NewUOW(ctx)
		if err != nil {
			if isSerializationError(err) {
				return err
			}
			return backoff.Permanent(err)
		}
		defer closeAttempt(ctx, uw)

		r, commitMsg, err := work(ctx, uw)
		if err != nil {
			if isSerializationError(err) {
				return err
			}
			return backoff.Permanent(err)
		}

		if commitMsg == "" {
			result = r
			return nil
		}

		if err := uw.Commit(ctx, commitMsg); err != nil {
			if issueops.IsNothingToCommitError(err) {
				result = r
				return nil
			}
			if isSerializationError(err) {
				return err
			}
			return backoff.Permanent(err)
		}

		result = r
		return nil
	}, backoff.WithContext(bo, ctx))

	return result, err
}

// RunTxEphemeral is RunTxResult for work whose writes touch ONLY ephemeral
// (dolt_ignored) state — today the leases table, whose writes must mint no
// Dolt commit and no history (bd-lrgn1; ported to proxied mode by bd-aq0ql).
// On success the attempt is committed with the SQL-only form (Tx.Commit with
// an empty message ⇒ plain COMMIT, no DOLT_COMMIT), which persists the
// working set but records nothing in dolt_log — the proxied analog of the
// classic DoltStore.withRetryTx commit discipline that HeartbeatIssue relies
// on. A serialization loser (heartbeat and reclaim/close contend on the same
// lease row by design) is replayed against a fresh unit of work, exactly like
// RunTxResult.
//
// Do NOT use this for work that writes versioned tables: those writes would
// persist while silently bypassing Dolt history. Every versioned write goes
// through RunTx/RunTxResult with a real commit message.
func RunTxEphemeral[T any](ctx context.Context, p UnitOfWorkProvider, work TxReadFunc[T]) (T, error) {
	var result T
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = txRetryInitialInterval
	bo.MaxElapsedTime = DefaultTxRetryMaxElapsed

	err := backoff.Retry(func() error {
		uw, err := p.NewUOW(ctx)
		if err != nil {
			if isSerializationError(err) {
				return err
			}
			return backoff.Permanent(err)
		}
		defer closeAttempt(ctx, uw)

		r, err := work(ctx, uw)
		if err != nil {
			if isSerializationError(err) {
				return err
			}
			return backoff.Permanent(err)
		}

		if err := uw.Commit(ctx, ""); err != nil {
			if isSerializationError(err) {
				return err
			}
			return backoff.Permanent(err)
		}

		result = r
		return nil
	}, backoff.WithContext(bo, ctx))

	return result, err
}

func RunTxRead[T any](ctx context.Context, p UnitOfWorkProvider, work TxReadFunc[T]) (T, error) {
	var zero T
	uw, err := p.NewUOW(ctx)
	if err != nil {
		return zero, err
	}
	defer closeAttempt(ctx, uw)

	return work(ctx, uw)
}
