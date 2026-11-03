package schema

import (
	"fmt"
	"regexp"
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