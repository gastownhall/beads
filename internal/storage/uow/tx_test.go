package uow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/domain"
)

// mockUnitOfWork implements UnitOfWork for testing
type mockUnitOfWork struct {
	commitErr         error
	commitCount       int
	closed            bool
	configUseCase     domain.ConfigUseCase
	issueUseCase      domain.IssueUseCase
	dependencyUseCase domain.DependencyUseCase
	labelUseCase      domain.LabelUseCase
	commentUseCase    domain.CommentUseCase
	// Recorded AT Close time: the close context's state afterwards says
	// nothing, because a detached close cancels its own context on the way out.
	closeErr         error
	closeHasDeadline bool
}

func (m *mockUnitOfWork) Close(ctx context.Context) {
	m.closed = true
	m.closeErr = ctx.Err()
	_, m.closeHasDeadline = ctx.Deadline()
}

func (m *mockUnitOfWork) Commit(ctx context.Context, message string) error {
	m.commitCount++
	return m.commitErr
}

func (m *mockUnitOfWork) SwitchDatabase(ctx context.Context, database string) error { return nil }

func (m *mockUnitOfWork) ConfigUseCase() domain.ConfigUseCase         { return m.configUseCase }
func (m *mockUnitOfWork) DoltRemoteUseCase() domain.DoltRemoteUseCase { return nil }
func (m *mockUnitOfWork) IssueUseCase() domain.IssueUseCase           { return m.issueUseCase }
func (m *mockUnitOfWork) DependencyUseCase() domain.DependencyUseCase { return m.dependencyUseCase }
func (m *mockUnitOfWork) LabelUseCase() domain.LabelUseCase           { return m.labelUseCase }
func (m *mockUnitOfWork) CommentUseCase() domain.CommentUseCase       { return m.commentUseCase }
func (m *mockUnitOfWork) RawSQLUseCase() domain.RawSQLUseCase         { return nil }
func (m *mockUnitOfWork) EventsJournalUseCase() domain.EventsJournalUseCase {
	return nil
}

// mockUnitOfWorkProvider implements UnitOfWorkProvider for testing
type mockUnitOfWorkProvider struct {
	uows        []*mockUnitOfWork
	uowIndex    int
	newUOWCalls int
	newUOWErr   error
}

func (m *mockUnitOfWorkProvider) NewUOW(ctx context.Context) (UnitOfWork, error) {
	m.newUOWCalls++
	if m.newUOWErr != nil {
		return nil, m.newUOWErr
	}
	if m.uowIndex >= len(m.uows) {
		return &mockUnitOfWork{}, nil
	}
	uw := m.uows[m.uowIndex]
	m.uowIndex++
	return uw, nil
}

func (m *mockUnitOfWorkProvider) Close(ctx context.Context) error {
	return nil
}

func newMySQLError(code uint16) error {
	return &mysql.MySQLError{Number: code, Message: "test error"}
}

type sqlStateError string

func (e sqlStateError) Error() string    { return "sqlstate " + string(e) }
func (e sqlStateError) SQLState() string { return string(e) }

func TestRunTx_Success(t *testing.T) {
	uw := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}

	err := RunTx(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "test commit", nil
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if uw.commitCount != 1 {
		t.Errorf("expected 1 commit, got %d", uw.commitCount)
	}
	if !uw.closed {
		t.Error("expected UOW to be closed")
	}
}

func TestRunTx_EmptyCommitMessageSkipsCommit(t *testing.T) {
	uw := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}

	err := RunTx(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "", nil // empty commit message
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if uw.commitCount != 0 {
		t.Errorf("expected 0 commits (skipped), got %d", uw.commitCount)
	}
	if !uw.closed {
		t.Error("expected UOW to be closed")
	}
}

func TestRunTx_WorkFunctionError(t *testing.T) {
	uw := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}
	workErr := errors.New("work failed")

	err := RunTx(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "", workErr
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, workErr) {
		t.Errorf("expected work error, got %v", err)
	}
	if uw.commitCount != 0 {
		t.Errorf("expected 0 commits on error, got %d", uw.commitCount)
	}
}

func TestRunTx_RetriesOnSerializationError(t *testing.T) {
	// First UOW will fail with serialization error, second will succeed
	uw1 := &mockUnitOfWork{commitErr: newMySQLError(1213)} // deadlock
	uw2 := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw1, uw2}}

	var callCount int32
	err := RunTx(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		atomic.AddInt32(&callCount, 1)
		return "test commit", nil
	})

	if err != nil {
		t.Fatalf("expected no error after retry, got %v", err)
	}
	if callCount < 2 {
		t.Errorf("expected at least 2 calls (retry), got %d", callCount)
	}
	if uw2.commitCount != 1 {
		t.Errorf("expected 1 successful commit, got %d", uw2.commitCount)
	}
}

