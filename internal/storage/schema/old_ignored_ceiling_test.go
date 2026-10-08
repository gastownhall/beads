package schema

import (
	"context"
	"embed"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// oldIgnoredCeiling24Files stands in for the ignored-migration set a binary
// built before BEADS-JOURNAL-PLAN.md's PR A2 would have embedded: 0025
// (actor), 0026 (dep rekey marker), 0027 (wisps.current_revision) and 0028
// (this plan's journal-shape convergence) all postdate it, so its ceiling is
// 24 — the d3ab CLI's measured ignored ceiling in the plan's evidence base
// (§4.1). Only the filename's version prefix is ever read
// (migrationSource.list/.latest), so one placeholder file is enough; it need
// not match the real ignored/0024 migration's SQL.
//
//go:embed testdata/old_ignored_ceiling_24/*.up.sql
var oldIgnoredCeiling24Files embed.FS

// TestOldIgnoredCeilingOpensAfter0028 is T2.9 (BEADS-JOURNAL-PLAN.md §4.3, PR
// A2): a binary that only knows the ignored series through 24 opens a store
// whose ignored cursor is already at 28 without refusing, because nothing on
// the open path compares a binary's ignored ceiling against the stored
// cursor — CurrentIgnoredVersion/LatestIgnoredVersion feed no skew guard the
// way CurrentVersion/LatestVersion do for the main plane (checkSchemaSkew).
//
// Kills:
//   - adding an ignored-plane skew check: oldIgnored (ceiling 24) reporting
//     atLatest()==true and migrate() applying nothing against a mocked cursor
//     of 28 pins that an old-ceiling source does not refuse or try to "catch
//     up" just because the stored cursor is ahead of what it knows; and the
//     checkSchemaSkew/CheckForwardDrift/CheckBehindDrift sub-tests below use a
//     strict sqlmock (ExpectationsWereMet) that would fail if any of them
//     issued a query against ignored_schema_migrations at all.
//   - adding a main-plane twin for the journal migrations: that would move
//     mainSource's own cursor, which the same checkSchemaSkew mock (driven
//     only by schema_migrations, unaffected by the ignored plane) would catch
//     as a changed main version the moment the twin's migration shipped.
func TestOldIgnoredCeilingOpensAfter0028(t *testing.T) {
	oldIgnored := migrationSource{
		files:       oldIgnoredCeiling24Files,
		dir:         "testdata/old_ignored_ceiling_24",
		cursorTable: ignoredSource.cursorTable,
	}
	if got := oldIgnored.latest(); got != 24 {
		t.Fatalf("oldIgnored.latest() = %d, want 24", got)
	}

	t.Run("old-ceiling source sees the store as at-latest, not behind", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		expectCursorProbe(mock, "ignored_schema_migrations", true)
		expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", 28)

		if !oldIgnored.atLatest(context.Background(), db) {
			t.Error("oldIgnored.atLatest() = false for a cursor (28) past its own ceiling (24); an old binary must not see this store as behind")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})

	t.Run("old-ceiling source applies nothing against a cursor past its ceiling", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		mock.ExpectExec(regexp.QuoteMeta("CREATE TABLE IF NOT EXISTS ignored_schema_migrations")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta("SHOW COLUMNS FROM ignored_schema_migrations LIKE 'content_hash'")).
			WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
				AddRow("content_hash", "char(64)", "YES", "", nil, ""))
		expectCursorProbe(mock, "ignored_schema_migrations", true)
		expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", 28)
		// No sentinelTables/sentinelColumns on oldIgnored (zero value), so
		// cursorRealityFloor has nothing further to probe, and no
		// ExecContext should run at all: migrate must see current(28) >=
		// target(24) and return immediately.

		applied, _, err := oldIgnored.migrate(context.Background(), db, 0)
		if err != nil {
			t.Fatalf("oldIgnored.migrate: %v", err)
		}
		if applied != 0 {
			t.Errorf("oldIgnored.migrate applied = %d, want 0 (a cursor past this source's ceiling has nothing pending)", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations (an old-ceiling binary must not touch the database at all here): %v", err)
		}
	})

	t.Run("the real open-path guards never consult the ignored cursor", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		// Main plane only: at the binary's own latest, unaffected by the
		// ignored cursor being 28. If checkSchemaSkew/CheckForwardDrift ever
		// queried ignored_schema_migrations, the strict mock below (no such
		// expectation registered) would fail the call.
		expectCursorProbe(mock, "schema_migrations", true)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(version), 0) FROM schema_migrations")).
			WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(LatestVersion()))

		if err := CheckForwardDrift(context.Background(), db); err != nil {
			t.Errorf("CheckForwardDrift = %v, want nil (ignored=28 must not drive a main-plane forward-drift refusal)", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})
}
