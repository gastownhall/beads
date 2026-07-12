package uow

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/domain"
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

type fakeRetryUOW struct {
	commitErr   error
	commitCalls int
	closeCalls  int
}

func (f *fakeRetryUOW) Close(context.Context) { f.closeCalls++ }

func (f *fakeRetryUOW) Commit(context.Context, string) error {
	f.commitCalls++
	return f.commitErr
}

func (f *fakeRetryUOW) ConfigUseCase() domain.ConfigUseCase         { return nil }
func (f *fakeRetryUOW) DoltRemoteUseCase() domain.DoltRemoteUseCase { return nil }
func (f *fakeRetryUOW) BootstrapUseCase() domain.BootstrapUseCase   { return nil }
func (f *fakeRetryUOW) IssueUseCase() domain.IssueUseCase           { return nil }
func (f *fakeRetryUOW) DependencyUseCase() domain.DependencyUseCase { return nil }
func (f *fakeRetryUOW) LabelUseCase() domain.LabelUseCase           { return nil }
func (f *fakeRetryUOW) CommentUseCase() domain.CommentUseCase       { return nil }
func (f *fakeRetryUOW) RawSQLUseCase() domain.RawSQLUseCase         { return nil }

type fakeRetryProvider struct {
	newUOW func(call int) (UnitOfWork, error)
	calls  int
}

func (f *fakeRetryProvider) NewUOW(context.Context) (UnitOfWork, error) {
	f.calls++
	return f.newUOW(f.calls)
}

func (f *fakeRetryProvider) Close(context.Context) error { return nil }

func mysqlSerializationErr(number uint16) error {
	return &mysql.MySQLError{Number: number, Message: "serialization failure"}
}

func TestRunWithFreshUOWRetries_SerializationRestartsEveryPhaseWithFreshUOW(t *testing.T) {
	for _, code := range []uint16{1205, 1213} {
		for _, phase := range []string{"begin", "body", "commit"} {
			t.Run(fmt.Sprintf("%s_%d", phase, code), func(t *testing.T) {
				failed := &fakeRetryUOW{}
				succeeded := &fakeRetryUOW{}
				if phase == "commit" {
					failed.commitErr = mysqlSerializationErr(code)
				}
				provider := &fakeRetryProvider{}
				provider.newUOW = func(call int) (UnitOfWork, error) {
					if call == 1 {
						if phase == "begin" {
							return nil, mysqlSerializationErr(code)
						}
						return failed, nil
					}
					return succeeded, nil
				}

				var seen []UnitOfWork
				bodyCalls := 0
				err := RunWithFreshUOWRetries(context.Background(), provider, "message", func(_ context.Context, uw UnitOfWork) error {
					bodyCalls++
					seen = append(seen, uw)
					if phase == "body" && bodyCalls == 1 {
						return mysqlSerializationErr(code)
					}
					return nil
				})
				require.NoError(t, err)
				assert.Equal(t, 2, provider.calls)
				assert.Equal(t, 1, succeeded.commitCalls)
				assert.Equal(t, 1, succeeded.closeCalls)

				if phase == "begin" {
					assert.Equal(t, 1, bodyCalls)
					require.Len(t, seen, 1)
					assert.Same(t, succeeded, seen[0])
					assert.Equal(t, 0, failed.closeCalls)
					return
				}

				require.Len(t, seen, 2)
				assert.NotSame(t, seen[0], seen[1], "retry reused the failed unit of work")
				assert.Equal(t, 1, failed.closeCalls)
				if phase == "body" {
					assert.Equal(t, 0, failed.commitCalls)
				} else {
					assert.Equal(t, 1, failed.commitCalls)
				}
			})
		}
	}
}