func TestRunTx_RetriesOnLockWaitTimeout(t *testing.T) {
	// First UOW will fail with lock wait timeout, second will succeed
	uw1 := &mockUnitOfWork{commitErr: newMySQLError(1205)} // lock wait timeout
	uw2 := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw1, uw2}}

	var callCount int32
	err := RunTx(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		atomic.AddInt32(&callCount, 1)
		return "test commit", nil
	})

	if err != nil {
		t.Fatalf("expected no error after retry, got %v", err)
	}
	if callCount < 2 {
		t.Errorf("expected at least 2 calls (retry), got %d", callCount)
	}
}

func TestRunTx_RetriesOnPostgresSerializationStates(t *testing.T) {
	for _, state := range []string{"40001", "40P01"} {
		t.Run(state, func(t *testing.T) {
			first := &mockUnitOfWork{commitErr: sqlStateError(state)}
			second := &mockUnitOfWork{}
			provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{first, second}}

			var calls int32
			err := RunTx(context.Background(), provider, func(context.Context, UnitOfWork) (string, error) {
				atomic.AddInt32(&calls, 1)
				return "retry postgres serialization", nil
			})
			if err != nil {
				t.Fatalf("RunTx() error = %v", err)
			}
			if calls != 2 {
				t.Fatalf("work calls = %d, want 2", calls)
			}
		})
	}
}

func TestRunTx_NothingToCommitIsSuccess(t *testing.T) {
	uw := &mockUnitOfWork{commitErr: errors.New("nothing to commit")}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}

	err := RunTx(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "test commit", nil
	})

	if err != nil {
		t.Fatalf("expected nothing-to-commit to be treated as success, got %v", err)
	}
}

func TestRunTx_PermanentErrorNotRetried(t *testing.T) {
	uw := &mockUnitOfWork{commitErr: errors.New("some other error")}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}

	var callCount int32
	err := RunTx(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		atomic.AddInt32(&callCount, 1)
		return "test commit", nil
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if callCount != 1 {
		t.Errorf("expected exactly 1 call (no retry for permanent error), got %d", callCount)
	}
}

func TestRunTxResult_Success(t *testing.T) {
	uw := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}

	result, err := RunTxResult(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (string, string, error) {
		return "my result", "test commit", nil
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result != "my result" {
		t.Errorf("expected 'my result', got %q", result)
	}
	if uw.commitCount != 1 {
		t.Errorf("expected 1 commit, got %d", uw.commitCount)
	}
}

func TestRunTxResult_EmptyCommitMessageSkipsCommit(t *testing.T) {
	uw := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}

	result, err := RunTxResult(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (int, string, error) {
		return 42, "", nil // empty commit message
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result != 42 {
		t.Errorf("expected 42, got %d", result)
	}
	if uw.commitCount != 0 {
		t.Errorf("expected 0 commits (skipped), got %d", uw.commitCount)
	}
}

func TestRunTxResult_RetriesOnSerializationError(t *testing.T) {
	uw1 := &mockUnitOfWork{commitErr: newMySQLError(1213)}
	uw2 := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw1, uw2}}

	var callCount int32
	result, err := RunTxResult(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (int, string, error) {
		atomic.AddInt32(&callCount, 1)
		return int(callCount), "test commit", nil
	})

	if err != nil {
		t.Fatalf("expected no error after retry, got %v", err)
	}
	if result < 2 {
		t.Errorf("expected result from retry attempt, got %d", result)
	}
}

// TestRunTxResultWithin_ExhaustedBudgetReturnsSerializationError pins what a
// caller branches on when every attempt loses Dolt's commit-time merge: the
// explicit budget bounds the loop, and the error handed back is the last
// serialization failure itself — not a context error, not a wrapper. Callers
// (bd update on the proxied-server path, and the HTTP claim endpoint after it)
// use IsSerializationError on this error to report an exhausted write conflict
// loudly instead of exiting 0 on a write that never landed.
func TestRunTxResultWithin_ExhaustedBudgetReturnsSerializationError(t *testing.T) {
	provider := &mockUnitOfWorkProvider{}

	var callCount int32
	start := time.Now()
	_, err := RunTxResultWithin(context.Background(), provider, 100*time.Millisecond,
		func(ctx context.Context, uw UnitOfWork) (int, string, error) {
			atomic.AddInt32(&callCount, 1)
			return 0, "", newMySQLError(1213)
		})

	if err == nil {
		t.Fatal("expected an error once the retry budget ran out")
	}
	if !IsSerializationError(err) {
		t.Errorf("err = %v, want the last serialization failure", err)
	}
	if callCount < 2 {
		t.Errorf("attempts = %d, want more than one before the budget ran out", callCount)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("elapsed = %s: the explicit budget was not honored", elapsed)
	}
}

func TestRunTxResult_NothingToCommitReturnsResult(t *testing.T) {
	uw := &mockUnitOfWork{commitErr: errors.New("nothing to commit")}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}

	result, err := RunTxResult(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (string, string, error) {
		return "my result", "test commit", nil
	})

	if err != nil {
		t.Fatalf("expected nothing-to-commit to succeed, got %v", err)
	}
	if result != "my result" {
		t.Errorf("expected 'my result', got %q", result)
	}
}

