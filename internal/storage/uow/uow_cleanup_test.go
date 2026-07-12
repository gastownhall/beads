package uow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/domain/db"
)

type cleanupObservingTx struct {
	contextErr  error
	deadline    time.Time
	hasDeadline bool
}

func (t *cleanupObservingTx) Runner() db.Runner { return nil }

func (t *cleanupObservingTx) Commit(context.Context, string) error { return nil }

func (t *cleanupObservingTx) Rollback(context.Context) error { return nil }

func (t *cleanupObservingTx) RollbackUnlessCommitted(ctx context.Context) {
	t.contextErr = ctx.Err()
	t.deadline, t.hasDeadline = ctx.Deadline()
}

func TestBaseUOWCloseUsesBoundedUncanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	tx := &cleanupObservingTx{}
	(&baseUOW{tx: tx}).Close(ctx)

	require.NoError(t, tx.contextErr, "rollback cleanup inherited the canceled command context")
	require.True(t, tx.hasDeadline, "rollback cleanup must remain bounded")
	require.GreaterOrEqual(t, tx.deadline.Sub(started), uowCleanupTimeout-time.Second)
	require.LessOrEqual(t, tx.deadline.Sub(started), uowCleanupTimeout+time.Second)
}
