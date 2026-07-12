package uow

import (
	"context"
	"errors"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/domain/db"
)

type fakeTx struct {
	failFirst int
	failErr   error
	calls     int
}

func (f *fakeTx) Runner() db.Runner { return nil }

func (f *fakeTx) Commit(_ context.Context, _ string) error {
	f.calls++
	if f.calls <= f.failFirst {
		return f.failErr
	}
	return nil
}

func (f *fakeTx) Rollback(_ context.Context) error          { return nil }
func (f *fakeTx) RollbackUnlessCommitted(_ context.Context) {}

func serializationErr() error {
	return &mysql.MySQLError{Number: 1213, Message: "deadlock detected"}
}

func TestCommitWithRetries_SuccessFirstTry(t *testing.T) {
	tx := &fakeTx{}
	require.NoError(t, CommitWithRetries(context.Background(), tx, "msg"))
	assert.Equal(t, 1, tx.calls)
}

func TestCommitWithRetries_RetriesSerializationThenSucceeds(t *testing.T) {
	tx := &fakeTx{failFirst: 2, failErr: serializationErr()}
	require.NoError(t, CommitWithRetries(context.Background(), tx, "msg"))
	assert.Equal(t, 3, tx.calls)
}

func TestCommitWithRetries_NonRetryableStopsImmediately(t *testing.T) {
	boom := errors.New("boom")
	tx := &fakeTx{failFirst: 100, failErr: boom}
	err := CommitWithRetries(context.Background(), tx, "msg")
	require.Error(t, err)
	assert.Equal(t, boom, err)
	assert.Equal(t, 1, tx.calls)
}

func TestCommitWithRetries_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tx := &fakeTx{failFirst: 100, failErr: serializationErr()}
	err := CommitWithRetries(ctx, tx, "msg")
	require.Error(t, err)
	assert.LessOrEqual(t, tx.calls, 2)
}
