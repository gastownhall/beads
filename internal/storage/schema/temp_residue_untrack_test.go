package schema

import (
	"regexp"

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
