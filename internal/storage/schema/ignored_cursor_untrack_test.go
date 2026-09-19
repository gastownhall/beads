package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"
)

// failOnSwallowedAdvisory turns the reconcile's advisory path fatal for the
// duration of a test. Every sqlmock helper installs it, and that is the point:
// in ordered sqlmock a statement nobody primed comes back as "call was not
// expected", which the advisory path demotes to a debug line and swallows — so
// a test missing a heal expectation passed while silently exercising the
// probe-failure fallback instead of the healthy path it claimed to cover.
// Tests that mean to exercise the advisory path opt out with
// allowSwallowedAdvisory.
func failOnSwallowedAdvisory(t *testing.T) {
	t.Helper()
	saved := ignoredCursorAdvisory
	ignoredCursorAdvisory = func(format string, args ...any) {
		t.Helper()
		t.Errorf("the untrack reconcile swallowed an advisory failure (an unprimed statement?): "+
			strings.TrimRight(format, "\n"), args...)
	}
	t.Cleanup(func() { ignoredCursorAdvisory = saved })
}

// allowSwallowedAdvisory records the advisory message instead of failing, for
// the tests whose subject IS the advisory path.
func allowSwallowedAdvisory(t *testing.T) *string {
	t.Helper()
	var logged string
	saved := ignoredCursorAdvisory
	ignoredCursorAdvisory = func(format string, args ...any) {
		logged += fmt.Sprintf(strings.TrimRight(format, "\n"), args...) + "\n"
	}
	t.Cleanup(func() { ignoredCursorAdvisory = saved })
	return &logged
}

// expectHeadTableProbe mocks the tracked-at-HEAD probe. qualifier is the
// identifier-quoted database name when a selector had to put the session on
// the database, empty when it was already there.
func expectHeadTableProbe(mock sqlmock.Sqlmock, qualifier, table string, tracked bool) {
	from := ""
	if qualifier != "" {
		from = " FROM " + qualifier
	}
	rows := sqlmock.NewRows([]string{"Tables_in_testdb"})
	if tracked {
		rows.AddRow(table)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLES" + from + " AS OF 'HEAD' LIKE '" + table + "'")).
		WillReturnRows(rows)
}

// doltIgnoreRow is one dolt_ignore row the resolution probe reads back.
type doltIgnoreRow struct {
	pattern string
	ignored bool
}

// exactlyIgnored is the shape seedDoltIgnorePatterns leaves on every healthy
// database: the cursor table's own name, switched on.
func exactlyIgnored(ignored bool) []doltIgnoreRow {
	return []doltIgnoreRow{{pattern: ignoredSource.cursorTable, ignored: ignored}}
}

func expectIgnoreResolution(mock sqlmock.Sqlmock, qualifier, table string, matches []doltIgnoreRow) {
	doltIgnore := "dolt_ignore"
	if qualifier != "" {
		doltIgnore = qualifier + ".dolt_ignore"
	}
	rows := sqlmock.NewRows([]string{"pattern", "ignored"})
	for _, m := range matches {
		rows.AddRow(m.pattern, m.ignored)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pattern, ignored FROM " + doltIgnore + " WHERE ? LIKE pattern")).
		WithArgs(table).
		WillReturnRows(rows)
}

// expectSchemaTableExists mocks the shared be-bv7x existence probe.
func expectSchemaTableExists(mock sqlmock.Sqlmock, table string, exists bool) {
	n := 0
	if exists {
		n = 1
	}
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WithArgs(table).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(n))
}

// expectCursorCopyColumns mocks the per-column shape read that decides which
// columns the copy moves. present is how many of cursorTableColumns the table
// actually has, counting from the front (a pre-#4259 lineage has 2).
func expectCursorCopyColumns(mock sqlmock.Sqlmock, table string, present int) string {
	for i, column := range cursorTableColumns {
		n := 0
		if i < present {
			n = 1
		}
		mock.ExpectQuery(`(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS`).
			WithArgs(table, column).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(n))
	}
	return strings.Join(cursorTableColumns[:present], ", ")
}

// expectIgnoredCursorGate mocks the shared read-only gate.
func expectIgnoredCursorGate(mock sqlmock.Sqlmock, qualifier string, tracked bool, matches []doltIgnoreRow, stray bool) {
	expectHeadTableProbe(mock, qualifier, ignoredSource.cursorTable, tracked)
	if tracked {
		expectIgnoreResolution(mock, qualifier, ignoredSource.cursorTable, matches)
	}
	expectSchemaTableExists(mock, ignoredCursorUntrackTempTable, stray)
}

