package versioncontrolops

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// This file pins every CALL DOLT_* site in mergesettle.go (and the
// ResolveConflicts call those flows share) to the draining QueryContext path
// (schema.DrainCall) instead of plain ExecContext, per GH#5556: callers pin
// the session these statements run on (a server-mode *sql.Tx or a pooled
// *sql.Conn), and a CALL whose result set stays undrained on an error path
// poisons the next statement on that session. See schema.DrainCall's doc
// comment for the go-sql-driver error-path asymmetry that makes ExecContext
// unsafe for a CALL here.
//
// sqlmock only satisfies an ExpectQuery expectation for a driver Query call
// (QueryContext), not for Exec (ExecContext), so a site reverted to plain
// ExecContext fails these tests loudly instead of silently reintroducing the
// drain gap.

// commitResolvedConflictsSQL is the exact statement CommitResolvedConflicts
// issues; pinned verbatim so the expectation cannot drift from the code.
const commitResolvedConflictsSQL = "CALL DOLT_COMMIT('-m', 'auto-resolve merge conflicts: " +
	"metadata, dependencies, schema_migrations, config, issues (field-level three-way merge), " +
	"labels/comments/events (union)')"

// doltConflictsSQL is the conflicts listing both TryAutoResolveMergeConflicts
// and GetConflicts issue.
const doltConflictsSQL = "SELECT `table`, num_conflicts FROM dolt_conflicts"

// constraintViolationsSQL is the outstanding-violations listing
// TryRepairFKCascadeViolations issues.
const constraintViolationsSQL = "SELECT `table` FROM dolt_constraint_violations WHERE num_violations > 0"

// recordingConn wraps a DBConn and records every statement issued through
// ExecContext and QueryContext. sqlmock's ExpectationsWereMet does not flag
// an unexpected extra call whose error the code under test swallowed
// (abortMerge swallows both of its CALL errors), so the recording is the only
// way to catch a stray statement — a DOLT_RESET that should not have run, or
// a CALL reverted to ExecContext against a leftover expectation.
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

// newDrainMock returns an ordered sqlmock DB plus a recording wrapper around
// it. Pass the wrapper (not the raw DB) to the code under test.
func newDrainMock(t *testing.T) (*recordingConn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &recordingConn{DBConn: db}, mock
}

// assertDrainedCalls verifies every sqlmock expectation was met and that no
// statement beginning with CALL ran through ExecContext.
func assertDrainedCalls(t *testing.T, mock sqlmock.Sqlmock, rec *recordingConn) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
	for _, q := range rec.execs {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(q)), "CALL") {
			t.Errorf("statement %q ran through ExecContext; every CALL DOLT_* must use the draining QueryContext path (schema.DrainCall)", q)
		}
	}
}

// emptyCallRows is what a Dolt procedure returns when called for its side
// effects alone: a result set with no rows.
func emptyCallRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"status"})
}

