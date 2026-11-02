package schema

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestMigrateUpDirtyTablesErrorUnversionedAtCursorZero pins the main-source
// pending-migration guard's classification (gastownhall/beads#6089): a refusal raised against
// a database with NO recorded main migration history — the schema_migrations
// table absent, or present but empty — carries Unversioned=true, and the
// error text is unchanged from the pre-classification refusal. A nonzero
// recorded cursor keeps Unversioned=false
// (TestMigrateUpReturnsDirtyTablesErrorForPreExistingDirtyTable), and the
// dependency-rekey refusal keeps the false zero value
// (TestRekeyDependencyIDsRefusesToWriteIntoDirtyTable).
// TestDirtyTablesErrorUnversioned pins the false default and the unchanged
// error text for every constructor.
//
// The 0-cursor shapes are the legacy-SDK residue an explicit `bd init` may
// adopt: a v1.0.0-era seeder (Gas Town's EnsureRigIssuePrefix class) writes
// config and creates routes/interactions without any main migration ever
// having run, so the first modern binary to open the database refuses at
// this guard with Unversioned=true. bd init's adoption path reads exactly
// this field to tell that shape apart from an ordinary versioned dirty
// database, which every caller must keep refusing.
func TestMigrateUpDirtyTablesErrorUnversionedAtCursorZero(t *testing.T) {
	failOnSwallowedAdvisory(t)

	// The dirty table is `config`, standing in for the
	// config/interactions/routes residue of the reporter's fixture: any
	// dirty table a pending migration touches reaches the same refusal.
	const dirtyTable = "config"

	for _, tc := range []struct {
		name          string
		cursorTable   bool
		cursorVersion int
	}{
		{name: "absent main cursor table", cursorTable: false},
		{name: "empty main cursor table", cursorTable: true, cursorVersion: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			// checkMigrateConsent: package-wide consent (migrate_consent_test.go's
			// TestMain), so no version read happens there. The seed then reads the
			// main cursor to qualify version-gated dolt_ignore patterns; on a
			// database with no recorded history that is version 0, and the seed
			// finds nothing registered, so every candidate is inserted.
			if tc.cursorTable {
				expectIgnorePatternSeed(mock, 0)
			} else {
				expectUnversionedSeedProbeAbsentCursor(mock)
			}
			// #4356: the open-time untrack reconcile is two reads and no
			// writes on this database.
			expectIgnoredCursorHealNoop(mock)
			// migrationWorkNeeded: cursor 0 is behind LatestVersion(), so the
			// || short-circuits before the ignored-source probes.
			expectUnversionedCursorRead(mock, tc.cursorTable)
			// dirtyBeforeAll: the residue table is dirty in the working set.
			expectDirtyDoltStatusRow(mock, dirtyTable, false)
			// The seed changed rows, so it is committed scoped+labeled before
			// the guards run; the residue table was never staged, so no
			// DOLT_RESET is issued.
			mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('dolt_ignore')")).
				WillReturnRows(sqlmock.NewRows([]string{"status"}))
			mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', 'schema: seed dolt_ignore patterns', '--skip-empty')")).
				WillReturnRows(sqlmock.NewRows([]string{"hash"}))
			// committableDirtyTables: same dirty state, re-read.
			expectDirtyDoltStatusRow(mock, dirtyTable, false)
			// The pre-pass main cursor (the classification input): 0.
			expectUnversionedCursorRead(mock, tc.cursorTable)
			// auxRekeyExemptTables: no ignored cursor table and no local_metadata,
			// so no aux table is exempted.
			expectCursorProbe(mock, "ignored_schema_migrations", false)
			for range auxRekeyPasses {
				mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
			}
			// pendingMigrationDirtyTables re-reads the cursor, then the
			// pending migrations that create/alter `config` mark it touched.
			expectUnversionedCursorRead(mock, tc.cursorTable)

			_, err = MigrateUp(context.Background(), db)
			if err == nil {
				t.Fatal("MigrateUp() error = nil, want *DirtyTablesError")
			}
			var dirtyErr *DirtyTablesError
			if !errors.As(err, &dirtyErr) {
				t.Fatalf("MigrateUp() error = %v (%T), want *DirtyTablesError", err, err)
			}
			if len(dirtyErr.Tables) != 1 || dirtyErr.Tables[0] != dirtyTable {
				t.Fatalf("DirtyTablesError.Tables = %v, want [%s]", dirtyErr.Tables, dirtyTable)
			}
			if !dirtyErr.Unversioned {
				t.Fatal("DirtyTablesError.Unversioned = false for a main cursor of 0, want true")
			}
			// The classification is additive: the refusal text — what the
			// reporter of #6089 saw, and what the working-set-reconcile
			// recovery paths match on — must not change.
			want := "pending schema migrations alter pre-existing dirty tables: " + dirtyTable +
				"; run 'bd dolt commit' to commit the working set at the current schema, then re-run the migration (gastownhall/beads#4566)"
			if dirtyErr.Error() != want {
				t.Fatalf("DirtyTablesError.Error() = %q, want %q", dirtyErr.Error(), want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

// expectUnversionedCursorRead mocks one migrationSource.currentVersion read
// on the unversioned shapes: the existence probe, plus the MAX(version) read
// only when the cursor table exists (an absent table reports version 0
// without ever selecting from it — be-bv7x's session-poisoning guard).
func expectUnversionedCursorRead(mock sqlmock.Sqlmock, cursorTable bool) {
	if !cursorTable {
		expectCursorProbe(mock, "schema_migrations", false)
		return
	}
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", 0)
}

// expectUnversionedSeedProbeAbsentCursor is expectIgnoreSeedProbe for a
// database whose schema_migrations table does not exist: the seed's cursor
// read stops at the existence probe (version 0), then the single presence
// SELECT runs against dolt_ignore. It mirrors the already-present variant's
// argument list exactly, so the two cannot drift apart.
func expectUnversionedSeedProbeAbsentCursor(mock sqlmock.Sqlmock) {
	expectCursorProbe(mock, "schema_migrations", false)
	candidates := expectedIgnoreSeedCandidates(0)
	args := make([]driver.Value, 0, len(candidates))
	for _, pattern := range candidates {
		args = append(args, pattern)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pattern FROM dolt_ignore WHERE pattern IN (")).
		WithArgs(args...).
		WillReturnRows(sqlmock.NewRows([]string{"pattern"}))
	for _, pattern := range candidates {
		mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
			WithArgs(pattern).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
}
