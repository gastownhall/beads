package versioncontrolops

import (
	"context"
	"time"
)

// AfterMergeGrace bounds the local work that has to follow a merge already in
// the database once its caller's context has ended: settling the conflicts a
// dolt CLI pull left, and the is_blocked recompute after a pull or merge with
// its marker and commit. Stopped the moment the caller is canceled (the first
// SIGINT or SIGTERM, GH#7989), that work leaves the merge half-settled; run
// without any bound, it lets a process holding the database keep bd from
// exiting.
const AfterMergeGrace = 15 * time.Second

// WithCancelGrace returns a context for work that must outlive the
// cancellation of ctx, but only by grace. It keeps the values of ctx. While
// ctx is live it has no deadline and only the returned cancel ends it, so it
// adds no bound to work its caller still waits for; once ctx ends, by
// cancellation or by its deadline, it ends grace later. A ctx that has
// already ended starts the grace at once. The returned cancel releases it.
func WithCancelGrace(ctx context.Context, grace time.Duration) (context.Context, context.CancelFunc) {
	if ctx.Err() != nil {
		return context.WithTimeout(context.WithoutCancel(ctx), grace)
	}
	graceCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-timer.C:
			cancel()
		case <-graceCtx.Done():
		}
	})
	return graceCtx, func() {
		stop()
		cancel()
	}
}