// expectIgnoredCursorHealNoop mocks the whole open-time reconcile on a healthy
// database, as MigrateUp calls it: two reads, no writes — the property that
// keeps this safe to run through a SELECT/DML-only fence.
func expectIgnoredCursorHealNoop(mock sqlmock.Sqlmock) {
	expectIgnoredCursorGate(mock, "", false, nil, false)
}

// expectIgnoredCursorBackup mocks the row-preservation half of Phase A. It
// MIRRORS: the scratch is cleared before the copy, so a stale scratch can
// never re-introduce rows the live cursor no longer has.
func expectIgnoredCursorBackup(mock sqlmock.Sqlmock, columnsPresent int) {
	mock.ExpectExec("(?s)^CREATE TABLE IF NOT EXISTS " + ignoredCursorUntrackTempTable).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectSchemaTableExists(mock, ignoredSource.cursorTable, true)
	expectCursorCopyColumns(mock, ignoredSource.cursorTable, columnsPresent)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM " + ignoredCursorUntrackTempTable)).
		WillReturnResult(sqlmock.NewResult(0, 25))
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO " + ignoredCursorUntrackTempTable)).
		WillReturnResult(sqlmock.NewResult(0, 25))
}

// expectIgnoredCursorUnstage mocks the last advisory step.
func expectIgnoredCursorUnstage(mock sqlmock.Sqlmock, staged ...string) {
	rows := sqlmock.NewRows([]string{"table_name", "staged"})
	for _, table := range staged {
		rows.AddRow(table, true)
	}
	mock.ExpectQuery("(?s)SELECT s\\.table_name, s\\.staged\\s+FROM dolt_status s").WillReturnRows(rows)
	for _, table := range staged {
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET(?)")).
			WithArgs(table).
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
	}
}

// expectIgnoredCursorUntrackCommit mocks the irreversible half of Phase A.
func expectIgnoredCursorUntrackCommit(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("DROP TABLE IF EXISTS " + ignoredSource.cursorTable)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("COMMIT")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('-f', ?)")).
		WithArgs(ignoredSource.cursorTable).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('--skip-empty', '-m', ?)")).
		WithArgs(ignoredCursorUntrackCommitMessage).
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))
}

// expectIgnoredCursorScratchDrop mocks the cleanup, including the straggler
// sweep that only fires if a concurrent commit swept the scratch into HEAD.
func expectIgnoredCursorScratchDrop(mock sqlmock.Sqlmock, sweptIntoHead bool) {
	mock.ExpectExec(regexp.QuoteMeta("DROP TABLE IF EXISTS " + ignoredCursorUntrackTempTable)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("COMMIT")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectHeadTableProbe(mock, "", ignoredCursorUntrackTempTable, sweptIntoHead)
	if !sweptIntoHead {
		return
	}
	expectIgnoredCursorUnstage(mock)
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
		WithArgs(ignoredCursorUntrackTempTable).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--skip-empty')")).
		WithArgs(ignoredCursorTempSweepCommitMessage).
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))
}

// expectIgnoredCursorRestore mocks Phase B.
func expectIgnoredCursorRestore(mock sqlmock.Sqlmock, sweptIntoHead bool) {
	mock.ExpectExec("(?s)^CREATE TABLE IF NOT EXISTS " + ignoredSource.cursorTable).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectCursorCopyColumns(mock, ignoredCursorUntrackTempTable, len(cursorTableColumns))
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO " + ignoredSource.cursorTable)).
		WillReturnResult(sqlmock.NewResult(0, 25))
	expectIgnoredCursorScratchDrop(mock, sweptIntoHead)
}

