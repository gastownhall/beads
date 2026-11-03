package schema

import (
	"bytes"
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

// expectTempListing mocks one listTempTables read of revision ('HEAD' or
// 'STAGED'), returning tables as Dolt would — LIKE's underscores are
// wildcards, so a listing may carry names the literal prefix check rejects.
func expectTempListing(mock sqlmock.Sqlmock, qualifier, revision string, tables ...string) {
	from := ""
	if qualifier != "" {
		from = " FROM " + qualifier
	}
	rows := sqlmock.NewRows([]string{"Tables_in_testdb"})
	for _, table := range tables {
		rows.AddRow(table)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLES" + from + " AS OF '" + revision + "' LIKE '__temp__%'")).
		WillReturnRows(rows)
}

// expectTempResidueGate mocks the committed-__temp__ gate on a healthy
// database: one HEAD listing that finds nothing, and nothing else.
func expectTempResidueGate(mock sqlmock.Sqlmock, qualifier string) {
	expectTempListing(mock, qualifier, "HEAD")
}

// expectOpenReconcilesNoop mocks both open-time reconciles MigrateUp runs
// before its migration pass, on a healthy database.
func expectOpenReconcilesNoop(mock sqlmock.Sqlmock) {
	expectIgnoredCursorHealNoop(mock)
	expectTempResidueGate(mock, "")
}

// captureStdLog routes the standard logger, which carries the repair's
// announcement and warnings, into a buffer for the test's duration.
func captureStdLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	saved := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(saved) })
	return &buf
}

