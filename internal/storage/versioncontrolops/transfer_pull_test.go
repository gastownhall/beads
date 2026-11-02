package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// pullScopeConn answers a pull's DOLT_FETCH itself, recording its context,
// and hands every local statement (merge, settle) to a sqlmock connection,
// recording each one's context. onMerge runs before the DOLT_MERGE statement
// reaches sqlmock, so a test can hold local work until the fetch deadline
// has passed.
type pullScopeConn struct {
	DBConn
	onMerge func(ctx context.Context)

	mu        sync.Mutex
	fetchCtx  context.Context
	localCtxs []context.Context
}

func (c *pullScopeConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "DOLT_FETCH") {
		c.mu.Lock()
		c.fetchCtx = ctx
		c.mu.Unlock()
		return sqlmock.NewResult(0, 0), nil
	}
	if err := c.local(ctx); err != nil {
		return nil, err
	}
	return c.DBConn.ExecContext(ctx, query, args...)
}

func (c *pullScopeConn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "DOLT_MERGE(") && c.onMerge != nil {
		c.onMerge(ctx)
	}
	if err := c.local(ctx); err != nil {
		return nil, err
	}
	return c.DBConn.QueryContext(ctx, query, args...)
}

func (c *pullScopeConn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	_ = c.local(ctx)
	return c.DBConn.QueryRowContext(ctx, query, args...)
}

// local records a local statement's context and, like a driver, refuses the
// statement once that context has ended.
func (c *pullScopeConn) local(ctx context.Context) error {
	c.mu.Lock()
	c.localCtxs = append(c.localCtxs, ctx)
	c.mu.Unlock()
	return ctx.Err()
}

// waitPastFetchDeadline blocks until the recorded fetch deadline has passed,
// failing the test when the fetch ran without one.
func (c *pullScopeConn) waitPastFetchDeadline(t *testing.T) {
	c.mu.Lock()
	fetchCtx := c.fetchCtx
	c.mu.Unlock()
	if fetchCtx == nil {
		t.Error("merge started before any fetch")
		return
	}
	deadline, ok := fetchCtx.Deadline()
	if !ok {
		t.Error("fetch ran on an unbounded context; want the transfer deadline")
		return
	}
	for time.Now().Before(deadline) {
		time.Sleep(time.Until(deadline))
	}
}

// TestPullTransferDeadlineScope pins AC-2 of GH#7989: the transfer deadline
// bounds a pull's fetch only. Local merge and settle run on the caller's own
// context, so they finish normally after the fetch budget has elapsed, and a
// caller cancellation during the merge still reaches them.
func TestPullTransferDeadlineScope(t *testing.T) {
	const timeout = 50 * time.Millisecond

	t.Run("local merge outlives the fetch deadline", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		expectWorkingSetClean(mock)
		expectConflictFlags(mock)
		expectDrainedCall(mock, "CALL DOLT_MERGE(?)", "origin/main")
		expectConflicts(mock)
		expectConflicts(mock)
		expectNoViolations(mock)

		conn := &pullScopeConn{DBConn: db}
		conn.onMerge = func(context.Context) { conn.waitPastFetchDeadline(t) }

		caller := WithTransferTimeout(context.Background(), timeout)
		if err := PullWithStrategy(caller, conn, "origin", "main", "", ""); err != nil {
			t.Fatalf("PullWithStrategy: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("local merge did not complete: %v", err)
		}
		if conn.fetchCtx == caller {
			t.Error("fetch ran on the caller's context, not a bounded child")
		}
		if len(conn.localCtxs) == 0 {
			t.Fatal("no local statement ran")
		}
		for i, ctx := range conn.localCtxs {
			if ctx != caller {
				t.Errorf("local statement %d ran on %v, want the caller's context", i, ctx)
			}
		}
	})

	t.Run("caller cancellation reaches the local merge", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mock.MatchExpectationsInOrder(false)
		expectWorkingSetClean(mock)
		expectConflictFlags(mock)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		conn := &pullScopeConn{DBConn: db}
		conn.onMerge = func(context.Context) { cancel() }

		err = PullWithStrategy(WithTransferTimeout(ctx, time.Hour), conn, "origin", "main", "", "")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want the caller's context.Canceled from the merge", err)
		}
		var tte *TransferTimeoutError
		if errors.As(err, &tte) {
			t.Errorf("merge cancellation reported as a transfer timeout: %v", err)
		}
	})
}