// TestHealSkipsHealthyDatabaseInTwoReads is the hot-path contract. Every bd
// invocation that opens a writable store pays this, so it must be two reads
// and nothing else — no write, no DDL, no commit — and both must be
// statements that SUCCEED, or the pooled Dolt session ends up pinned to a
// stale catalog snapshot for the rest of its life (be-bv7x).
func TestHealSkipsHealthyDatabaseInTwoReads(t *testing.T) {
	db, mock := newMockDB(t)

	expectIgnoredCursorHealNoop(mock)

	healed, err := healTrackedIgnoredCursorTable(context.Background(), db)
	if err != nil {
		t.Fatalf("healTrackedIgnoredCursorTable() error = %v, want nil", err)
	}
	if healed {
		t.Fatal("healTrackedIgnoredCursorTable() reported a heal on a healthy database")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestHealResolvesDoltIgnoreLikeDoltDoes is the data-loss guard. Dolt resolves
// conflicting dolt_ignore patterns most-specific-first, so "any matching
// ignored=1 row" is not the same question — verified against the engine, a
// database carrying ('zz%', 1) alongside ('zzprobe', 0) TRACKS zzprobe while a
// naive count calls it ignored. Believing the count there would drop and
// commit away a table the operator explicitly chose to keep versioned.
func TestHealResolvesDoltIgnoreLikeDoltDoes(t *testing.T) {
	broad := doltIgnoreRow{pattern: "ignored\\_%", ignored: true}
	exactOff := doltIgnoreRow{pattern: ignoredSource.cursorTable, ignored: false}
	exactOn := doltIgnoreRow{pattern: ignoredSource.cursorTable, ignored: true}
	midOff := doltIgnoreRow{pattern: "ignored\\_schema%", ignored: false}

	for _, tt := range []struct {
		name    string
		matches []doltIgnoreRow
		want    bool
		why     string
	}{
		{
			name:    "exact on, the seeded shape",
			matches: []doltIgnoreRow{exactOn},
			want:    true,
			why:     "the seed writes the exact name; this is every healthy database",
		},
		{
			name:    "exact off, the documented operator override",
			matches: []doltIgnoreRow{exactOff},
			want:    false,
		},
		{
			name:    "broad on plus exact off",
			matches: []doltIgnoreRow{broad, exactOff},
			want:    false,
			why:     "an exact name is maximally specific, so Dolt keeps the table TRACKED; healing here destroys the operator's lineage",
		},
		{
			name:    "broad off plus exact on",
			matches: []doltIgnoreRow{{pattern: "ignored\\_%", ignored: false}, exactOn},
			want:    true,
			why:     "the exact row wins in this direction too",
		},
		{
			name:    "broad on only",
			matches: []doltIgnoreRow{broad},
			want:    true,
			why:     "nothing contradicts it",
		},
		{
			name:    "wildcards disagree, no exact row",
			matches: []doltIgnoreRow{broad, midOff},
			want:    false,
			why:     "specificity between two wildcards is Dolt's to decide, not ours: decline rather than guess wrong destructively",
		},
		{
			name:    "no pattern covers it",
			matches: nil,
			want:    false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock := newMockDB(t)

			expectIgnoreResolution(mock, "", ignoredSource.cursorTable, tt.matches)

			got, err := tableActivelyIgnored(context.Background(), db, "", ignoredSource.cursorTable)
			if err != nil {
				t.Fatalf("tableActivelyIgnored() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("tableActivelyIgnored() = %v, want %v (%s)", got, tt.want, tt.why)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// The end-to-end version of the case above: the gate must stop before it
// touches anything.
func TestHealRespectsOperatorIgnoreOverrideUnderABroaderPattern(t *testing.T) {
	db, mock := newMockDB(t)

	expectIgnoredCursorGate(mock, "", true, []doltIgnoreRow{
		{pattern: "ignored\\_%", ignored: true},
		{pattern: ignoredSource.cursorTable, ignored: false},
	}, false)

	healed, err := healTrackedIgnoredCursorTable(context.Background(), db)
	if err != nil {
		t.Fatalf("healTrackedIgnoredCursorTable() error = %v, want nil", err)
	}
	if healed {
		t.Fatal("healTrackedIgnoredCursorTable() untracked a table Dolt keeps tracked for the operator")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The #5816 lesson: a heal that cannot run must never be the reason a database
// stops opening. A client with no read on the HEAD probe (or a transient
// failure) leaves the caller exactly where it was.
func TestHealIsNonFatalWhenTheProbeFails(t *testing.T) {
	db, mock := newMockDB(t)
	logged := allowSwallowedAdvisory(t)

	mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLES AS OF 'HEAD'")).
		WillReturnError(errors.New("command denied to user 'fenced'@'%'"))

	healed, err := healTrackedIgnoredCursorTable(context.Background(), db)
	if err != nil {
		t.Fatalf("healTrackedIgnoredCursorTable() error = %v, want nil (a failed probe must not brick the open)", err)
	}
	if healed {
		t.Fatal("healTrackedIgnoredCursorTable() reported a heal after its probe failed")
	}
	if !strings.Contains(*logged, "command denied") {
		t.Fatalf("advisory output = %q, want the discarded probe error reported", *logged)
	}
}

// The advisory zone runs to the END of the unstage sweep, not to the backup.
// A client whose grant covers CREATE and INSERT but not CALL DOLT_RESET would
// otherwise fail every open persistently with nothing destroyed — precisely
// the brick class the policy exists to prevent.
func TestHealIsNonFatalThroughTheWholePreDropZone(t *testing.T) {
	for _, tt := range []struct {
		name   string
		expect func(sqlmock.Sqlmock)
	}{
		{
			name: "scratch table cannot be created",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec("(?s)^CREATE TABLE IF NOT EXISTS " + ignoredCursorUntrackTempTable).
					WillReturnError(errors.New("command denied to user 'fenced'@'%'"))
			},
		},
		{
			name: "working set cannot be read",
			expect: func(mock sqlmock.Sqlmock) {
				expectIgnoredCursorBackup(mock, len(cursorTableColumns))
				mock.ExpectQuery("(?s)SELECT s\\.table_name, s\\.staged\\s+FROM dolt_status s").
					WillReturnError(errors.New("connection reset"))
			},
		},
		{
			name: "staging area cannot be cleared",
			expect: func(mock sqlmock.Sqlmock) {
				expectIgnoredCursorBackup(mock, len(cursorTableColumns))
				mock.ExpectQuery("(?s)SELECT s\\.table_name, s\\.staged\\s+FROM dolt_status s").
					WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}).AddRow("issues", true))
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET(?)")).
					WithArgs("issues").
					WillReturnError(errors.New("command denied to user 'fenced'@'%'"))
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			allowSwallowedAdvisory(t)

			expectIgnoredCursorGate(mock, "", true, exactlyIgnored(true), false)
			tt.expect(mock)

			healed, err := healTrackedIgnoredCursorTable(context.Background(), db)
			if err != nil {
				t.Fatalf("healTrackedIgnoredCursorTable() error = %v, want nil: nothing is destroyed before the DROP, so this must cost one open, not every open", err)
			}
			if healed {
				t.Fatal("healTrackedIgnoredCursorTable() reported a heal it did not perform")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// Past the DROP the policy inverts: the pass must not continue without a
// cursor table, so the error is returned and the open fails. It must NOT be a
// *DirtyTablesError — the embedded lenient intents and MigrateUpWithLock's
// bootstrap heal both branch on that type, and a heal failure is neither a
// dirty working set nor something DOLT_RESET('--hard') should be pointed at.
func TestHealIsFatalAfterTheDropAndIsNotADirtyTablesError(t *testing.T) {
	db, mock := newMockDB(t)

	expectIgnoredCursorGate(mock, "", true, exactlyIgnored(true), false)
	expectIgnoredCursorBackup(mock, len(cursorTableColumns))
	expectIgnoredCursorUnstage(mock)
	mock.ExpectExec(regexp.QuoteMeta("DROP TABLE IF EXISTS " + ignoredSource.cursorTable)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("COMMIT")).
		WillReturnError(errors.New("connection reset"))

	healed, err := healTrackedIgnoredCursorTable(context.Background(), db)
	if err == nil {
		t.Fatal("healTrackedIgnoredCursorTable() error = nil after the drop failed to commit, want the failure returned")
	}
	if healed {
		t.Fatal("healTrackedIgnoredCursorTable() reported success on a failed untrack")
	}
	var dirtyErr *DirtyTablesError
	if errors.As(err, &dirtyErr) {
		t.Fatalf("heal failure satisfies errors.As(*DirtyTablesError): %v — lenient opens would swallow it and the bootstrap heal would hard-reset on it", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestHealUntracksAndRestoresInOrder pins the statement contract of the whole
// repair. The order is not cosmetic: the backup must precede the drop, the
// bare COMMIT must precede each staging call (on the sql-server path Dolt's
// procedures read a working set an open transaction has not published), the
// add must carry '-f' (a plain add of an ignored table is a silent no-op, so
// without it the drop never stages and the recreate nets the whole repair back
// out), and the commit must carry '--skip-empty' so a replay is not fatal.
func TestHealUntracksAndRestoresInOrder(t *testing.T) {
	db, mock := newMockDB(t)

	expectIgnoredCursorGate(mock, "", true, exactlyIgnored(true), false)
	expectIgnoredCursorBackup(mock, len(cursorTableColumns))
	expectIgnoredCursorUnstage(mock)
	expectIgnoredCursorUntrackCommit(mock)
	expectIgnoredCursorRestore(mock, false)

	healed, err := healTrackedIgnoredCursorTable(context.Background(), db)
	if err != nil {
		t.Fatalf("healTrackedIgnoredCursorTable() error = %v", err)
	}
	if !healed {
		t.Fatal("healTrackedIgnoredCursorTable() = false on a legacy tracked database, want true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A lineage old enough to be tracked may also predate content_hash (#4259):
// the copy reads the column list off the schema rather than assuming it, or
// the backup dies on an unknown column and the heal never runs.
func TestHealCopiesLegacyShapeWithoutContentHash(t *testing.T) {
	db, mock := newMockDB(t)

	expectIgnoredCursorGate(mock, "", true, exactlyIgnored(true), false)
	expectIgnoredCursorBackup(mock, len(cursorTableColumns)-1)
	expectIgnoredCursorUnstage(mock)
	expectIgnoredCursorUntrackCommit(mock)
	expectIgnoredCursorRestore(mock, false)

	if _, err := healTrackedIgnoredCursorTable(context.Background(), db); err != nil {
		t.Fatalf("healTrackedIgnoredCursorTable() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The scratch table carries a perfectly committable name, so a concurrent
// writer's blanket commit can sweep it into HEAD during the repair window.
// Dropping it locally would then leave a permanent delete delta — the same
// class of tracked residue this whole fix exists to remove — so the deletion
// is committed, scoped to that table, after the staging area is cleared so
// nothing unrelated rides along under its message.
func TestHealCommitsAStrayScratchTableOutOfHead(t *testing.T) {
	db, mock := newMockDB(t)

	expectIgnoredCursorGate(mock, "", true, exactlyIgnored(true), false)
	expectIgnoredCursorBackup(mock, len(cursorTableColumns))
	expectIgnoredCursorUnstage(mock)
	expectIgnoredCursorUntrackCommit(mock)
	expectIgnoredCursorRestore(mock, true)

	if _, err := healTrackedIgnoredCursorTable(context.Background(), db); err != nil {
		t.Fatalf("healTrackedIgnoredCursorTable() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The resume path: a previous open committed the drop and died before putting
// the rows back. The cursor table now reads as untracked, so the gate would
// call the database healthy — the surviving scratch table is the only evidence
// that it is not, and skipping it would lose the migration cursor and replay
// the whole ignored series from zero.
func TestHealResumesFromASurvivingScratchTable(t *testing.T) {
	db, mock := newMockDB(t)

	expectIgnoredCursorGate(mock, "", false, nil, true)
	expectIgnoreResolution(mock, "", ignoredSource.cursorTable, exactlyIgnored(true))
	expectSchemaTableExists(mock, ignoredSource.cursorTable, false)
	expectIgnoredCursorRestore(mock, false)

	healed, err := healTrackedIgnoredCursorTable(context.Background(), db)
	if err != nil {
		t.Fatalf("healTrackedIgnoredCursorTable() error = %v", err)
	}
	if !healed {
		t.Fatal("healTrackedIgnoredCursorTable() = false with an interrupted run to resume, want true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestHealNeverResurrectsRowsIntoALiveCursorTable is the counterpart, and the
// reason the resume is bounded rather than a blanket union. A scratch left
// behind while the cursor table is present and correct is stale bookkeeping.
// Unioning it back in inverts the sanctioned bad-release recovery — which is a
// cursor ROLLBACK, deleting rows so a corrected migration series re-applies —
// by restoring exactly the versions the operator deleted, silently skipping
// the corrected migrations. Cleanup only: no CREATE, no INSERT.
func TestHealNeverResurrectsRowsIntoALiveCursorTable(t *testing.T) {
	db, mock := newMockDB(t)

	expectIgnoredCursorGate(mock, "", false, nil, true)
	expectIgnoreResolution(mock, "", ignoredSource.cursorTable, exactlyIgnored(true))
	expectSchemaTableExists(mock, ignoredSource.cursorTable, true)
	// Cleanup only. An INSERT here would be an unexpected statement.
	expectIgnoredCursorScratchDrop(mock, false)

	healed, err := healTrackedIgnoredCursorTable(context.Background(), db)
	if err != nil {
		t.Fatalf("healTrackedIgnoredCursorTable() error = %v", err)
	}
	if !healed {
		t.Fatal("healTrackedIgnoredCursorTable() = false with a stray scratch table to clean up, want true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The resume re-reads the operator veto rather than inheriting the decision
// from whichever open crashed. An operator who resolved a crashed repair by
// deliberately versioning the cursor table must not get stale rows pushed back
// into it — but the scratch is bd's own junk and is still cleaned up.
func TestHealResumeRespectsTheOperatorOverride(t *testing.T) {
	db, mock := newMockDB(t)

	expectIgnoredCursorGate(mock, "", false, nil, true)
	expectIgnoreResolution(mock, "", ignoredSource.cursorTable, exactlyIgnored(false))
	expectSchemaTableExists(mock, ignoredSource.cursorTable, false)
	expectIgnoredCursorScratchDrop(mock, false)

	if _, err := healTrackedIgnoredCursorTable(context.Background(), db); err != nil {
		t.Fatalf("healTrackedIgnoredCursorTable() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (an override must suppress the restore but not the cleanup): %v", err)
	}
}

// A fenced client can REACH the resume state without having created it — some
// privileged opener crashed mid-repair — and the database is fully usable in
// it. Failing its open would be a brand-new failure class, which is the one
// thing #5816 says a heal must never mint. A privilege refusal therefore
// degrades advisorily; anything else stays fatal so a privileged opener still
// converges.
func TestHealResumeDegradesForClientsThatCannotRunDDL(t *testing.T) {
	denied := &mysql.MySQLError{Number: 1142, Message: "CREATE command denied to user 'fenced'@'%'"}

	for _, tt := range []struct {
		name      string
		err       error
		wantFatal bool
	}{
		{name: "privilege refusal", err: denied},
		{name: "anything else", err: errors.New("connection reset"), wantFatal: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			allowSwallowedAdvisory(t)

			expectIgnoredCursorGate(mock, "", false, nil, true)
			expectIgnoreResolution(mock, "", ignoredSource.cursorTable, exactlyIgnored(true))
			expectSchemaTableExists(mock, ignoredSource.cursorTable, false)
			mock.ExpectExec("(?s)^CREATE TABLE IF NOT EXISTS " + ignoredSource.cursorTable).
				WillReturnError(tt.err)

			_, err := healTrackedIgnoredCursorTable(context.Background(), db)
			if tt.wantFatal && err == nil {
				t.Fatal("healTrackedIgnoredCursorTable() error = nil, want the failure returned so a privileged opener retries")
			}
			if !tt.wantFatal && err != nil {
				t.Fatalf("healTrackedIgnoredCursorTable() error = %v, want nil: a client that cannot run DDL must open read-degraded, not fail forever", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// The other crash window: the drop ran but its commit did not, so HEAD still
// carries the table while the working set does not. Phase A re-runs from the
// top, and the copy must treat the missing source as "already saved" rather
// than as a failure — otherwise the pre-mutation policy swallows it and the
// database never converges.
func TestHealResumesWhenTheDroppedTableIsGoneFromTheWorkingSet(t *testing.T) {
	db, mock := newMockDB(t)

	expectIgnoredCursorGate(mock, "", true, exactlyIgnored(true), true)
	mock.ExpectExec("(?s)^CREATE TABLE IF NOT EXISTS " + ignoredCursorUntrackTempTable).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectSchemaTableExists(mock, ignoredSource.cursorTable, false)
	expectIgnoredCursorUnstage(mock)
	expectIgnoredCursorUntrackCommit(mock)
	expectIgnoredCursorRestore(mock, false)

	healed, err := healTrackedIgnoredCursorTable(context.Background(), db)
	if err != nil {
		t.Fatalf("healTrackedIgnoredCursorTable() error = %v", err)
	}
	if !healed {
		t.Fatal("healTrackedIgnoredCursorTable() = false resuming a committed-drop crash, want true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The probe reads HEAD through a spelling that cannot fail. The direct
// `SELECT … AS OF 'HEAD'` answers the same question, but it errors on every
// healthy database, and a Dolt session that issues a failing statement stays
// pinned to its pre-statement catalog snapshot for the rest of its pooled life
// (be-bv7x). The LIKE form is also bounded to one row, so it costs what every
// other steady-state probe costs.
func TestTrackedAtHeadProbeIssuesNoFailingStatement(t *testing.T) {
	for _, tt := range []struct {
		name      string
		qualifier string
	}{
		{name: "session already on the database", qualifier: ""},
		{name: "database selected for us", qualifier: "`testdb`"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock := newMockDB(t)

			expectHeadTableProbe(mock, tt.qualifier, ignoredSource.cursorTable, false)

			tracked, err := tableTrackedAtHead(context.Background(), db, tt.qualifier, ignoredSource.cursorTable)
			if err != nil {
				t.Fatalf("tableTrackedAtHead() error = %v", err)
			}
			if tracked {
				t.Fatal("tableTrackedAtHead() = true with the cursor table absent from HEAD")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// '_' is a LIKE single-character wildcard and the cursor table's name is full
// of them, so the probe must not trust what the LIKE matched.
func TestTrackedAtHeadProbeComparesTheNameExactly(t *testing.T) {
	db, mock := newMockDB(t)

	mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLES AS OF 'HEAD' LIKE '" + ignoredSource.cursorTable + "'")).
		WillReturnRows(sqlmock.NewRows([]string{"Tables_in_testdb"}).AddRow("ignoredXschemaXmigrations"))

	tracked, err := tableTrackedAtHead(context.Background(), db, "", ignoredSource.cursorTable)
	if err != nil {
		t.Fatalf("tableTrackedAtHead() error = %v", err)
	}
	if tracked {
		t.Fatal("tableTrackedAtHead() = true on a name the LIKE wildcards matched but that is not the cursor table")
	}
}

// TestCursorTableColumnsMatchBootstrapSQL is the drift guard the untrack
// reconcile depends on: it copies cursor rows out and back BY NAME, so a
// column added to bootstrapSQL and not to cursorTableColumns would be silently
// dropped from every database the reconcile repairs. The shape has already
// changed out of band once (#4259 added content_hash).
func TestCursorTableColumnsMatchBootstrapSQL(t *testing.T) {
	ddl := ignoredSource.bootstrapSQL()
	open := strings.Index(ddl, "(")
	close := strings.LastIndex(ddl, ")")
	if open < 0 || close < open {
		t.Fatalf("bootstrapSQL() is not a parenthesised CREATE: %q", ddl)
	}

	var declared []string
	for _, line := range strings.Split(ddl[open+1:close], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		declared = append(declared, strings.Fields(line)[0])
	}

	if len(declared) != len(cursorTableColumns) {
		t.Fatalf("bootstrapSQL() declares %v, cursorTableColumns is %v — the untrack reconcile copies by name and would drop the difference",
			declared, cursorTableColumns)
	}
	for i := range declared {
		if declared[i] != cursorTableColumns[i] {
			t.Fatalf("bootstrapSQL() column %d = %q, cursorTableColumns has %q", i, declared[i], cursorTableColumns[i])
		}
	}
}

// The scratch table's DDL must come from the same generator as the real cursor
// table's, or the only copy of the cursor during the repair silently stops
// carrying a column.
func TestScratchTableSharesTheCursorBootstrapShape(t *testing.T) {
	scratch := ignoredCursorScratch.bootstrapSQL()
	cursor := ignoredSource.bootstrapSQL()

	want := strings.Replace(cursor, ignoredSource.cursorTable, ignoredCursorUntrackTempTable, 1)
	if scratch != want {
		t.Fatalf("scratch DDL diverged from the cursor bootstrap:\n got: %s\nwant: %s", scratch, want)
	}
}

// TestAlreadyConvergedDeclinesOnALegacyTrackedCursor is the server-mode half
// of the fix. A wedged legacy database is at-latest and fully seeded — its
// dirt is the residue of a COMPLETED pass — so every predicate the fast path
// checked before this one says "converged", MigrateUp never runs, and the heal
// placed inside it would be unreachable on exactly the mode that has the most
// of these databases.
func TestAlreadyConvergedDeclinesOnALegacyTrackedCursor(t *testing.T) {
	db, mock := newMockDB(t)

	expectCurrentDatabase(mock, "testdb")
	expectNoMigrationWorkNeeded(mock)
	expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
	expectHeadTableProbe(mock, "", ignoredSource.cursorTable, true)
	expectIgnoreResolution(mock, "", ignoredSource.cursorTable, exactlyIgnored(true))
	expectSchemaTableExists(mock, ignoredCursorUntrackTempTable, false)

	converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
	if err != nil {
		t.Fatalf("alreadyConverged() error = %v", err)
	}
	if converged {
		t.Fatal("alreadyConverged() = true on a legacy tracked cursor table, want false: the locked path is the only one that heals it")
	}
	// The lock probe must never be reached — the mock is not primed for it.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestAlreadyConvergedDeclinesOnASurvivingScratchTable is the half that was
// missing. The reconcile also cleans up after an interrupted run, and in that
// state the cursor table itself reads perfectly healthy — so a fast path that
// mirrored only the legacy-shape term would return "converged" forever and the
// cleanup branch would be unreachable in server mode. The scratch is
// deliberately NOT dolt_ignore'd, so the next pull's auto-commit puts it in
// HEAD and push replicates it fleet-wide.
func TestAlreadyConvergedDeclinesOnASurvivingScratchTable(t *testing.T) {
	db, mock := newMockDB(t)

	expectCurrentDatabase(mock, "testdb")
	expectNoMigrationWorkNeeded(mock)
	expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
	expectHeadTableProbe(mock, "", ignoredSource.cursorTable, false)
	expectSchemaTableExists(mock, ignoredCursorUntrackTempTable, true)

	converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
	if err != nil {
		t.Fatalf("alreadyConverged() error = %v", err)
	}
	if converged {
		t.Fatal("alreadyConverged() = true with an interrupted reconcile to finish, want false: server mode would never clean it up")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The mirror image, and the reason the tracked probe also reads the ignore
// flag: an operator who un-ignored the cursor table has a legitimately
// tracked, legitimately converged database. Declining it would hand that fleet
// a GET_LOCK on every single invocation, forever — the exact saturation the
// fast path was built to remove.
func TestAlreadyConvergedAcceptsATrackedCursorTheOperatorUnignored(t *testing.T) {
	db, mock := newMockDB(t)

	expectCurrentDatabase(mock, "testdb")
	expectNoMigrationWorkNeeded(mock)
	expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
	expectHeadTableProbe(mock, "", ignoredSource.cursorTable, true)
	expectIgnoreResolution(mock, "", ignoredSource.cursorTable, exactlyIgnored(false))
	expectSchemaTableExists(mock, ignoredCursorUntrackTempTable, false)
	expectMigrationLockProbe(mock, "testdb", 1)

	converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
	if err != nil {
		t.Fatalf("alreadyConverged() error = %v", err)
	}
	if !converged {
		t.Fatal("alreadyConverged() = false on a database whose operator deliberately versions the cursor table, want true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The fast path's probes fail closed, and this one is no exception.
func TestAlreadyConvergedFailsClosedOnAnUnreadableHeadProbe(t *testing.T) {
	db, mock := newMockDB(t)

	expectCurrentDatabase(mock, "testdb")
	expectNoMigrationWorkNeeded(mock)
	expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
	mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLES AS OF 'HEAD'")).
		WillReturnError(sql.ErrConnDone)

	converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
	if err == nil {
		t.Fatal("alreadyConverged() error = nil, want the HEAD probe failure")
	}
	if converged {
		t.Fatal("alreadyConverged() = true on an unreadable HEAD probe, want false")
	}
}

// The lock probe's comment requires it to stay LAST — everything above it is
// the cheap steady-state question, and the reconcile gate is now part of that.
func TestAlreadyConvergedKeepsTheLockProbeLast(t *testing.T) {
	db, mock := newMockDB(t)

	expectCurrentDatabase(mock, "testdb")
	expectNoMigrationWorkNeeded(mock)
	expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
	expectHeadTableProbe(mock, "", ignoredSource.cursorTable, true)
	expectIgnoreResolution(mock, "", ignoredSource.cursorTable, exactlyIgnored(true))
	expectSchemaTableExists(mock, ignoredCursorUntrackTempTable, false)

	if _, err := alreadyConverged(context.Background(), db, "testdb", nil); err != nil {
		t.Fatalf("alreadyConverged() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (IS_FREE_LOCK must not run once a cheaper term has already declined): %v", err)
	}
}

// TestMigrateUpCountsAHealAsWork pins the signal the rest of the system reads.
// A heal-only pass applies no numbered migration, but it runs DDL and moves
// HEAD, so reporting 0 told DoltStore.Open not to rebuild the pool it had
// pinned to the pre-migration session root (be-itm5) and told `bd migrate` to
// print "already at vN" right after minting a fleet-visible commit.
func TestMigrateUpCountsAHealAsWork(t *testing.T) {
	db, mock := newMockDB(t)

	expectIgnorePatternSeedNoop(mock, LatestVersion())
	expectIgnoredCursorGate(mock, "", true, exactlyIgnored(true), false)
	expectIgnoredCursorBackup(mock, len(cursorTableColumns))
	expectIgnoredCursorUnstage(mock)
	expectIgnoredCursorUntrackCommit(mock)
	expectIgnoredCursorRestore(mock, false)
	expectNoMigrationWorkNeeded(mock)

	applied, err := MigrateUp(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	if applied != 1 {
		t.Fatalf("MigrateUp() = %d after a heal-only pass, want 1: callers key pool rebuilds and write reporting on this", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
