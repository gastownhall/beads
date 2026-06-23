package uow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
)

const defaultRunInTxMaxElapsed = 15 * time.Second

// ErrCommitIndeterminate indicates that a transaction's commit could not be
// confirmed because the connection failed at or after COMMIT. The write may or
// may not have landed on the server, so the sequence is deliberately NOT
// retried — replaying fn could double-apply (e.g. create a second issue). The
// caller should re-read state to determine what happened.
var ErrCommitIndeterminate = errors.New("commit result indeterminate after connection loss")

// RunInTx opens a UnitOfWork, calls fn, and commits, retrying the whole
// sequence on transient failures with phase-aware safety:
//
//   - Pre-commit failures (NewUOW, a connection pin, or a transient error
//     raised by fn before COMMIT) leave nothing committed, so the sequence is
//     replayed.
//   - Serialization failures (1213/1205) guarantee a server-side rollback, so
//     they are replayed at any phase.
//   - A connection failure at/after COMMIT is AMBIGUOUS — the commit may have
//     succeeded before the connection dropped. It is NOT retried; RunInTx
//     returns ErrCommitIndeterminate so the caller can reconcile rather than
//     risk a double-apply.
//
// Domain errors returned by fn are never retried.
func RunInTx(ctx context.Context, p UnitOfWorkProvider, commitMsg string, fn func(uw UnitOfWork) error) error {
	return RunInTxMsg(ctx, p, func(uw UnitOfWork) (string, error) {
		return commitMsg, fn(uw)
	})
}

// RunInTxMsg is like RunInTx but fn returns the commit message together with
// any error, allowing callers whose message depends on the domain result to
// compute it inside the same retry-safe closure.
func RunInTxMsg(ctx context.Context, p UnitOfWorkProvider, fn func(uw UnitOfWork) (string, error)) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 25 * time.Millisecond
	bo.MaxElapsedTime = defaultRunInTxMaxElapsed

	return backoff.Retry(func() error {
		// Pre-commit: opening the unit of work pins a connection and starts the
		// transaction. Nothing is committed yet, so transient failures replay.
		uw, err := p.NewUOW(ctx)
		if err != nil {
			if isSerializationError(err) || isInvalidConnectionError(err) {
				return err // retry
			}
			return backoff.Permanent(err)
		}
		defer uw.Close(ctx)

		// Pre-commit: fn runs entirely inside the open transaction. A transient
		// error here (e.g. the server reaped the connection mid-write) rolled
		// the transaction back, so replaying the whole sequence is safe. A
		// domain error (validation, not-found, already-claimed, ...) is final.
		msg, err := fn(uw)
		if err != nil {
			if isSerializationError(err) || isInvalidConnectionError(err) {
				return err // pre-commit transient: retry
			}
			return backoff.Permanent(err) // domain error: do not retry
		}

		// Commit phase: this is where retry safety gets subtle.
		err = uw.Commit(ctx, msg)
		switch {
		case err == nil:
			return nil
		case isNothingToCommit(err):
			return nil // no data changes to persist — idempotent success
		case isSerializationError(err):
			return err // server rolled the tx back — safe to replay
		case isInvalidConnectionError(err):
			// Ambiguous: the commit may have landed before the connection died.
			// Retrying would re-run fn and could double-apply. Surface instead.
			return backoff.Permanent(fmt.Errorf("%w: %w", ErrCommitIndeterminate, err))
		default:
			return backoff.Permanent(err)
		}
	}, backoff.WithContext(bo, ctx))
}

func isNothingToCommit(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "nothing to commit") ||
		(strings.Contains(s, "no changes") && strings.Contains(s, "commit"))
}
