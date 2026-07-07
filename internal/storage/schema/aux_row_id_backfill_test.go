package schema

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/rowid"
)

var commentsTable = auxRekeyTables[1]

func nstr(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

func commentDigest(issueID, author, text, createdAt string) string {
	return rowid.Digest([]sql.NullString{nstr(issueID), nstr(author), nstr(text), nstr(createdAt)})
}

func expectCommentsSelect(mock sqlmock.Sqlmock) *sqlmock.ExpectedQuery {
	return mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT id, issue_id, author, text, CAST(created_at AS CHAR) FROM comments"))
}

// TestRekeyAuxRowTableConvergesRandomIDs verifies the core convergence: rows
// carrying 0037's per-clone-random ids are rewritten to the deterministic
// content-derived value, so independently-migrated clones end up identical.
func TestRekeyAuxRowTableConvergesRandomIDs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectColumnExists(mock, true)
	expectCommentsSelect(mock).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "author", "text", "created_at"}).
			AddRow("random-a", "bd-1", "steve", "first", "2026-06-09 12:00:00").
			AddRow("random-b", "bd-1", "steve", "second", "2026-06-09 12:00:01"))

	// Updates are issued in sorted-old-id order, each to ordinal 0 of its own
	// content group.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE comments SET id = ? WHERE id = ?")).
		WithArgs(rowid.New("comments", 0, commentDigest("bd-1", "steve", "first", "2026-06-09 12:00:00")), "random-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE comments SET id = ? WHERE id = ?")).
		WithArgs(rowid.New("comments", 0, commentDigest("bd-1", "steve", "second", "2026-06-09 12:00:01")), "random-b").
		WillReturnResult(sqlmock.NewResult(0, 1))

	wrote, err := rekeyAuxRowTable(context.Background(), db, commentsTable)
	if err != nil {
		t.Fatalf("rekeyAuxRowTable: %v", err)
	}
	if !wrote {
		t.Error("expected wrote=true when rows were re-keyed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRekeyAuxRowTableDuplicateRowsGetDistinctOrdinals verifies exact-duplicate
// rows (no natural identity to tell them apart) take distinct ordinals of the
// same digest, so the re-key never collapses or collides them.
func TestRekeyAuxRowTableDuplicateRowsGetDistinctOrdinals(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	digest := commentDigest("bd-1", "steve", "same text", "2026-06-09 12:00:00")

	expectColumnExists(mock, true)
	expectCommentsSelect(mock).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "author", "text", "created_at"}).
			AddRow("zzz-random", "bd-1", "steve", "same text", "2026-06-09 12:00:00").
			AddRow("aaa-random", "bd-1", "steve", "same text", "2026-06-09 12:00:00"))

	// Free rows are assigned in sorted-current-id order: aaa-random takes
	// ordinal 0, zzz-random takes ordinal 1.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE comments SET id = ? WHERE id = ?")).
		WithArgs(rowid.New("comments", 0, digest), "aaa-random").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE comments SET id = ? WHERE id = ?")).
		WithArgs(rowid.New("comments", 1, digest), "zzz-random").
		WillReturnResult(sqlmock.NewResult(0, 1))

	wrote, err := rekeyAuxRowTable(context.Background(), db, commentsTable)
	if err != nil {
		t.Fatalf("rekeyAuxRowTable: %v", err)
	}
	if !wrote {
		t.Error("expected wrote=true when duplicate rows were re-keyed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRekeyAuxRowTableKeepsHeldTargetsStable verifies a row already holding one
// of its group's deterministic ids keeps it: re-running after a partial pass
// (or after a new duplicate appears) must never swap ids within a group, which
// would collide mid-update.
func TestRekeyAuxRowTableKeepsHeldTargetsStable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	digest := commentDigest("bd-1", "steve", "same text", "2026-06-09 12:00:00")
	held := rowid.New("comments", 1, digest)

	expectColumnExists(mock, true)
	expectCommentsSelect(mock).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "author", "text", "created_at"}).
			AddRow(held, "bd-1", "steve", "same text", "2026-06-09 12:00:00").
			AddRow("random-x", "bd-1", "steve", "same text", "2026-06-09 12:00:00"))

	// The held row is untouched; the random row takes the remaining ordinal 0.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE comments SET id = ? WHERE id = ?")).
		WithArgs(rowid.New("comments", 0, digest), "random-x").
		WillReturnResult(sqlmock.NewResult(0, 1))

	wrote, err := rekeyAuxRowTable(context.Background(), db, commentsTable)
	if err != nil {
		t.Fatalf("rekeyAuxRowTable: %v", err)
	}
	if !wrote {
		t.Error("expected wrote=true when one row was re-keyed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRekeyAuxRowTableIdempotent verifies that when every row already carries a
// deterministic id, no UPDATE is issued.
func TestRekeyAuxRowTableIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	digest := commentDigest("bd-1", "steve", "hello", "2026-06-09 12:00:00")

	expectColumnExists(mock, true)
	expectCommentsSelect(mock).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "author", "text", "created_at"}).
			AddRow(rowid.New("comments", 0, digest), "bd-1", "steve", "hello", "2026-06-09 12:00:00"))
	// No ExpectExec: zero UPDATEs expected.

	wrote, err := rekeyAuxRowTable(context.Background(), db, commentsTable)
	if err != nil {
		t.Fatalf("rekeyAuxRowTable: %v", err)
	}
	if wrote {
		t.Error("expected wrote=false when all rows already deterministic")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRekeyAuxRowTableSkipsMissingTable verifies the backfill no-ops cleanly
// when the table/id column is absent (older or partial schema).
func TestRekeyAuxRowTableSkipsMissingTable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectColumnExists(mock, false)

	wrote, err := rekeyAuxRowTable(context.Background(), db, commentsTable)
	if err != nil {
		t.Fatalf("rekeyAuxRowTable: %v", err)
	}
	if wrote {
		t.Error("expected wrote=false when the id column is absent")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRekeyAuxRowIDsSkipsWhenMarkerRecorded verifies the clone-local gate: once
// the ignored marker migration is recorded, the re-key never scans a table
// again, so steady-state opens do not churn synced rows.
func TestRekeyAuxRowIDsSkipsWhenMarkerRecorded(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
		"version", auxRowRekeyMarkerVersion)
	// No further expectations: no table may be probed or scanned.

	wrote, err := rekeyAuxRowIDs(context.Background(), db, auxRowRekeyShippedMainVersion-1)
	if err != nil {
		t.Fatalf("rekeyAuxRowIDs: %v", err)
	}
	if wrote {
		t.Error("expected wrote=false when the marker is already recorded")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRekeyAuxRowIDsRunsAllTablesWhenMarkerPending verifies the one-time pass
// covers every synced aux table when the marker is still pending.
func TestRekeyAuxRowIDsRunsAllTablesWhenMarkerPending(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
		"version", auxRowRekeyMarkerVersion-1)
	expectAuxRekeySentinel(mock, false)
	expectSetAuxRekeySentinel(mock)
	// Each of the four tables is probed; this mocked world has none of them,
	// so each probe returns 0 and the loop completes without scanning.
	for range auxRekeyTables {
		expectColumnExists(mock, false)
	}
	expectClearAuxRekeySentinel(mock)

	wrote, err := rekeyAuxRowIDs(context.Background(), db, auxRowRekeyShippedMainVersion-1)
	if err != nil {
		t.Fatalf("rekeyAuxRowIDs: %v", err)
	}
	if wrote {
		t.Error("expected wrote=false when no aux table exists")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// expectAuxRekeySentinel mocks auxRekeyResumePending: the local_metadata
// table-existence probe, then (when the table exists) the sentinel-row count.
func expectAuxRekeySentinel(mock sqlmock.Sqlmock, pending bool) {
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	n := 0
	if pending {
		n = 1
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM local_metadata WHERE `key` = ?")).
		WithArgs(auxRowRekeyInProgressKey).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(n))
}

func expectSetAuxRekeySentinel(mock sqlmock.Sqlmock) {
	// Fresh clones lack the dolt-ignored local_metadata table, so the set
	// path ensures it exists first.
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS local_metadata`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata (`key`, value) VALUES (?, '1')")).
		WithArgs(auxRowRekeyInProgressKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectClearAuxRekeySentinel(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM local_metadata WHERE `key` = ?")).
		WithArgs(auxRowRekeyInProgressKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// TestRekeyAuxRowIDsSkipsConvergedLineage verifies the bd-578h9.4 gate: when
// the main cursor already reached the version that shipped with the re-key
// BEFORE this pass ran, the lineage was migrated by a rekey-aware binary and
// has converged — a pending marker just means "fresh clone" (the marker table
// is dolt-ignored and never synced), and no table may be probed or rewritten.
func TestRekeyAuxRowIDsSkipsConvergedLineage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
		"version", auxRowRekeyMarkerVersion-1)
	expectAuxRekeySentinel(mock, false)
	// No further expectations: marker is pending, but the pre-pass main
	// cursor at the watershed and no crash sentinel means no table may be
	// probed or scanned.

	wrote, err := rekeyAuxRowIDs(context.Background(), db, auxRowRekeyShippedMainVersion)
	if err != nil {
		t.Fatalf("rekeyAuxRowIDs: %v", err)
	}
	if wrote {
		t.Error("expected wrote=false for a converged lineage (pre-pass cursor at watershed)")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRekeyAuxRowIDsResumesAfterCrash covers bd-578h9.16: a pass that died
// mid-rekey already advanced the main cursor past the watershed, so without
// the sentinel the next pass would read the lineage as a converged fresh
// clone, record the marker, and strand the partially re-keyed rows forever.
// A pending sentinel must override the fresh-clone skip and resume the
// rewrite.
func TestRekeyAuxRowIDsResumesAfterCrash(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
		"version", auxRowRekeyMarkerVersion-1)
	expectAuxRekeySentinel(mock, true)
	expectSetAuxRekeySentinel(mock)
	for range auxRekeyTables {
		expectColumnExists(mock, false)
	}
	expectClearAuxRekeySentinel(mock)

	// Pre-pass cursor at the watershed — the crashed pass already migrated
	// main — yet the rewrite must run because the sentinel is pending.
	if _, err := rekeyAuxRowIDs(context.Background(), db, auxRowRekeyShippedMainVersion); err != nil {
		t.Fatalf("rekeyAuxRowIDs: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRekeyAuxRowIDsKeepsSentinelOnFailure: when a table rewrite fails, the
// sentinel must NOT be cleared — it is the only record that the rewrite is
// incomplete once the main cursor has advanced.
func TestRekeyAuxRowIDsKeepsSentinelOnFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
		"version", auxRowRekeyMarkerVersion-1)
	expectAuxRekeySentinel(mock, false)
	expectSetAuxRekeySentinel(mock)
	// First table probe fails; no DELETE of the sentinel may follow.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS`).
		WillReturnError(errors.New("boom"))

	if _, err := rekeyAuxRowIDs(context.Background(), db, auxRowRekeyShippedMainVersion-1); err == nil {
		t.Fatal("expected the table failure to propagate")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