func TestRunWithFreshUOWRetries_NonRetryableBeginErrorStopsImmediately(t *testing.T) {
	beginErr := fmt.Errorf("connect: %w", driver.ErrBadConn)
	provider := &fakeRetryProvider{newUOW: func(int) (UnitOfWork, error) { return nil, beginErr }}
	bodyCalls := 0

	err := RunWithFreshUOWRetries(context.Background(), provider, "message", func(context.Context, UnitOfWork) error {
		bodyCalls++
		return nil
	})
	require.ErrorIs(t, err, driver.ErrBadConn)
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, 0, bodyCalls)
}

func TestRunWithFreshUOWRetries_BodyErrorStopsImmediately(t *testing.T) {
	uw := &fakeRetryUOW{}
	provider := &fakeRetryProvider{newUOW: func(int) (UnitOfWork, error) { return uw, nil }}
	bodyErr := errors.New("validation failed")

	err := RunWithFreshUOWRetries(context.Background(), provider, "message", func(context.Context, UnitOfWork) error {
		return bodyErr
	})
	require.ErrorIs(t, err, bodyErr)
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, 0, uw.commitCalls)
	assert.Equal(t, 1, uw.closeCalls)
}

func TestRunWithFreshUOWRetries_BodyConnectionErrorIsNotRetried(t *testing.T) {
	uw := &fakeRetryUOW{}
	provider := &fakeRetryProvider{newUOW: func(int) (UnitOfWork, error) { return uw, nil }}

	err := RunWithFreshUOWRetries(context.Background(), provider, "message", func(context.Context, UnitOfWork) error {
		return fmt.Errorf("body connection lost: %w", driver.ErrBadConn)
	})
	require.ErrorIs(t, err, driver.ErrBadConn)
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, 0, uw.commitCalls)
	assert.Equal(t, 1, uw.closeCalls)
}

func TestRunWithFreshUOWRetries_NonRetryableCommitErrorStopsImmediately(t *testing.T) {
	commitErr := fmt.Errorf("commit result ambiguous: %w", driver.ErrBadConn)
	uw := &fakeRetryUOW{commitErr: commitErr}
	provider := &fakeRetryProvider{newUOW: func(int) (UnitOfWork, error) { return uw, nil }}
	bodyCalls := 0

	err := RunWithFreshUOWRetries(context.Background(), provider, "message", func(context.Context, UnitOfWork) error {
		bodyCalls++
		return nil
	})
	require.ErrorIs(t, err, driver.ErrBadConn)
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, 1, bodyCalls)
	assert.Equal(t, 1, uw.commitCalls)
	assert.Equal(t, 1, uw.closeCalls)
}

func TestRunWithFreshUOWRetries_ContextCancellationStopsRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	uw := &fakeRetryUOW{}
	provider := &fakeRetryProvider{newUOW: func(int) (UnitOfWork, error) { return uw, nil }}
	bodyCalls := 0

	err := RunWithFreshUOWRetries(ctx, provider, "message", func(context.Context, UnitOfWork) error {
		bodyCalls++
		cancel()
		return mysqlSerializationErr(1213)
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, 1, bodyCalls)
	assert.Equal(t, 0, uw.commitCalls)
	assert.Equal(t, 1, uw.closeCalls)
}

func TestRunWithFreshUOWRetries_ClosesSuccessfulAttempt(t *testing.T) {
	uw := &fakeRetryUOW{}
	provider := &fakeRetryProvider{newUOW: func(int) (UnitOfWork, error) { return uw, nil }}

	require.NoError(t, RunWithFreshUOWRetries(context.Background(), provider, "message", func(context.Context, UnitOfWork) error {
		return nil
	}))
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, 1, uw.commitCalls)
	assert.Equal(t, 1, uw.closeCalls)
}

func TestRunWithFreshUOWRetriesDynamicMessage_RejectsNilMessage(t *testing.T) {
	err := RunWithFreshUOWRetriesDynamicMessage(context.Background(), &fakeRetryProvider{}, nil,
		func(context.Context, UnitOfWork) error { return nil })
	require.EqualError(t, err, "uow: fresh retry message function must not be nil")
}
