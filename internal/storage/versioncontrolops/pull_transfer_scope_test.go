package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// fakePullScopeDB backs PullWithStrategy for TestPullTransferDeadlineScope.
// ExecContext and QueryContext record the context each statement ran under;
// the first merge-path statement after the fetch blocks on gate until the test
// releases it, so the test can hold the pull between fetch and merge. Row
// plumbing for the settle path's queries (dolt_status, DOLT_MERGE,
// dolt_conflicts, dolt_constraint_violations) is delegated to a sqlmock
// *sql.DB, which the test seeds with empty result sets.
type fakePullScopeDB struct {
	*sql.DB
	mu           sync.Mutex
	stmts        []recordedTransferStmt
	mergeStarted chan struct{}
	gate         chan struct{}

	mergeGateOnce sync.Once
}

func newFakePullScopeDB(t *testing.T) (*fakePullScopeDB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	// The pull settle path reads merge state through these queries; every
	// one returns an empty set so the merge settles cleanly.
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery("SELECT 1 FROM dolt_status").
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectQuery("CALL DOLT_MERGE").
		WillReturnRows(sqlmock.NewRows([]string{"message"}))
	mock.ExpectQuery("FROM dolt_conflicts").
		WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}))
	mock.ExpectQuery("FROM dolt_conflicts").
		WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}))
	mock.ExpectQuery("FROM dolt_constraint_violations").
		WillReturnRows(sqlmock.NewRows([]string{"table"}))
	gate := make(chan struct{})
	return &fakePullScopeDB{
		DB:           sqlDB,
		mergeStarted: make(chan struct{}),
		gate:         gate,
	}, mock
}

// record notes one statement and its context. isTransfer marks DOLT_PUSH /
// DOLT_FETCH statements, which run under the derived transfer deadline; every
// other statement in the pull belongs to the local merge/settle path.
func (f *fakePullScopeDB) record(query string, ctx context.Context) (isTransfer, isMergePath bool) {
	f.mu.Lock()
	f.stmts = append(f.stmts, recordedTransferStmt{query: query, ctx: ctx})
	f.mu.Unlock()
	transfer := strings.Contains(query, "DOLT_PUSH") || strings.Contains(query, "DOLT_FETCH")
	return transfer, !transfer
}

func (f *fakePullScopeDB) ExecContext(ctx context.Context, query string, _ ...any) (sql.Result, error) {
	transfer, mergePath := f.record(query, ctx)
	if transfer {
		return nil, nil
	}
	if mergePath {
		f.mergeGateOnce.Do(func() { close(f.mergeStarted) })
		<-f.gate
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

func (f *fakePullScopeDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	transfer, mergePath := f.record(query, ctx)
	if transfer {
		return nil, errors.New("unexpected transfer query: " + query)
	}
	if mergePath {
		f.mergeGateOnce.Do(func() { close(f.mergeStarted) })
		<-f.gate
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.DB.QueryContext(ctx, query, args...)
}

// fetchCtx returns the context the DOLT_FETCH statement ran under.
func (f *fakePullScopeDB) fetchCtx(t *testing.T) context.Context {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.stmts {
		if strings.Contains(s.query, "DOLT_FETCH") {
			return s.ctx
		}
	}
	t.Fatal("no DOLT_FETCH statement was recorded")
	return nil
}

// mergeStmts returns every local merge/settle statement recorded so far, and
// requires at least one.
func (f *fakePullScopeDB) mergeStmts(t *testing.T) []recordedTransferStmt {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedTransferStmt
	for _, s := range f.stmts {
		if !strings.Contains(s.query, "DOLT_PUSH") && !strings.Contains(s.query, "DOLT_FETCH") {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		t.Fatal("no merge/settle statements recorded")
	}
	return out
}

// TestPullTransferDeadlineScope pins GH#7989 AC-2: a pull's fetch runs under
// the bounded child context, but the local merge and settle work after it
// keeps the live caller context and completes normally even once the fetch
// deadline has elapsed. On base the fetch statement runs on the caller's
// context, so the bounded-child assertion fails. A caller cancellation
// during the local merge still propagates.
func TestPullTransferDeadlineScope(t *testing.T) {
	const timeout = 100 * time.Millisecond

	t.Run("local merge and settle keep the caller context", func(t *testing.T) {
		fake, _ := newFakePullScopeDB(t)
		gate := fake.gate

		// The caller stays live throughout; its deadline is only a
		// regression watchdog.
		callerCtx, cancelCaller := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelCaller()
		ctx := WithRemoteTransferTimeout(callerCtx, timeout)

		errCh := make(chan error, 1)
		go func() { errCh <- PullWithStrategy(ctx, fake, "origin", "main", "", "") }()

		select {
		case <-fake.mergeStarted:
		case <-time.After(20 * time.Second):
			cancelCaller()
			t.Fatal("pull never reached its merge/settle statements")
		}

		// Observe the recorded fetch deadline and let it elapse with the
		// merge gated: local work must not be canceled by it.
		fetchCtx := fake.fetchCtx(t)
		fetchDeadline, hasDeadline := fetchCtx.Deadline()
		if !hasDeadline {
			cancelCaller()
			t.Fatalf("fetch statement context has no deadline; caller ctx %v", callerCtx)
		}
		wait := time.Until(fetchDeadline)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait + 50*time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-callerCtx.Done():
			cancelCaller()
			t.Fatalf("watchdog fired while waiting for the fetch deadline")
		}

		// The fetch deadline elapsed: the caller must still be live, and the
		// merge/settle statements must be running on that live caller
		// context, not on the fetch's child context.
		if err := callerCtx.Err(); err != nil {
			t.Fatalf("caller context canceled after the fetch deadline elapsed: %v", err)
		}
		for _, s := range fake.mergeStmts(t) {
			if s.ctx != ctx {
				t.Fatalf("merge statement %q ran on %v, want the caller context the pull received", s.query, s.ctx)
			}
			if err := s.ctx.Err(); err != nil {
				t.Fatalf("merge statement %q context canceled: %v", s.query, err)
			}
		}

		// Release the gate: the pull must complete normally.
		close(gate)
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("pull with an elapsed fetch deadline failed: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("pull did not complete after the gate was released")
		}

		// The fetch itself ran on a bounded child context: a deadline
		// earlier than the caller's watchdog. On base the fetch runs on
		// the caller's context and this fails.
		if fetchCtx == callerCtx {
			t.Fatal("fetch statement ran on the caller context, want a bounded child context")
		}
		callerDeadline, _ := callerCtx.Deadline()
		if !fetchDeadline.Before(callerDeadline) {
			t.Fatalf("fetch deadline %s is not earlier than the caller deadline %s", fetchDeadline, callerDeadline)
		}
	})

	t.Run("caller cancellation during local merge propagates", func(t *testing.T) {
		fake, _ := newFakePullScopeDB(t)
		gate := fake.gate

		callerCtx, cancelCaller := context.WithCancel(context.Background())
		defer cancelCaller()
		ctx := WithRemoteTransferTimeout(callerCtx, timeout)

		errCh := make(chan error, 1)
		go func() { errCh <- PullWithStrategy(ctx, fake, "origin", "main", "", "") }()

		select {
		case <-fake.mergeStarted:
		case <-time.After(20 * time.Second):
			t.Fatal("pull never reached its merge/settle statements")
		}

		cancelCaller()
		close(gate)

		select {
		case err := <-errCh:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("pull error during a canceled merge is %v, want context.Canceled", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("pull did not return after caller cancellation")
		}
	})
}