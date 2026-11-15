package schema

import (
	"context"
	"errors"
	"maps"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// expectHeadContentHashProbes mocks the shape probes ReadMigrationContentHashes
// issues before its HEAD read when the cursor is fully present: the be-bv7x
// cursor-existence probe, then the content_hash column.
func expectHeadContentHashProbes(mock sqlmock.Sqlmock) {
	expectCursorProbe(mock, "schema_migrations", true)
	mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations LIKE 'content_hash'`).
		WillReturnRows(showColumnsRows("content_hash"))
}

// TestReadMigrationContentHashesAtHeadIssuesNoFailingStatement is the be-bv7x
// rule for the HEAD read (bd-21dc1). `bd doctor` reaches it on a database it
// never migrates, where a SELECT against the absent cursor table, or against a
// pre-#4259 cursor without content_hash, fails and pins the pooled Dolt
// session to its pre-statement catalog. The strict mock admits only the shape
// probes, and the error must still read as nothing-to-compare to the callers.
func TestReadMigrationContentHashesAtHeadIssuesNoFailingStatement(t *testing.T) {
	tests := []struct {
		name   string
		expect func(sqlmock.Sqlmock)
	}{
		{
			name: "no usable database",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = DATABASE()")).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
			},
		},
		{
			name: "cursor table absent",
			expect: func(mock sqlmock.Sqlmock) {
				expectCursorProbe(mock, "schema_migrations", false)
			},
		},
		{
			name: "content_hash column absent",
			expect: func(mock sqlmock.Sqlmock) {
				expectCursorProbe(mock, "schema_migrations", true)
				mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations LIKE 'content_hash'`).
					WillReturnRows(showColumnsRows())
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			tc.expect(mock)

			_, err = ReadMigrationContentHashes(context.Background(), db, "")
			if err == nil || !MissingMigrationObjectErr(err) {
				t.Fatalf("ReadMigrationContentHashes(HEAD) error = %v, want a missing-object error", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestReadMigrationContentHashesAtHeadPassesProbeErrorsThrough covers a probe
// that fails outright. That is not evidence the cursor is missing, so the
// error must reach the callers as is (the doctor warns, the smart gate routes
// undetermined) instead of reading as nothing-to-compare, and no SELECT may
// follow it.
func TestReadMigrationContentHashesAtHeadPassesProbeErrorsThrough(t *testing.T) {
	probeErr := errors.New("lock wait timeout exceeded")
	tests := []struct {
		name   string
		expect func(sqlmock.Sqlmock)
	}{
		{
			name: "database probe fails",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = DATABASE()")).
					WillReturnError(probeErr)
			},
		},
		{
			name: "table probe fails",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = DATABASE()")).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				mock.ExpectQuery(`^SHOW TABLES$`).WillReturnError(probeErr)
			},
		},
		{
			name: "content_hash probe fails",
			expect: func(mock sqlmock.Sqlmock) {
				expectCursorProbe(mock, "schema_migrations", true)
				mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations LIKE 'content_hash'`).
					WillReturnError(probeErr)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			tc.expect(mock)

			_, err = ReadMigrationContentHashes(context.Background(), db, "")
			if !errors.Is(err, probeErr) {
				t.Fatalf("ReadMigrationContentHashes(HEAD) error = %v, want the probe's error %v", err, probeErr)
			}
			if MissingMigrationObjectErr(err) {
				t.Fatalf("ReadMigrationContentHashes(HEAD) error = %v reads as a missing object; a failed probe is not one", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestReadMigrationContentHashesAtHeadReadsAPresentCursor covers the other
// half: once both probes find the shape, the HEAD read runs and still drops
// NULL and empty hashes.
func TestReadMigrationContentHashesAtHeadReadsAPresentCursor(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	expectHeadContentHashProbes(mock)
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).
			AddRow(1, "a").AddRow(2, nil).AddRow(3, ""))

	got, err := ReadMigrationContentHashes(context.Background(), db, "")
	if err != nil {
		t.Fatalf("ReadMigrationContentHashes(HEAD): %v", err)
	}
	if want := map[int]string{1: "a"}; !maps.Equal(got, want) {
		t.Errorf("ReadMigrationContentHashes(HEAD) = %v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