// TestRunTxResult_ClosesWithADetachedContext protects the pinned connection.
// Close sends ROLLBACK, and the transaction layer poisons the connection when
// that send fails rather than returning it to the pool — so closing with the
// caller's already-canceled context (an HTTP client that hung up mid-claim, an
// expired deadline) would burn one session every time. Correctness is safe
// either way; capacity is not.
func TestRunTxResult_ClosesWithADetachedContext(t *testing.T) {
	uw := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}

	ctx, cancel := context.WithCancel(context.Background())
	_, err := RunTxResult(ctx, provider, func(context.Context, UnitOfWork) (int, string, error) {
		// The caller goes away while the attempt is in flight.
		cancel()
		return 1, "", nil
	})
	if err != nil {
		t.Fatalf("RunTxResult: %v", err)
	}

	if !uw.closed {
		t.Fatal("unit of work was never closed; the rollback is not guaranteed")
	}
	if uw.closeErr != nil {
		t.Fatalf("close context was already done (%v): the ROLLBACK cannot be sent, so the pinned connection is poisoned instead of returned", uw.closeErr)
	}
	if !uw.closeHasDeadline {
		t.Error("close context has no deadline; a hung rollback would block the caller forever")
	}
}

// TestRunTxClosesWithADetachedContext and its RunTxRead twin: same hazard, same
// protection, different entry point. These two are what the ~nine proxied CLI
// commands run through, so the caller whose context goes away mid-attempt is a
// user pressing Ctrl-C rather than an HTTP client hanging up — and it burns the
// pinned session exactly the same way.
func TestRunTxClosesWithADetachedContext(t *testing.T) {
	uw := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}

	ctx, cancel := context.WithCancel(context.Background())
	err := RunTx(ctx, provider, func(context.Context, UnitOfWork) (string, error) {
		cancel()
		return "", nil
	})
	if err != nil {
		t.Fatalf("RunTx: %v", err)
	}

	if !uw.closed {
		t.Fatal("unit of work was never closed; the rollback is not guaranteed")
	}
	if uw.closeErr != nil {
		t.Fatalf("close context was already done (%v): the ROLLBACK cannot be sent, so the pinned connection is poisoned instead of returned", uw.closeErr)
	}
	if !uw.closeHasDeadline {
		t.Error("close context has no deadline; a hung rollback would block the caller forever")
	}
}

func TestRunTxReadClosesWithADetachedContext(t *testing.T) {
	uw := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}

	ctx, cancel := context.WithCancel(context.Background())
	_, err := RunTxRead(ctx, provider, func(context.Context, UnitOfWork) (int, error) {
		cancel()
		return 1, nil
	})
	if err != nil {
		t.Fatalf("RunTxRead: %v", err)
	}

	if !uw.closed {
		t.Fatal("unit of work was never closed; the rollback is not guaranteed")
	}
	if uw.closeErr != nil {
		t.Fatalf("close context was already done (%v): the ROLLBACK cannot be sent, so the pinned connection is poisoned instead of returned", uw.closeErr)
	}
	if !uw.closeHasDeadline {
		t.Error("close context has no deadline; a hung rollback would block the caller forever")
	}
}

func TestRunTxRead_Success(t *testing.T) {
	uw := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}

	result, err := RunTxRead(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "read result", nil
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result != "read result" {
		t.Errorf("expected 'read result', got %q", result)
	}
	if uw.commitCount != 0 {
		t.Errorf("expected 0 commits for read operation, got %d", uw.commitCount)
	}
	if !uw.closed {
		t.Error("expected UOW to be closed")
	}
}

func TestRunTxRead_Error(t *testing.T) {
	uw := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}
	readErr := errors.New("read failed")

	_, err := RunTxRead(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "", readErr
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, readErr) {
		t.Errorf("expected read error, got %v", err)
	}
}

func TestRunTx_ContextCancellation(t *testing.T) {
	uw1 := &mockUnitOfWork{commitErr: newMySQLError(1213)}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw1}}

	ctx, cancel := context.WithCancel(context.Background())

	var callCount int32
	err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		count := atomic.AddInt32(&callCount, 1)
		if count == 1 {
			cancel()
		}
		return "test commit", nil
	})

	if err == nil {
		t.Fatal("expected error due to cancelled context")
	}
	if callCount > 2 {
		t.Errorf("expected retries to stop after context cancellation, got %d calls", callCount)
	}
}

func TestRunTx_NewUOWError(t *testing.T) {
	provider := &mockUnitOfWorkProvider{newUOWErr: errors.New("connection failed")}

	err := RunTx(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "test commit", nil
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRunTx_WorkSerializationErrorRetries(t *testing.T) {
	// Work function itself returns serialization error (not commit)
	uw1 := &mockUnitOfWork{}
	uw2 := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw1, uw2}}

	var callCount int32
	err := RunTx(context.Background(), provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		count := atomic.AddInt32(&callCount, 1)
		if count == 1 {
			return "", newMySQLError(1213) // deadlock from work function
		}
		return "test commit", nil
	})

	if err != nil {
		t.Fatalf("expected no error after retry, got %v", err)
	}
	if callCount < 2 {
		t.Errorf("expected at least 2 calls (retry), got %d", callCount)
	}
}
