//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
)

func TestStageAndCommitAfterSQLCommitResponseLossIsIndeterminateAndNotReplayed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failCall   int
		wantCalls  int
		lastSQLHas string
	}{
		{name: "DOLT_ADD", failCall: 1, wantCalls: 1, lastSQLHas: "DOLT_ADD"},
		{name: "DOLT_COMMIT", failCall: 2, wantCalls: 2, lastSQLHas: "DOLT_COMMIT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			responseLoss := errors.New(tc.name + " response lost after SQL commit")
			conn := &postSQLCommitFailureConn{failCall: tc.failCall, err: responseLoss, status: newDoltStatusStub(t)}

			err := stageAndCommitAfterSQLCommit(t.Context(), conn,
				map[string]bool{"issues": true}, "bd: commit derived state", "tester <tester@example.com>")
			if !errors.Is(err, responseLoss) {
				t.Fatalf("stageAndCommitAfterSQLCommit() error = %v, want cause %v", err, responseLoss)
			}
			if !errors.Is(err, storage.ErrCommitIndeterminate) {
				t.Fatalf("stageAndCommitAfterSQLCommit() error = %v, want storage.ErrCommitIndeterminate", err)
			}
			if len(conn.calls) != tc.wantCalls {
				t.Fatalf("version-control calls = %d, want %d (no replay): %v", len(conn.calls), tc.wantCalls, conn.calls)
			}
			if !strings.Contains(conn.calls[len(conn.calls)-1], tc.lastSQLHas) {
				t.Fatalf("last version-control call = %q, want %s", conn.calls[len(conn.calls)-1], tc.lastSQLHas)
			}
		})
	}
}

// newDoltStatusStub returns a *sql.DB whose dolt_status queries each yield a
// single-row count of 1, standing in for the pending/staged empty-commit guard
// reads (GH#4288 re-port) that StageAndCommit issues around its CALLs, and
// whose CALLs each answer the empty result set schema.DrainCall consumes. The
// version-control call counting below deliberately tracks only QueryContext,
// which the guard reads (QueryRowContext) never reach, so they stay invisible
// to the assertions.
func newDoltStatusStub(t *testing.T) *sql.DB {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 4; i++ {
		mock.ExpectQuery(`FROM dolt_status`).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(`^CALL DOLT_`).
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type postSQLCommitFailureConn struct {
	failCall int
	err      error
	calls    []string
	status   *sql.DB
}

// ExecContext refuses: StageAndCommit drains its CALLs through QueryContext
// (schema.DrainCall), so a CALL arriving here is a revert to ExecContext.
func (*postSQLCommitFailureConn) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errors.New("unexpected ExecContext")
}

func (c *postSQLCommitFailureConn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.calls = append(c.calls, query)
	if len(c.calls) == c.failCall {
		return nil, c.err
	}
	return c.status.QueryContext(ctx, query, args...)
}

func (c *postSQLCommitFailureConn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return c.status.QueryRowContext(ctx, query, args...)
}