func TestMergeSettleDrainsCallResultSets(t *testing.T) {
	t.Run("merge_and_settle_metadata", func(t *testing.T) {
		rec, mock := newDrainMock(t)

		mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM dolt_status LIMIT 1")).
			WillReturnRows(sqlmock.NewRows([]string{"1"}))
		mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 1")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_force_transaction_commit = 1")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE(?)")).
			WithArgs("origin/main").
			WillReturnRows(emptyCallRows())
		mock.ExpectQuery(regexp.QuoteMeta(doltConflictsSQL)).
			WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("metadata", 1))
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'metadata')")).
			WillReturnRows(emptyCallRows())
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('metadata')")).
			WillReturnRows(emptyCallRows())
		mock.ExpectQuery(regexp.QuoteMeta(constraintViolationsSQL)).
			WillReturnRows(sqlmock.NewRows([]string{"table"}))
		mock.ExpectQuery(regexp.QuoteMeta(commitResolvedConflictsSQL)).
			WillReturnRows(emptyCallRows())

		if err := MergeAndSettleWithStrategy(context.Background(), rec, "origin/main", ""); err != nil {
			t.Fatalf("MergeAndSettleWithStrategy: %v", err)
		}
		assertDrainedCalls(t, mock, rec)
	})

	t.Run("merge_with_strategy_clean", func(t *testing.T) {
		rec, mock := newDrainMock(t)

		mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM dolt_status LIMIT 1")).
			WillReturnRows(sqlmock.NewRows([]string{"1"}))
		mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 1")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_force_transaction_commit = 1")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE('--author', ?, ?)")).
			WithArgs("Tester <tester@example.com>", "feature").
			WillReturnRows(emptyCallRows())
		mock.ExpectQuery(regexp.QuoteMeta(doltConflictsSQL)).
			WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}))

		conflicts, err := MergeWithStrategy(context.Background(), rec, "feature",
			"Tester <tester@example.com>", "theirs")
		if err != nil {
			t.Fatalf("MergeWithStrategy: %v", err)
		}
		if len(conflicts) != 0 {
			t.Fatalf("MergeWithStrategy conflicts = %v, want none", conflicts)
		}
		assertDrainedCalls(t, mock, rec)
	})

	t.Run("auto_resolve_config_and_schema_migrations", func(t *testing.T) {
		rec, mock := newDrainMock(t)

		// Conflicted tables in the order the pre-screen loop must see them.
		mock.ExpectQuery(regexp.QuoteMeta(doltConflictsSQL)).
			WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}).
				AddRow("config", 1).
				AddRow("schema_migrations", 1))
		// config pre-screen: same kv.memory.* key on both sides.
		mock.ExpectQuery(regexp.QuoteMeta("\n\t\tSELECT our_key, their_key FROM dolt_conflicts_config")).
			WillReturnRows(sqlmock.NewRows([]string{"our_key", "their_key"}).
				AddRow("kv.memory.a", "kv.memory.a"))
		// schema_migrations pre-screen: vintage row, ours without a hash.
		mock.ExpectQuery(regexp.QuoteMeta("\n\t\tSELECT our_version, their_version, our_content_hash, their_content_hash\n\t\tFROM dolt_conflicts_schema_migrations")).
			WillReturnRows(sqlmock.NewRows([]string{"our_version", "their_version", "our_content_hash", "their_content_hash"}).
				AddRow(7, 7, nil, "h7"))
		// Resolved-key diagnostics: empty keeps the stderr Notice quiet.
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(our_key, their_key) FROM dolt_conflicts_config")).
			WillReturnRows(sqlmock.NewRows([]string{"key"}))
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'config')")).
			WillReturnRows(emptyCallRows())
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('config')")).
			WillReturnRows(emptyCallRows())
		// Vintage resolve: theirs carries the hash ours lacks, so the
		// working-set row is backfilled before the table-level --ours.
		mock.ExpectQuery(regexp.QuoteMeta("\n\t\tSELECT our_version, our_content_hash, their_content_hash\n\t\tFROM dolt_conflicts_schema_migrations")).
			WillReturnRows(sqlmock.NewRows([]string{"our_version", "our_content_hash", "their_content_hash"}).
				AddRow(7, nil, "h7"))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE schema_migrations SET content_hash = ? WHERE version = ?")).
			WithArgs("h7", 7).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_CONFLICTS_RESOLVE('--ours', 'schema_migrations')")).
			WillReturnRows(emptyCallRows())
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('schema_migrations')")).
			WillReturnRows(emptyCallRows())

		resolved, err := TryAutoResolveMergeConflicts(context.Background(), rec)
		if err != nil {
			t.Fatalf("TryAutoResolveMergeConflicts: %v", err)
		}
		if !resolved {
			t.Fatal("TryAutoResolveMergeConflicts resolved = false, want true")
		}
		assertDrainedCalls(t, mock, rec)
	})

	t.Run("try_repair_fk_cascade_labels", func(t *testing.T) {
		rec, mock := newDrainMock(t)

		mock.ExpectQuery(regexp.QuoteMeta(constraintViolationsSQL)).
			WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("labels"))
		// t is from the fixed fkCascadeRepairDeletes allowlist.
		//nolint:gosec // G202: hardcoded table name.
		mock.ExpectQuery(regexp.QuoteMeta("SELECT violation_type, violation_info FROM dolt_constraint_violations_labels")).
			WillReturnRows(sqlmock.NewRows([]string{"violation_type", "violation_info"}).
				AddRow("foreign key", `{"ReferencedTable":"issues"}`))
		mock.ExpectExec(regexp.QuoteMeta("DELETE FROM labels WHERE issue_id NOT IN (SELECT id FROM issues)")).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("DELETE FROM dolt_constraint_violations_labels")).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('labels')")).
			WillReturnRows(emptyCallRows())
		mock.ExpectQuery(regexp.QuoteMeta(constraintViolationsSQL)).
			WillReturnRows(sqlmock.NewRows([]string{"table"}))

		repaired, had, err := TryRepairFKCascadeViolations(context.Background(), rec)
		if err != nil {
			t.Fatalf("TryRepairFKCascadeViolations: %v", err)
		}
		if !repaired || !had {
			t.Fatalf("TryRepairFKCascadeViolations = (%v, %v), want (true, true)", repaired, had)
		}
		assertDrainedCalls(t, mock, rec)
	})
}