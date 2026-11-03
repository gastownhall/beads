package schema

import (
	"bytes"
	"context"
	"log"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
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
