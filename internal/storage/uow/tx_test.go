package uow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/domain"
)

// mockUnitOfWork implements UnitOfWork for testing
type mockUnitOfWork struct {
	commitErr   error
	commitCount int
	closed      bool
}

func (m *mockUnitOfWork) Close(ctx context.Context) {
	m.closed = true
}

func (m *mockUnitOfWork) Commit(ctx context.Context, message string) error {
	m.commitCount++
	return m.commitErr
}

func (m *mockUnitOfWork) ConfigUseCase() domain.ConfigUseCase         { return nil }
func (m *mockUnitOfWork) DoltRemoteUseCase() domain.DoltRemoteUseCase { return nil }
func (m *mockUnitOfWork) BootstrapUseCase() domain.BootstrapUseCase   { return nil }
func (m *mockUnitOfWork) IssueUseCase() domain.IssueUseCase           { return nil }
func (m *mockUnitOfWork) DependencyUseCase() domain.DependencyUseCase { return nil }
func (m *mockUnitOfWork) LabelUseCase() domain.LabelUseCase           { return nil }
func (m *mockUnitOfWork) CommentUseCase() domain.CommentUseCase       { return nil }
func (m *mockUnitOfWork) RawSQLUseCase() domain.RawSQLUseCase         { return nil }

// mockUnitOfWorkProvider implements UnitOfWorkProvider for testing
type mockUnitOfWorkProvider struct {
	uows      []*mockUnitOfWork
	uowIndex  int
	newUOWErr error
}

func (m *mockUnitOfWorkProvider) NewUOW(ctx context.Context) (UnitOfWork, error) {
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
