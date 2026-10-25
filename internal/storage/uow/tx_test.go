package uow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/domain"
	publicops "github.com/steveyegge/beads/issueops"
)

// mockUnitOfWork implements UnitOfWork for testing
type mockUnitOfWork struct {
	commitErr error
	// commitDelay, when set, is slept inside Commit before returning
	// commitErr — lets a test simulate an attempt that takes real wall-clock
	// time to fail (a slow commit racing a context deadline), as opposed to
	// commitErr alone, which fails instantly.
	commitDelay       time.Duration
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
	if m.commitDelay > 0 {
		time.Sleep(m.commitDelay)
	}
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

// TestRunTxResultWithin_SlowAttemptStopsBeforeADoomedRetry pins the fix for
// the gap bee's live 1000-item run exposed: retryTxBudget / maxElapsed is a
// BUDGET fixed once at the top of the call, and says nothing about how long
// any one attempt actually takes. Before this fix, a slow failing attempt
// (its commit loses Dolt's merge after running most of the remaining ctx
// deadline) still triggered a retry whenever elapsed-so-far was under
// maxElapsed — and that retry then got killed mid-transaction by ctx's own
// deadline, surfacing as context.DeadlineExceeded (an internal-httpapi
// isUnavailable() miss -> a generic 500) instead of the exhausted-write-
// conflict outcome this loop already reports correctly when IT gives up
// (uow.IsSerializationError -> httpapi maps that to 503 + Retry-After).
//
// The first attempt's commit sleeps 1.2s (over half the 2s ctx) before
// failing with a retryable deadlock; with retryTxAttemptHeadroom (5s) added
// on top, the ~0.8s left after that attempt can never cover another attempt
// of similar cost, so the loop must stop WITHOUT trying uw2 at all and return
// uw1's own serialization error.
//
// The look-ahead this pins is gated on publicops.HasExtendedRetryBudget, the
// same marker retryTxBudget checks (see that gate's comment in tx.go), so
// this ctx must carry it — an unmarked ctx would skip the new check entirely
// and this test would regress to asserting old, unfixed behavior.
func TestRunTxResultWithin_SlowAttemptStopsBeforeADoomedRetry(t *testing.T) {
	uw1 := &mockUnitOfWork{commitErr: newMySQLError(1213), commitDelay: 1200 * time.Millisecond}
	uw2 := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw1, uw2}}

	ctx, cancel := context.WithTimeout(publicops.WithExtendedRetryBudget(context.Background()), 2*time.Second)
	defer cancel()

	_, err := RunTxResultWithin(ctx, provider, 10*time.Second, func(ctx context.Context, uw UnitOfWork) (struct{}, string, error) {
		return struct{}{}, "slow attempt probe", nil
	})

	if err == nil {
		t.Fatal("expected an error: the only attempt tried fails on commit")
	}
	if !IsSerializationError(err) {
		t.Errorf("err = %v, want uw1's own serialization error (-> 503 Retry-After); a ctx-cancellation error here would be the regression (-> a generic 500)", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v wraps context.DeadlineExceeded: a doomed second attempt was started and got cut off by ctx instead of being skipped", err)
	}
	if provider.newUOWCalls != 1 {
		t.Errorf("NewUOW calls = %d, want exactly 1: a second attempt was started even though it could not have finished before ctx's deadline", provider.newUOWCalls)
	}
	if uw2.commitCount != 0 {
		t.Errorf("uw2.commitCount = %d, want 0: uw2 must never be reached", uw2.commitCount)
	}
}

// TestRunTxResultWithin_ShortAttemptStillRetries is
// TestRunTxResultWithin_SlowAttemptStopsBeforeADoomedRetry's control: when the
// failing attempt is fast relative to what's left on ctx, the new
// look-before-you-retry check must not block the retry that would otherwise
// succeed.
func TestRunTxResultWithin_ShortAttemptStillRetries(t *testing.T) {
	uw1 := &mockUnitOfWork{commitErr: newMySQLError(1213), commitDelay: 50 * time.Millisecond}
	uw2 := &mockUnitOfWork{}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw1, uw2}}

	// 8s comfortably exceeds retryTxAttemptHeadroom (5s) by more than the 50ms
	// first attempt costs, so the real-time budget this test takes is
	// governed by how fast the two attempts actually run (well under a
	// second), not by this deadline. Marked for the same reason as the slow-
	// attempt test above: the look-ahead only runs when this marker is set.
	ctx, cancel := context.WithTimeout(publicops.WithExtendedRetryBudget(context.Background()), 8*time.Second)
	defer cancel()

	_, err := RunTxResultWithin(ctx, provider, 15*time.Second, func(ctx context.Context, uw UnitOfWork) (struct{}, string, error) {
		return struct{}{}, "short attempt probe", nil
	})

	if err != nil {
		t.Fatalf("expected no error after retry, got %v", err)
	}
	if provider.newUOWCalls < 2 {
		t.Errorf("NewUOW calls = %d, want at least 2: a short failed attempt must still be retried", provider.newUOWCalls)
	}
	if uw2.commitCount != 1 {
		t.Errorf("uw2.commitCount = %d, want 1", uw2.commitCount)
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
