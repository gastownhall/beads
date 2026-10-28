package versioncontrolops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// TestMergeSettleDrainsCallResultSets pins every CALL DOLT_* the merge-settle
// machinery issues to schema.DrainCall (QueryContext plus a drain of every
// result set) instead of plain ExecContext (GH#5556). Callers pin the session
// (a *sql.Tx in server mode, a *sql.Conn returned to the pool afterwards by
// MergeWithStrategy), so a CALL that fails before go-sql-driver/mysql's own
// handleOk.discardResults() leaves its result sets unread and poisons every
// later statement on that session ("busy buffer" -> "driver: bad connection");
// see schema.DrainCall's doc comment.
//
// sqlmock satisfies an ExpectQuery only for a driver Query call, so in ordered
// mode a CALL reverted to ExecContext fails with "call to ExecQuery ... was not
// expected". The recordingConn wrapper also catches reverts on paths that
// swallow the CALL's error (abortMerge, the "up to date" merge swallow): every
// subtest asserts no CALL reached ExecContext.
func TestMergeSettleDrainsCallResultSets(t *testing.T) {
	t.Run("merge_and_settle_metadata", func(t *testing.T) {
		rec, mock := newDrainMock(t)
		expectWorkingSetClean(mock)
		expectConflictFlags(mock)
		expectDrainedCall(mock, "CALL DOLT_MERGE(?)", "origin/main")
		expectConflicts(mock, "metadata")
		expectDrainedCall(mock, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'metadata')")
		expectDrainedCall(mock, "CALL DOLT_ADD('metadata')")
		expectNoViolations(mock)
		expectDrainedCall(mock, autoResolveCommitCall)

		if err := MergeAndSettleWithStrategy(context.Background(), rec, "origin/main", ""); err != nil {
			t.Fatalf("MergeAndSettleWithStrategy: %v", err)
		}
		rec.assertDrained(t, mock)
	})

	t.Run("merge_with_strategy_clean", func(t *testing.T) {
		rec, mock := newDrainMock(t)
		expectWorkingSetClean(mock)
		expectConflictFlags(mock)
		expectDrainedCall(mock, "CALL DOLT_MERGE('--author', ?, ?)", testAuthor, "feature")
		expectConflicts(mock)

		conflicts, err := MergeWithStrategy(context.Background(), rec, "feature", testAuthor, "theirs")
		if err != nil || conflicts != nil {
			t.Fatalf("MergeWithStrategy = (%v, %v), want (nil, nil)", conflicts, err)
		}
		rec.assertDrained(t, mock)
	})

	t.Run("auto_resolve_config_and_schema_migrations", func(t *testing.T) {
		rec, mock := newDrainMock(t)
		expectConflicts(mock, "config", "schema_migrations")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT our_key, their_key FROM dolt_conflicts_config")).
			WillReturnRows(sqlmock.NewRows([]string{"our_key", "their_key"}).AddRow("kv.memory.a", "kv.memory.a"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT our_version, their_version, our_content_hash, their_content_hash")).
			WillReturnRows(sqlmock.NewRows([]string{"our_version", "their_version", "our_content_hash", "their_content_hash"}).
				AddRow(7, 7, nil, "h7"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(our_key, their_key) FROM dolt_conflicts_config")).
			WillReturnRows(sqlmock.NewRows([]string{"key"}))
		expectDrainedCall(mock, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'config')")
		expectDrainedCall(mock, "CALL DOLT_ADD('config')")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT our_version, our_content_hash, their_content_hash")).
			WillReturnRows(sqlmock.NewRows([]string{"our_version", "our_content_hash", "their_content_hash"}).
				AddRow(7, nil, "h7"))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE schema_migrations SET content_hash = ? WHERE version = ?")).
			WithArgs("h7", int64(7)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectDrainedCall(mock, "CALL DOLT_CONFLICTS_RESOLVE('--ours', 'schema_migrations')")
		expectDrainedCall(mock, "CALL DOLT_ADD('schema_migrations')")

		resolved, err := TryAutoResolveMergeConflicts(context.Background(), rec)
		if err != nil || !resolved {
			t.Fatalf("TryAutoResolveMergeConflicts = (%v, %v), want (true, nil)", resolved, err)
		}
		rec.assertDrained(t, mock)
	})

	t.Run("fk_cascade_repair_labels", func(t *testing.T) {
		rec, mock := newDrainMock(t)
		expectViolations(mock, "labels")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT violation_type, violation_info FROM dolt_constraint_violations_labels")).
			WillReturnRows(sqlmock.NewRows([]string{"violation_type", "violation_info"}).
				AddRow("foreign key", `{"ReferencedTable":"issues"}`))
		mock.ExpectExec(regexp.QuoteMeta(fkCascadeRepairDeletes["labels"])).
			WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(regexp.QuoteMeta("DELETE FROM dolt_constraint_violations_labels")).
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectDrainedCall(mock, "CALL DOLT_ADD('labels')")
		expectNoViolations(mock)

		repaired, had, err := TryRepairFKCascadeViolations(context.Background(), rec)
		if err != nil || !repaired || !had {
			t.Fatalf("TryRepairFKCascadeViolations = (%v, %v, %v), want (true, true, nil)", repaired, had, err)
		}
		rec.assertDrained(t, mock)
	})
}

const testAuthor = "Tester <tester@example.com>"

// autoResolveCommitCall is CommitResolvedConflicts' exact statement.
const autoResolveCommitCall = "CALL DOLT_COMMIT('-m', 'auto-resolve merge conflicts: metadata, dependencies, schema_migrations, config, issues (field-level three-way merge), labels/comments/events (union)')"

// recordingConn logs every statement sent through ExecContext and QueryContext
// before passing it to the wrapped sqlmock connection. sqlmock's
// ExpectationsWereMet does not flag an unexpected extra statement, and the
// best-effort abortMerge discards the error such a statement gets, so the log
// is what proves no CALL took the exec path and no stray DOLT_RESET ran.
type recordingConn struct {
	DBConn
	execs   []string
	queries []string
}

func (c *recordingConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	c.execs = append(c.execs, query)
	return c.DBConn.ExecContext(ctx, query, args...)
}

func (c *recordingConn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.queries = append(c.queries, query)
	return c.DBConn.QueryContext(ctx, query, args...)
}

// assertDrained fails the test when any expectation is unmet or any CALL was
// sent through ExecContext.
func (c *recordingConn) assertDrained(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
	for _, q := range c.execs {
		if strings.HasPrefix(q, "CALL ") {
			t.Errorf("%q went through ExecContext; CALLs must go through schema.DrainCall", q)
		}
	}
}

func newDrainMock(t *testing.T) (*recordingConn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &recordingConn{DBConn: db}, mock
}

// expectDrainedCall expects call as a query answering an empty result set, the
// shape schema.DrainCall consumes.
func expectDrainedCall(mock sqlmock.Sqlmock, call string, args ...driver.Value) *sqlmock.ExpectedQuery {
	q := mock.ExpectQuery(regexp.QuoteMeta(call))
	if len(args) > 0 {
		q = q.WithArgs(args...)
	}
	return q.WillReturnRows(sqlmock.NewRows([]string{"status"}))
}

func expectWorkingSetClean(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM dolt_status LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
}

func expectConflictFlags(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 1")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_force_transaction_commit = 1")).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

// expectConflicts answers one dolt_conflicts read with one conflict per table.
func expectConflicts(mock sqlmock.Sqlmock, tables ...string) {
	rows := sqlmock.NewRows([]string{"table", "num_conflicts"})
	for _, table := range tables {
		rows.AddRow(table, 1)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).WillReturnRows(rows)
}

func expectNoViolations(mock sqlmock.Sqlmock) {
	expectViolations(mock)
}

// expectViolations answers one dolt_constraint_violations read with tables.
func expectViolations(mock sqlmock.Sqlmock, tables ...string) {
	rows := sqlmock.NewRows([]string{"table"})
	for _, table := range tables {
		rows.AddRow(table)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `table` FROM dolt_constraint_violations WHERE num_violations > 0")).
		WillReturnRows(rows)
}
