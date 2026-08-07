//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

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
			conn := &postSQLCommitFailureConn{failCall: tc.failCall, err: responseLoss}

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

type postSQLCommitFailureConn struct {
	failCall int
	err      error
	calls    []string
}

func (c *postSQLCommitFailureConn) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	c.calls = append(c.calls, query)
	if len(c.calls) == c.failCall {
		return nil, c.err
	}
	return driver.RowsAffected(1), nil
}

func (*postSQLCommitFailureConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("unexpected QueryContext")
}

func (*postSQLCommitFailureConn) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}
