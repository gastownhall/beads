package schema

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// expectTempResidueNoop mocks the __temp__ residue gate on a healthy
// database, as both MigrateUp's reconcile and alreadyConverged call it: one
// empty HEAD listing, no dolt_ignore resolution reads, no writes.
//
// qualifier is the identifier-quoted database name when a selector put the
// session on the database, empty when it was already there.
func expectTempResidueNoop(mock sqlmock.Sqlmock, qualifier string) {
	from := ""
	if qualifier != "" {
		from = " FROM " + qualifier
	}
	mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLES" + from + " AS OF 'HEAD' LIKE '__temp__%'")).
		WillReturnRows(sqlmock.NewRows([]string{"Tables_in_testdb"}))
}

// expectTempResidueHeadListing mocks the gate's HEAD listing with the given
// committed residue names (which the LIKE form returns ahead of the Go-side
// literal-prefix check).
func expectTempResidueHeadListing(mock sqlmock.Sqlmock, qualifier string, tables ...string) {
	from := ""
	if qualifier != "" {
		from = " FROM " + qualifier
	}
	rows := sqlmock.NewRows([]string{"Tables_in_testdb"})
	for _, table := range tables {
		rows.AddRow(table)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLES" + from + " AS OF 'HEAD' LIKE '__temp__%'")).
		WillReturnRows(rows)
}

// captureTempResidueAdvisory swaps the advisory hook for one that records
// into the returned string, restoring the original on cleanup.
func captureTempResidueAdvisory(t *testing.T) *string {
	t.Helper()
	logged := ""
	saved := tempResidueAdvisory
	tempResidueAdvisory = func(format string, args ...any) {
		logged += fmt.Sprintf(format, args...)
	}
	t.Cleanup(func() { tempResidueAdvisory = saved })
	return &logged
}

// failOnSwallowedTempResidueAdvisory is failOnSwallowedAdvisory for the
// residue reconcile's own advisory hook: in ordered sqlmock a statement
// nobody primed comes back as "call was not expected", which the advisory
// path demotes to a debug line and swallows — so a test missing a
// reconcile expectation would pass while silently exercising the
// decline path instead of the repair it claims to cover.
func failOnSwallowedTempResidueAdvisory(t *testing.T) {
	t.Helper()
	saved := tempResidueAdvisory
	tempResidueAdvisory = func(format string, args ...any) {
		t.Helper()
		t.Errorf("the __temp__ residue reconcile swallowed an advisory failure (an unprimed statement?): "+
			strings.TrimRight(format, "\n"), args...)
	}
	t.Cleanup(func() { tempResidueAdvisory = saved })
}

// expectTempResidueStagedListing mocks the staged-root listing that narrows
// the residue to names DOLT_RM('--cached') can actually remove.
func expectTempResidueStagedListing(mock sqlmock.Sqlmock, tables ...string) {
	rows := sqlmock.NewRows([]string{"Tables_in_testdb"})
	for _, table := range tables {
		rows.AddRow(table)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLES AS OF 'STAGED'")).
		WillReturnRows(rows)
}

// The #4356/#7073 interplay: the cursor heal's scratch table
// (__temp__ignored_schema_migrations_untrack) got committed at HEAD by some
// older blanket commit — the exact straggler shape dropIgnoredCursorScratch's
// comment describes — while the working set no longer carries it, so the
// heal's gate finds nothing and declines. The residue reconcile is then the
// only repair that can reach it, and it must reach it with a CACHED removal
// only: a DROP TABLE (or a forced DOLT_ADD staging a delete) would destroy
// the scratch's working rows, which are the cursor's only copy while a
// repair that crashed between the drop and its commit is being resumed.
// Ordered sqlmock fails any statement nobody primed and
// failOnSwallowedTempResidueAdvisory makes the resulting advisory decline
// fatal, so the expectations below pin the whole repair to the two call
// procedures and nothing else.
func TestTempResidueCachedUntrackPreservesCursorScratchRows(t *testing.T) {
	db, mock := newMockDB(t)
	failOnSwallowedTempResidueAdvisory(t)

	// The heal's read-only gate: cursor table not tracked at HEAD, no
	// stray scratch in the working set. The heal declines without touching
	// anything — the shape is invisible to it.
	expectIgnoredCursorGate(mock, "", false, nil, false)

	// The residue gate sees the scratch at HEAD and the seeded "__temp__%"
	// pattern genuinely covers the name.
	expectTempResidueHeadListing(mock, "", ignoredCursorUntrackTempTable)
	expectIgnoreResolution(mock, "", ignoredCursorUntrackTempTable,
		[]doltIgnoreRow{{pattern: tempResiduePrefix + "%", ignored: true}})

	// The cached untrack, statement by statement. No DROP TABLE, no
	// DOLT_ADD: --cached never reads the working set, so whatever rows the
	// scratch still holds survive verbatim for the heal's resume contract.
	expectIgnoredCursorUnstage(mock)
	mock.ExpectExec(regexp.QuoteMeta("COMMIT")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectTempResidueStagedListing(mock, ignoredCursorUntrackTempTable)
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RM('--cached', ?)")).
		WithArgs(ignoredCursorUntrackTempTable).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('--skip-empty', '-m', ?)")).
		WithArgs(tempResidueUntrackCommitSubject + ": " + ignoredCursorUntrackTempTable).
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))

	ctx := context.Background()
	healed, err := healTrackedIgnoredCursorTable(ctx, db)
	if err != nil {
		t.Fatalf("healTrackedIgnoredCursorTable() error = %v, want nil", err)
	}
	if healed {
		t.Fatal("healTrackedIgnoredCursorTable() reported a heal on a database whose gate it cannot see")
	}

	fixed, err := reconcileTempResidue(ctx, db)
	if err != nil {
		t.Fatalf("reconcileTempResidue() error = %v, want nil", err)
	}
	if !fixed {
		t.Fatal("reconcileTempResidue() reported no repair on a HEAD-tracked scratch")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
