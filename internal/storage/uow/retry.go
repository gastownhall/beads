package uow

import (
	"context"
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
