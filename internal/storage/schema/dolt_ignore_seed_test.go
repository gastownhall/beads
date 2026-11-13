package schema

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"
)

// A fenced wire client holds SELECT on dolt_ignore and nothing else, so the
// seed must issue ZERO statements against a database whose patterns are all
// already registered. sqlmock in ordered mode fails any Exec that was not
// expected, which is exactly the shape the fence produces at the wire: the
// old unconditional `INSERT IGNORE` died here with
//
//	command denied to user 'bd_lego_pilot_op'@'%'
//
// and took `bd init --server --external` with it.
func TestSeedDoltIgnorePatternsWritesNothingWhenAllPatternsPresent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	const mainVersion = 63
	expectIgnorePatternSeedNoop(mock, mainVersion)

	changed, err := seedDoltIgnorePatterns(context.Background(), db)
	if err != nil {
		t.Fatalf("seedDoltIgnorePatterns() error = %v, want nil (a correctly seeded database must be readable through a fence that grants no write on dolt_ignore)", err)
	}
	if changed {
		t.Fatal("seedDoltIgnorePatterns() reported changed=true on a fully seeded database")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The heal path still works: an under-seeded database (the out-of-band
// table-copy case migration 0019/0028 cannot reach) gets exactly the missing
// rows written, and nothing else.
func TestSeedDoltIgnorePatternsWritesOnlyTheMissingPatterns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	const mainVersion = 63
	candidates := expectedIgnoreSeedCandidates(mainVersion)
	if len(candidates) < 3 {
		t.Fatalf("expected at least 3 seed candidates at v%d, got %d", mainVersion, len(candidates))
	}
	present := candidates[:len(candidates)-2]
	missing := candidates[len(candidates)-2:]

	expectIgnoreSeedProbe(mock, mainVersion, present)
	for _, pattern := range missing {
		mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
			WithArgs(pattern).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}

	changed, err := seedDoltIgnorePatterns(context.Background(), db)
	if err != nil {
		t.Fatalf("seedDoltIgnorePatterns() error = %v", err)
	}
	if !changed {
		t.Fatal("seedDoltIgnorePatterns() reported changed=false after seeding missing patterns")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A genuinely fresh database has no dolt_ignore table yet — the first INSERT
// creates it. A failing probe must therefore degrade to the blind write, not
// to "everything is present". That path only ever runs as the privileged
// opener that is about to create the schema.
func TestSeedDoltIgnorePatternsSeedsWhenTheTableDoesNotExistYet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	// No schema_migrations either: the cursor probe reports it absent, so
	// version-gated patterns are skipped and only the canonical set is asserted.
	expectCursorProbe(mock, "schema_migrations", false)
	args := make([]driver.Value, 0, len(doltIgnorePatterns))
	for _, pattern := range doltIgnorePatterns {
		args = append(args, pattern)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pattern FROM dolt_ignore WHERE pattern IN (")).
		WithArgs(args...).
		WillReturnError(errors.New("table not found: dolt_ignore"))
	for _, pattern := range doltIgnorePatterns {
		mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
			WithArgs(pattern).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}

	changed, err := seedDoltIgnorePatterns(context.Background(), db)
	if err != nil {
		t.Fatalf("seedDoltIgnorePatterns() error = %v", err)
	}
	if !changed {
		t.Fatal("seedDoltIgnorePatterns() reported changed=false on a fresh database")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The presence probe asks the database, not Go, whether a pattern is a
// duplicate — so a row stored in another case under the default
// case-insensitive collation counts as present, exactly as INSERT IGNORE
// would have treated it.
func TestSeedDoltIgnorePatternsHonoursStoredCasing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	const mainVersion = 63
	candidates := expectedIgnoreSeedCandidates(mainVersion)
	shouty := make([]string, 0, len(candidates))
	for _, pattern := range candidates {
		shouty = append(shouty, upperASCII(pattern))
	}
	expectIgnoreSeedProbe(mock, mainVersion, shouty)

	changed, err := seedDoltIgnorePatterns(context.Background(), db)
	if err != nil {
		t.Fatalf("seedDoltIgnorePatterns() error = %v", err)
	}
	if changed {
		t.Fatal("seedDoltIgnorePatterns() re-seeded patterns the database already reported as present")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A partial read must degrade to the documented blind write, not to "these
// first patterns are present". If the presence probe SELECT succeeds but the
// row stream errors partway (an iteration error only rows.Err() reveals), the
// half-filled result cannot be trusted: on the very SELECT/DML-only fence this
// seed supports, misclassifying an already-registered pattern as missing draws
// a spurious INSERT IGNORE that is itself command-denied. The seed must discard
// the partial read and re-assert every candidate on the privileged opener,
// exactly as a query-level failure degrades.
func TestSeedDoltIgnorePatternsDegradesToBlindWriteOnPartialRead(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	const mainVersion = 63
	candidates := expectedIgnoreSeedCandidates(mainVersion)
	const failAt = 2
	if len(candidates) <= failAt {
		t.Fatalf("expected more than %d seed candidates at v%d, got %d", failAt, mainVersion, len(candidates))
	}

	// The probe reports the cursor, the main version, then returns rows for the
	// candidates but errors mid-stream: rows 0..failAt-1 scan cleanly, then the
	// stream fails, leaving present partially populated.
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", mainVersion)
	args := make([]driver.Value, 0, len(candidates))
	rows := sqlmock.NewRows([]string{"pattern"})
	for _, pattern := range candidates {
		args = append(args, pattern)
		rows.AddRow(pattern)
	}
	rows.RowError(failAt, errors.New("connection reset mid-read"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pattern FROM dolt_ignore WHERE pattern IN (")).
		WithArgs(args...).
		WillReturnRows(rows)

	// Every candidate must be (re)asserted, in order: the partial map is
	// discarded, so the seed blind-writes the full set. Under the unguarded
	// loop only the post-error subset would be written, and ordered sqlmock
	// would reject the first mismatched INSERT.
	for _, pattern := range candidates {
		mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
			WithArgs(pattern).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}

	changed, err := seedDoltIgnorePatterns(context.Background(), db)
	if err != nil {
		t.Fatalf("seedDoltIgnorePatterns() error = %v", err)
	}
	if !changed {
		t.Fatal("seedDoltIgnorePatterns() reported changed=false after a partial read forced a blind write")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (a partial read must discard the probe and re-assert every pattern): %v", err)
	}
}

// The ignored-series table rebuilds (ignored/0001, ignored/0002) stage their
// work through __temp__<table> intermediates before renaming to the final
// dolt_ignored names. Against a @@dolt_transaction_commit=1 server every
// non-ignored CREATE auto-commits, so without this pattern the intermediates
// materialize as real tables at HEAD and the rename halves wedge dolt_status
// (field incident: three-city shared hub, twice in one day, fleet write
// refusal behind the dirty-table guard). This binary's own pass now turns the
// setting off for its session (GH#4470); the pattern still guards binaries
// without that suspension and other auto-commit sessions on a shared server.
// The canonical set must cover the intermediates so they are born ignored;
// dolt's own matcher treats the underscores literally (patterns support only
// ? * %), so the entry matches exactly the __temp__ prefix. The engine-level
// contract is pinned by TestEmbeddedTempRebuildIntermediatesNeverReachHistory
// in embeddeddolt, which keeps the setting on through its pass
// (SetKeepTransactionCommitForTest).
func TestDoltIgnorePatternsCoverRebuildIntermediates(t *testing.T) {
	if !slices.Contains(doltIgnorePatterns, "__temp__%") {
		t.Fatal("doltIgnorePatterns is missing \"__temp__%\": ignored-series rebuild intermediates would auto-commit as tracked tables on dolt_transaction_commit=1 servers")
	}
}

func upperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// GH#6169 (bd-obnpv0): every new canonical pattern leaves existing databases
// under-seeded, and a fenced wire client (SELECT/DML-on-data-only grant) is
// denied the seed INSERT. The tests below pin when that denial may degrade to
// a warning and when it must stay fatal.

// seedDenial is the error a fenced wire client gets back from the seed INSERT.
func seedDenial() *mysql.MySQLError {
	return &mysql.MySQLError{
		Number:  1142,
		Message: "INSERT command denied to user 'bd_lego_pilot_op'@'%' for table 'dolt_ignore'",
	}
}

// refusedSeedMissing is the pattern pair the refused-seed tests leave
// unregistered. The first one is the INSERT that gets refused.
var refusedSeedMissing = []string{"__temp__%", "bd_events_journal"}

// wantSeedError is the error text seedDoltIgnorePatterns returns for cause on
// pattern, unchanged by MigrateUp.
func wantSeedError(pattern string, cause error) string {
	return fmt.Sprintf("seeding dolt_ignore pattern %q: %v", pattern, cause)
}

// expectSeedInsertFailure mocks the seed on a database missing
// refusedSeedMissing: the presence probe, then INSERTs that succeed for the
// first okInserts missing patterns and fail with cause on the next one.
// Nothing after the failing INSERT is expected: the seed stops there.
func expectSeedInsertFailure(mock sqlmock.Sqlmock, mainVersion, okInserts int, cause error) {
	var present []string
	for _, pattern := range expectedIgnoreSeedCandidates(mainVersion) {
		if !slices.Contains(refusedSeedMissing, pattern) {
			present = append(present, pattern)
		}
	}
	expectIgnoreSeedProbe(mock, mainVersion, present)
	for _, pattern := range refusedSeedMissing[:okInserts] {
		mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
			WithArgs(pattern).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
		WithArgs(refusedSeedMissing[okInserts]).
		WillReturnError(cause)
}

// expectNoMigrationWork mocks migrationWorkNeeded on a fully current
// database (both cursors at latest, hash columns present, custom tables
// populated), the same sequence TestMigrateUpSkipsSeedCommitWhenNothingChanged
// primes.
func expectNoMigrationWork(mock sqlmock.Sqlmock) {
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	expectIgnoredSentinelProbes(mock, true)
	expectContentHashColumnExists(mock)
	expectContentHashColumnExists(mock)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_types", "count", 1)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_statuses", "count", 1)
}

const newTablesQuery = "SELECT table_name FROM dolt_status WHERE status = 'new table' ORDER BY table_name"

// expectNewTables mocks the refused seed's new-table guard.
func expectNewTables(mock sqlmock.Sqlmock, tables ...string) {
	rows := sqlmock.NewRows([]string{"table_name"})
	for _, table := range tables {
		rows.AddRow(table)
	}
	mock.ExpectQuery(regexp.QuoteMeta(newTablesQuery)).WillReturnRows(rows)
}

// captureDoltIgnoreSeedNotices redirects the refused-seed warning for one test.
func captureDoltIgnoreSeedNotices(t *testing.T) *bytes.Buffer {
	t.Helper()
	var notices bytes.Buffer
	orig := doltIgnoreSeedNoticeOut
	doltIgnoreSeedNoticeOut = &notices
	t.Cleanup(func() { doltIgnoreSeedNoticeOut = orig })
	return &notices
}

// newSeedMock is a fresh ordered sqlmock closed when the test ends.
func newSeedMock(t *testing.T) (DBConn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// AC-1: an otherwise-current database whose only gap is a canonical pattern
// the opener may not write opens with one warning, and nothing after the
// refused INSERT writes: no cursor repair, no scoped seed commit, no pass.
func TestMigrateUpDegradesRefusedSeedWhenConverged(t *testing.T) {
	denials := map[string]error{
		"1044 db access denied":       &mysql.MySQLError{Number: 1044, Message: "Access denied for user 'op'@'%' to database 'beads'"},
		"1045 access denied":          &mysql.MySQLError{Number: 1045, Message: "Access denied for user 'op'@'%'"},
		"1142 table command denied":   seedDenial(),
		"1143 column command denied":  &mysql.MySQLError{Number: 1143, Message: "INSERT command denied to user 'op'@'%' for column 'pattern' in table 'dolt_ignore'"},
		"1227 specific access denied": &mysql.MySQLError{Number: 1227, Message: "Access denied; you need (at least one of) the SUPER privilege(s) for this operation"},
		"untyped command denied":      errors.New("command denied to user 'bd_lego_pilot_op'@'%'"),
	}
	for name, denial := range denials {
		t.Run(name, func(t *testing.T) {
			failOnSwallowedAdvisory(t)
			notices := captureDoltIgnoreSeedNotices(t)
			db, mock := newSeedMock(t)

			// Two patterns are missing; the first refusal stops the seed, so
			// only one INSERT is attempted and only one warning printed.
			expectSeedInsertFailure(mock, LatestVersion(), 0, denial)
			expectNoMigrationWork(mock)
			expectNewTables(mock)

			applied, err := MigrateUp(context.Background(), db)
			if err != nil {
				t.Fatalf("MigrateUp() error = %v, want nil (a refused seed on a current schema must degrade to a warning)", err)
			}
			if applied != 0 {
				t.Fatalf("MigrateUp() applied = %d, want 0", applied)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
			assertOneRefusedSeedWarning(t, notices.String(), wantSeedError(refusedSeedMissing[0], denial))
		})
	}

	// An INSERT that succeeded before the refusal leaves changed=true inside
	// the seed. The degraded open still stops: no scoped seed commit (ordered
	// sqlmock rejects the DOLT_ADD), no claim the seed converged.
	t.Run("earlier insert succeeded", func(t *testing.T) {
		failOnSwallowedAdvisory(t)
		notices := captureDoltIgnoreSeedNotices(t)
		db, mock := newSeedMock(t)

		expectSeedInsertFailure(mock, LatestVersion(), 1, seedDenial())
		expectNoMigrationWork(mock)
		expectNewTables(mock)

		applied, err := MigrateUp(context.Background(), db)
		if err != nil {
			t.Fatalf("MigrateUp() error = %v, want nil", err)
		}
		if applied != 0 {
			t.Fatalf("MigrateUp() applied = %d, want 0", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
		assertOneRefusedSeedWarning(t, notices.String(), wantSeedError(refusedSeedMissing[1], seedDenial()))
	})
}

func assertOneRefusedSeedWarning(t *testing.T, notices, seedErr string) {
	t.Helper()
	if strings.Count(notices, "\n") != 1 || !strings.HasSuffix(notices, "\n") {
		t.Fatalf("warning output = %q, want exactly one line", notices)
	}
	for _, want := range []string{
		seedErr,
		"continuing without seeding dolt_ignore on the current schema",
		"open this database once with a privileged bd that can write dolt_ignore to heal the missing patterns",
	} {
		if !strings.Contains(notices, want) {
			t.Fatalf("warning = %q, want it to contain %q", notices, want)
		}
	}
}

// AC-2: pending migration work cannot run without the patterns, so the
// refusal stays the original seed error. Nothing past migrationWorkNeeded
// runs: no cursor repair, no new-table read, no migration, no warning.
func TestMigrateUpRefusedSeedFailsWhenWorkPending(t *testing.T) {
	const latestMinusOne = 1
	cases := map[string]struct {
		mainVersion int
		work        func(sqlmock.Sqlmock)
	}{
		"main migrations pending": {
			mainVersion: LatestVersion() - latestMinusOne,
			work: func(mock sqlmock.Sqlmock) {
				expectCursorProbe(mock, "schema_migrations", true)
				expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion()-latestMinusOne)
			},
		},
		"ignored migrations pending": {
			mainVersion: LatestVersion(),
			work: func(mock sqlmock.Sqlmock) {
				expectCursorProbe(mock, "schema_migrations", true)
				expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
				expectCursorProbe(mock, "ignored_schema_migrations", false)
			},
		},
		"content_hash column missing": {
			mainVersion: LatestVersion(),
			work: func(mock sqlmock.Sqlmock) {
				expectCursorProbe(mock, "schema_migrations", true)
				expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
				expectCursorProbe(mock, "ignored_schema_migrations", true)
				expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
				expectIgnoredSentinelProbes(mock, true)
				mock.ExpectQuery(`SHOW COLUMNS FROM \w+ LIKE 'content_hash'`).WillReturnRows(showColumnsRows())
			},
		},
		"custom types backfill pending": {
			mainVersion: LatestVersion(),
			work: func(mock sqlmock.Sqlmock) {
				expectCursorProbe(mock, "schema_migrations", true)
				expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
				expectCursorProbe(mock, "ignored_schema_migrations", true)
				expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
				expectIgnoredSentinelProbes(mock, true)
				expectContentHashColumnExists(mock)
				expectContentHashColumnExists(mock)
				expectScalar(mock, "SELECT COUNT(*) FROM custom_types", "count", 0)
				expectScalar(mock, "SELECT `value` FROM config WHERE `key` = 'types.custom'", "value", "spike")
				expectScalar(mock, "SELECT COUNT(*) FROM custom_statuses", "count", 1)
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			failOnSwallowedAdvisory(t)
			notices := captureDoltIgnoreSeedNotices(t)
			db, mock := newSeedMock(t)
			denial := seedDenial()

			expectSeedInsertFailure(mock, tc.mainVersion, 0, denial)
			tc.work(mock)

			_, err := MigrateUp(context.Background(), db)
			if err == nil {
				t.Fatal("MigrateUp() error = nil, want the refused seed while migration work is pending")
			}
			if got, want := err.Error(), wantSeedError(refusedSeedMissing[0], denial); got != want {
				t.Fatalf("MigrateUp() error = %q, want the original seed error %q", got, want)
			}
			var mysqlErr *mysql.MySQLError
			if !errors.As(err, &mysqlErr) || mysqlErr != denial {
				t.Fatalf("MigrateUp() error = %v, want it to wrap the seed denial", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations (the work probe must run before refusing): %v", err)
			}
			if notices.Len() != 0 {
				t.Fatalf("refused open printed %q, want no continuation warning", notices.String())
			}
		})
	}

	// A work probe that cannot answer is no licence to continue: the open
	// fails with both causes preserved.
	t.Run("work probe fails", func(t *testing.T) {
		failOnSwallowedAdvisory(t)
		notices := captureDoltIgnoreSeedNotices(t)
		db, mock := newSeedMock(t)
		denial := seedDenial()
		probeErr := errors.New("connection reset by peer")

		expectSeedInsertFailure(mock, LatestVersion(), 0, denial)
		expectCursorProbe(mock, "schema_migrations", true)
		expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
		expectCursorProbe(mock, "ignored_schema_migrations", true)
		expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
		expectIgnoredSentinelProbes(mock, true)
		mock.ExpectQuery(`SHOW COLUMNS FROM \w+ LIKE 'content_hash'`).WillReturnError(probeErr)

		_, err := MigrateUp(context.Background(), db)
		if err == nil {
			t.Fatal("MigrateUp() error = nil, want a failure when the work probe fails")
		}
		var mysqlErr *mysql.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr != denial {
			t.Fatalf("MigrateUp() error = %v, want it to wrap the seed denial", err)
		}
		if !errors.Is(err, probeErr) {
			t.Fatalf("MigrateUp() error = %v, want it to wrap the work-probe failure", err)
		}
		if !strings.Contains(err.Error(), "checking schema migration work") {
			t.Fatalf("MigrateUp() error = %q, want it to say the migration-work check failed", err.Error())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
		if notices.Len() != 0 {
			t.Fatalf("refused open printed %q, want no continuation warning", notices.String())
		}
	})
}
