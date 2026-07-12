package uow

import (
	"context"
	"errors"
	"time"

	"github.com/cenkalti/backoff/v4"
)

type Committer interface {
	Commit(ctx context.Context, message string) error
}

func CommitWithRetries(ctx context.Context, c Committer, message string) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 25 * time.Millisecond
	bo.MaxElapsedTime = 15 * time.Second
	return backoff.Retry(func() error {
		err := c.Commit(ctx, message)
		if err == nil {
			return nil
		}
		if isSerializationError(err) {
			return err
		}
		return backoff.Permanent(err)
	}, backoff.WithContext(bo, ctx))
}

// RunWithFreshUOWRetries runs operation and commits it in a newly opened unit
// of work on every attempt. It retries only typed MySQL/Dolt serialization
// failures (1213 and 1205). Close rolls back and releases every failed attempt,
// so a serialization failure from NewUOW, operation, or Commit restarts the
// entire operation against a fresh snapshot.
//
// Generic connection failures are deliberately not retried. In particular, a
// connection loss during Commit is ambiguous: the commit may have landed, so a
// blind replay could apply the mutation twice. Callers that need to recover an
// ambiguous result must use an idempotency receipt outside this helper.
//
// The operation may run more than once. It must keep attempt-local results inside
// the callback until this helper returns success.
//
// Every successfully opened UnitOfWork is closed before the attempt returns.
// The provider remains owned by the caller and is not closed here.
func RunWithFreshUOWRetries(
	ctx context.Context,
	provider UnitOfWorkProvider,
	message string,
	operation func(context.Context, UnitOfWork) error,
) error {
	return runWithFreshUOWRetries(ctx, provider, func() string { return message }, operation)
}

// RunWithFreshUOWRetriesDynamicMessage is the dynamic-message form of
// RunWithFreshUOWRetries. It is intended for operations such as claim-next,
// where the durable issue ID is selected inside each attempt and therefore is
// not known until immediately before Commit. message is evaluated once per
// commit attempt, after operation returns successfully.
func RunWithFreshUOWRetriesDynamicMessage(
	ctx context.Context,
	provider UnitOfWorkProvider,
	message func() string,
	operation func(context.Context, UnitOfWork) error,
) error {
	if message == nil {
		return errors.New("uow: fresh retry message function must not be nil")
	}
	return runWithFreshUOWRetries(ctx, provider, message, operation)
}

func runWithFreshUOWRetries(
	ctx context.Context,
	provider UnitOfWorkProvider,
	message func() string,
	operation func(context.Context, UnitOfWork) error,
) error {
	if provider == nil {
		return errors.New("uow: fresh retry provider must not be nil")
	}
	if operation == nil {
		return errors.New("uow: fresh retry operation must not be nil")
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 25 * time.Millisecond
	bo.MaxElapsedTime = 15 * time.Second

	return backoff.Retry(func() error {
		if err := ctx.Err(); err != nil {
			return backoff.Permanent(err)
		}

		uw, err := provider.NewUOW(ctx)
		if err != nil {
			return freshUOWRetryError(err)
		}
		if uw == nil {
			return backoff.Permanent(errors.New("uow: provider returned a nil unit of work"))
		}
		defer uw.Close(ctx)

		if err := operation(ctx, uw); err != nil {
			return freshUOWRetryError(err)
		}
		if err := uw.Commit(ctx, message()); err != nil {
			return freshUOWRetryError(err)
		}
		return nil
	}, backoff.WithContext(bo, ctx))
}

func freshUOWRetryError(err error) error {
	if isSerializationError(err) {
		return err
	}
	return backoff.Permanent(err)
}
