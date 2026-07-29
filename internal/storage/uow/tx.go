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
	txRetryMaxElapsed      = 15 * time.Second
)

type TxFunc func(ctx context.Context, uw UnitOfWork) (commitMsg string, err error)

type TxFuncResult[T any] func(ctx context.Context, uw UnitOfWork) (result T, commitMsg string, err error)

type TxReadFunc[T any] func(ctx context.Context, uw UnitOfWork) (T, error)

func RunTx(ctx context.Context, p UnitOfWorkProvider, work TxFunc) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = txRetryInitialInterval
	bo.MaxElapsedTime = txRetryMaxElapsed

	return backoff.Retry(func() error {
		uw, err := p.NewUOW(ctx)
		if err != nil {
			if isSerializationError(err) {
				return err
			}
			return backoff.Permanent(err)
		}
		defer uw.Close(ctx)

		commitMsg, err := work(ctx, uw)
		if err != nil {
			if isSerializationError(err) {
				return err
			}
			return backoff.Permanent(err)
		}

		if commitMsg == "" {
			return nil
		}

		if err := uw.Commit(ctx, commitMsg); err != nil {
			if issueops.IsNothingToCommitError(err) {
				return nil
			}
			if isSerializationError(err) {
				return err
			}
			return backoff.Permanent(err)
		}

		return nil
	}, backoff.WithContext(bo, ctx))
}

func RunTxResult[T any](ctx context.Context, p UnitOfWorkProvider, work TxFuncResult[T]) (T, error) {
	var result T
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = txRetryInitialInterval
	bo.MaxElapsedTime = txRetryMaxElapsed

	err := backoff.Retry(func() error {
		uw, err := p.NewUOW(ctx)
		if err != nil {
			if isSerializationError(err) {
				return err
			}
			return backoff.Permanent(err)
		}
		defer uw.Close(ctx)

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

func RunTxRead[T any](ctx context.Context, p UnitOfWorkProvider, work TxReadFunc[T]) (T, error) {
	var zero T
	uw, err := p.NewUOW(ctx)
	if err != nil {
		return zero, err
	}
	defer uw.Close(ctx)

	return work(ctx, uw)
}