// TestTempResidueUntrackSkipsAHealthyDatabaseInOneRead is the healthy-path
// contract every writable open pays: one HEAD listing that finds nothing, no
// write, no log line, and nothing counted.
func TestTempResidueUntrackSkipsAHealthyDatabaseInOneRead(t *testing.T) {
	db, mock := newMockDB(t)
	logged := captureStdLog(t)

	expectTempResidueGate(mock, "")

	untracked, err := untrackTempResidue(context.Background(), db)
	if err != nil {
		t.Fatalf("untrackTempResidue() error = %v, want nil", err)
	}
	if untracked {
		t.Fatal("untrackTempResidue() counted a repair on a healthy database")
	}
	if logged.Len() > 0 {
		t.Errorf("untrackTempResidue() logged on a healthy database: %q", logged.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestTempResidueGateIgnoresLikeWildcardLookalikes: LIKE '__temp__%' reads
// each underscore as any character, so the HEAD listing can carry a table
// that merely looks like an intermediate. It is no residue: no dolt_ignore
// read, no write.
func TestTempResidueGateIgnoresLikeWildcardLookalikes(t *testing.T) {
	db, mock := newMockDB(t)
	logged := captureStdLog(t)

	expectTempListing(mock, "", "HEAD", "abtempcdcounters")

	untracked, err := untrackTempResidue(context.Background(), db)
	if err != nil {
		t.Fatalf("untrackTempResidue() error = %v, want nil", err)
	}
	if untracked {
		t.Fatal("untrackTempResidue() counted a repair for a LIKE lookalike")
	}
	if logged.Len() > 0 {
		t.Errorf("untrackTempResidue() logged for a LIKE lookalike: %q", logged.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// vetoedTempResidue is an operator who versions one intermediate on purpose:
// an exact ignored=0 row, which Dolt resolves over the broader seeded
// __temp__% pattern.
var vetoedTempResidue = []doltIgnoreRow{
	{pattern: "__temp__%", ignored: true},
	{pattern: "__temp__wisp_child_counters", ignored: false},
}

// TestTempResidueUntrackRespectsAnOperatorVeto: the vetoed table is no
// residue, so its presence at HEAD neither starts a repair nor writes.
func TestTempResidueUntrackRespectsAnOperatorVeto(t *testing.T) {
	db, mock := newMockDB(t)
	logged := captureStdLog(t)

	expectTempListing(mock, "", "HEAD", "__temp__wisp_child_counters")
	expectIgnoreResolution(mock, "", "__temp__wisp_child_counters", vetoedTempResidue)

	untracked, err := untrackTempResidue(context.Background(), db)
	if err != nil {
		t.Fatalf("untrackTempResidue() error = %v, want nil", err)
	}
	if untracked {
		t.Fatal("untrackTempResidue() untracked a table the operator versions on purpose")
	}
	if logged.Len() > 0 {
		t.Errorf("untrackTempResidue() logged for a vetoed table: %q", logged.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestAlreadyConvergedAcceptsAVetoedTempTable: the same veto must not cost
// the lock-free fast path either, or every open of that database would take
// the migration lock forever.
func TestAlreadyConvergedAcceptsAVetoedTempTable(t *testing.T) {
	db, mock := newMockDB(t)

	expectCurrentDatabase(mock, "testdb")
	expectNoMigrationWorkNeeded(mock)
	expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
	expectIgnoredCursorGate(mock, "", false, nil, false)
	expectTempListing(mock, "", "HEAD", "__temp__wisp_child_counters")
	expectIgnoreResolution(mock, "", "__temp__wisp_child_counters", vetoedTempResidue)
	expectMigrationLockProbe(mock, "testdb", 1)

	converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
	if err != nil {
		t.Fatalf("alreadyConverged() error = %v", err)
	}
	if !converged {
		t.Fatal("alreadyConverged() = false on a database whose operator versions the intermediate, want true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// expectTempResidueUntrackStart mocks the reconcile up to, not including,
// DOLT_RM for one eligible table: the gate, an unstage that finds nothing
// staged, the publishing COMMIT and the staged listing.
func expectTempResidueUntrackStart(mock sqlmock.Sqlmock, table string) {
	expectTempListing(mock, "", "HEAD", table)
	expectIgnoreResolution(mock, "", table, []doltIgnoreRow{{pattern: "__temp__%", ignored: true}})
	expectIgnoredCursorUnstage(mock)
	mock.ExpectExec(regexp.QuoteMeta("COMMIT")).WillReturnResult(sqlmock.NewResult(0, 0))
	expectTempListing(mock, "", "STAGED", table)
}

func expectTempResidueRM(mock sqlmock.Sqlmock, table string) *sqlmock.ExpectedQuery {
	return mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RM('--cached', ?)")).WithArgs(table)
}

func expectTempResidueCommit(mock sqlmock.Sqlmock) *sqlmock.ExpectedQuery {
	return mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('--skip-empty', '-m', ?)")).
		WithArgs(tempResidueUntrackCommitMessage)
}

// TestTempResidueUntrackDeclinesWhenTheUnstageIsDenied: a client fenced from
// DOLT_RESET cannot scope the commit, so the reconcile declines before
// anything can move HEAD, counts nothing and lets the open go on.
func TestTempResidueUntrackDeclinesWhenTheUnstageIsDenied(t *testing.T) {
	db, mock := newMockDB(t)
	advisory := allowSwallowedAdvisory(t)
	const table = "__temp__wisp_child_counters"

	expectTempListing(mock, "", "HEAD", table)
	expectIgnoreResolution(mock, "", table, []doltIgnoreRow{{pattern: "__temp__%", ignored: true}})
	mock.ExpectQuery("(?s)SELECT s\\.table_name, s\\.staged\\s+FROM dolt_status s").
		WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}).AddRow("config", true))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET(?)")).WithArgs("config").
		WillReturnError(&mysql.MySQLError{Number: 1227, Message: "Access denied; you need the SUPER privilege"})

	untracked, err := untrackTempResidue(context.Background(), db)
	if err != nil {
		t.Fatalf("untrackTempResidue() error = %v, want nil: the repair is optional", err)
	}
	if untracked {
		t.Fatal("untrackTempResidue() counted a repair that never reached DOLT_RM")
	}
	if !strings.Contains(*advisory, "cannot clear the staging area") {
		t.Errorf("advisory = %q, want it to name the declined step", *advisory)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestTempResidueUntrackFailuresAfterTheRemovalAreCountedNotFatal covers a
// failure once DOLT_RM was issued: denied, failed, or removed but not
// committed. None may fail the open, each must count as possible mutation
// (HEAD may have moved, so the caller must not keep a stale root), and a
// later permitted open must converge.
func TestTempResidueUntrackFailuresAfterTheRemovalAreCountedNotFatal(t *testing.T) {
	const table = "__temp__wisp_child_counters"
	denied := &mysql.MySQLError{Number: 1142, Message: "DELETE command denied to user 'fenced'@'%'"}
	for _, tc := range []struct {
		name    string
		fail    func(mock sqlmock.Sqlmock)
		advises string
		warns   string
	}{
		{
			name:    "rm denied",
			fail:    func(mock sqlmock.Sqlmock) { expectTempResidueRM(mock, table).WillReturnError(denied) },
			advises: "not permitted to untrack",
		},
		{
			name: "rm failed",
			fail: func(mock sqlmock.Sqlmock) {
				expectTempResidueRM(mock, table).WillReturnError(errors.New("injected DOLT_RM failure"))
			},
			warns: "untracking committed " + table + " failed",
		},
		{
			name: "commit failed",
			fail: func(mock sqlmock.Sqlmock) {
				expectTempResidueRM(mock, table).WillReturnRows(sqlmock.NewRows([]string{"status"}))
				expectTempResidueCommit(mock).WillReturnError(errors.New("injected DOLT_COMMIT failure"))
			},
			warns: "committing the untrack of committed " + table + " failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			advisory := allowSwallowedAdvisory(t)
			logged := captureStdLog(t)

			expectTempResidueUntrackStart(mock, table)
			tc.fail(mock)
			untracked, err := untrackTempResidue(context.Background(), db)
			if err != nil {
				t.Fatalf("untrackTempResidue() error = %v, want nil: the repair is optional", err)
			}
			if !untracked {
				t.Fatal("untrackTempResidue() = false after DOLT_RM was issued; possible mutation must be counted")
			}
			if tc.advises != "" && !strings.Contains(*advisory, tc.advises) {
				t.Errorf("advisory = %q, want %q", *advisory, tc.advises)
			}
			if tc.warns != "" && !strings.Contains(logged.String(), tc.warns) {
				t.Errorf("log = %q, want %q", logged.String(), tc.warns)
			}

			// The next, permitted open finds the residue again and converges.
			expectTempResidueUntrackStart(mock, table)
			expectTempResidueRM(mock, table).WillReturnRows(sqlmock.NewRows([]string{"status"}))
			expectTempResidueCommit(mock).WillReturnRows(sqlmock.NewRows([]string{"hash"}))
			untracked, err = untrackTempResidue(context.Background(), db)
			if err != nil || !untracked {
				t.Fatalf("retry untrackTempResidue() = %v, %v; want true, nil", untracked, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}
