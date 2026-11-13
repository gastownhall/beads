package schema

import (
	"bytes"
	"context"
	"database/sql"
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

// --- bd-obnpv0 / gastownhall/beads#6169: a refused dolt_ignore seed must not
// take a fenced wire client's open down. The tests below pin the approved
// design: the seed itself still returns the refusal (the eligibility
// decision must not lose it), and migrateUpConsented degrades only on a
// database that has no migration work pending and no new table in its
// working set.

// refusedIgnoreSeedInsert is the wire-shaped privilege refusal the mocks
// return from the seed's INSERT: exactly what a box-fenced operator grant
// (SELECT/DML-on-data-only) produces against dolt_ignore.
func refusedIgnoreSeedInsert() *mysql.MySQLError {
	return &mysql.MySQLError{
		Number:  1142,
		Message: "INSERT command denied to user 'bd_lego_pilot_op'@'%' for table 'dolt_ignore'",
	}
}

// wrappedIgnoreSeedErr is the failure text seedDoltIgnorePatterns returns for
// a denied INSERT of pattern: the wrapped, pattern-specific error callers
// must keep seeing unchanged on every fatal arm.
func wrappedIgnoreSeedErr(pattern string, cause error) string {
	return fmt.Sprintf("seeding dolt_ignore pattern %q: %v", pattern, cause)
}

// captureIgnoreSeedNotice redirects the degraded-seed warning to a buffer
// for the duration of one test and returns it, so the tests can assert the
// open's one visible warning without a public capture hook.
func captureIgnoreSeedNotice(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	saved := doltIgnoreSeedNoticeOut
	doltIgnoreSeedNoticeOut = &buf
	t.Cleanup(func() { doltIgnoreSeedNoticeOut = saved })
	return &buf
}

// expectRefusedIgnoreSeed mocks the seed on an under-seeded database: the
// presence probe reports every candidate except the missing ones, and the
// FIRST INSERT for a missing pattern fails with seedErr — the exact wire
// shape a fence produces. The seed stops at its first error, so exactly one
// INSERT is expected however many patterns are missing.
func expectRefusedIgnoreSeed(mock sqlmock.Sqlmock, mainVersion int, seedErr error, missing ...string) {
	candidates := expectedIgnoreSeedCandidates(mainVersion)
	missingSet := make(map[string]bool, len(missing))
	for _, pattern := range missing {
		missingSet[pattern] = true
	}
	present := make([]string, 0, len(candidates))
	first := ""
	for _, pattern := range candidates {
		if missingSet[pattern] {
			if first == "" {
				first = pattern
			}
			continue
		}
		present = append(present, pattern)
	}
	if first == "" {
		panic("expectRefusedIgnoreSeed: none of the missing patterns is a seed candidate")
	}
	expectIgnoreSeedProbe(mock, mainVersion, present)
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
		WithArgs(first).
		WillReturnError(seedErr)
}

// expectNoMigrationWork mocks migrationWorkNeeded's no-work answer, the
// shape TestMigrateUpSkipsSeedCommitWhenNothingChanged pins: both cursors
// at-latest (the ignored lane's sentinel probes included), both content_hash
// columns present, no custom-types/statuses backfill pending.
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

// expectNoNewTables mocks the capture guard's clean answer: dolt_status
// reports no table in "new table" state.
func expectNoNewTables(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT table_name FROM dolt_status WHERE status = 'new table' ORDER BY table_name")).
		WillReturnRows(sqlmock.NewRows([]string{"table_name"}))
}

// TestMigrateUpDegradesRefusedSeedWhenConverged pins AC-1 of bd-obnpv0: a
// fenced wire client whose dolt_ignore INSERT is command-denied still opens a
// database that has no migration work pending and no new table in its
// working set. MigrateUp returns (0, nil), prints exactly one warning naming
// the refused pattern and the privileged-heal remedy, and issues no further
// statement — ordered sqlmock fails any unprimed call, so the
// expectations-were-met check proves the "no subsequent write or commit"
// half. Every code dberrors.IsAccessDenied classifies must degrade, not just
// the canonical 1142 wire shape.
func TestMigrateUpDegradesRefusedSeedWhenConverged(t *testing.T) {
	denials := []struct {
		name string
		err  error
	}{
		{"1142 table access", refusedIgnoreSeedInsert()},
		{"1044 database access", &mysql.MySQLError{Number: 1044, Message: "access denied to database 'beads' for user 'bd_lego_pilot_op'"}},
		{"1045 access", &mysql.MySQLError{Number: 1045, Message: "access denied for user 'bd_lego_pilot_op'@'%' (using password: YES)"}},
		{"1143 column access", &mysql.MySQLError{Number: 1143, Message: "INSERT command denied to user 'bd_lego_pilot_op'@'%' for column 'pattern' in table 'beads'.'dolt_ignore'"}},
		{"1227 specific access", &mysql.MySQLError{Number: 1227, Message: "Access denied; you need (at least one of) the INSERT privilege(s) for this operation"}},
		{"plain-string denial", errors.New("Error 1142: INSERT command denied to user 'bd_lego_pilot_op'@'%' for table 'dolt_ignore'")},
	}
	for _, tt := range denials {
		t.Run(tt.name, func(t *testing.T) {
			failOnSwallowedAdvisory(t)
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer db.Close()

			const pattern = "__temp__%"
			expectRefusedIgnoreSeed(mock, LatestVersion(), tt.err, pattern)
			expectNoMigrationWork(mock)
			expectNoNewTables(mock)

			notice := captureIgnoreSeedNotice(t)
			applied, err := MigrateUp(context.Background(), db)
			if err != nil {
				t.Fatalf("MigrateUp() error = %v, want nil: a permission-denied seed must degrade to a warning on an otherwise-converged database", err)
			}
			if applied != 0 {
				t.Fatalf("MigrateUp() applied = %d, want 0", applied)
			}
			warn := notice.String()
			if warn == "" {
				t.Fatal("no degraded-seed warning was printed")
			}
			if strings.Count(warn, "\n") != 1 || !strings.HasSuffix(warn, "\n") {
				t.Fatalf("degraded-seed warning = %q, want exactly one line", warn)
			}
			for _, want := range []string{
				`"` + pattern + `"`,
				tt.err.Error(),
				"continuing without seeding dolt_ignore on the current schema",
				"open this database once with a privileged bd that can write dolt_ignore to heal the missing patterns",
			} {
				if !strings.Contains(warn, want) {
					t.Fatalf("degraded-seed warning %q missing %q", warn, want)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations (the degraded open must issue no subsequent write or commit): %v", err)
			}
		})
	}
}

// TestSeedDoltIgnorePatternsToleratesPermissionDeniedInsert is the triage
// repro for bd-obnpv0, adapted to the approved design: the design rejected a
// seed-local swallow (it loses the denial before the eligibility decision),
// so the tolerance is asserted at MigrateUp, the layer that owns the
// decision. The original repro's shape is kept: the probe reports every
// candidate present except the last two, and every INSERT the seed attempts
// would be denied.
func TestSeedDoltIgnorePatternsToleratesPermissionDeniedInsert(t *testing.T) {
	t.Run("multiple missing patterns, one refused INSERT, one warning", func(t *testing.T) {
		failOnSwallowedAdvisory(t)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		// The seed stops at its first error, so only the first missing
		// candidate's INSERT is ever attempted.
		expectRefusedIgnoreSeed(mock, LatestVersion(), refusedIgnoreSeedInsert(), "wisps", "events")
		expectNoMigrationWork(mock)
		expectNoNewTables(mock)

		notice := captureIgnoreSeedNotice(t)
		applied, err := MigrateUp(context.Background(), db)
		if err != nil {
			t.Fatalf("MigrateUp() error = %v, want nil (a fenced wire client must still open a converged database when its dolt_ignore INSERT is denied)", err)
		}
		if applied != 0 {
			t.Fatalf("MigrateUp() applied = %d, want 0", applied)
		}
		if !strings.Contains(notice.String(), `"wisps"`) {
			t.Fatalf("degraded-seed warning %q must name the refused pattern", notice.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations (the second denied INSERT must never be attempted): %v", err)
		}
	})

	// A first INSERT can succeed before a later one is refused (grants can
	// change under the seed). The degraded open must not turn that partial
	// success into a scoped commit or a convergence claim: no DOLT_ADD or
	// DOLT_COMMIT is primed, so ordered sqlmock fails the test if one runs.
	t.Run("an earlier successful INSERT is never claimed as a scoped commit", func(t *testing.T) {
		failOnSwallowedAdvisory(t)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		candidates := expectedIgnoreSeedCandidates(LatestVersion())
		if len(candidates) < 3 {
			t.Fatalf("expected at least 3 seed candidates, got %d", len(candidates))
		}
		present := candidates[:len(candidates)-2]
		missing := candidates[len(candidates)-2:]
		expectIgnoreSeedProbe(mock, LatestVersion(), present)
		mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
			WithArgs(missing[0]).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
			WithArgs(missing[1]).
			WillReturnError(refusedIgnoreSeedInsert())
		expectNoMigrationWork(mock)
		expectNoNewTables(mock)

		notice := captureIgnoreSeedNotice(t)
		applied, err := MigrateUp(context.Background(), db)
		if err != nil {
			t.Fatalf("MigrateUp() error = %v, want nil", err)
		}
		if applied != 0 {
			t.Fatalf("MigrateUp() applied = %d, want 0", applied)
		}
		if !strings.Contains(notice.String(), fmt.Sprintf("%q", missing[1])) {
			t.Fatalf("degraded-seed warning %q must name the refused pattern", notice.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations (a partially seeded database must not be scoped-committed by the degraded open): %v", err)
		}
	})
}

// TestMigrateUpRefusedSeedFailsWhenWorkPending pins AC-2: the degradation is
// only for a database with nothing else to do. With migration work pending
// the original wrapped seed refusal comes back unchanged — before any
// reconciliation, migration statement or status read — because a pass that
// migrates through a seed it could not assert would blanket-commit
// (DOLT_COMMIT('-Am')) whatever the unasserted patterns were protecting.
func TestMigrateUpRefusedSeedFailsWhenWorkPending(t *testing.T) {
	assertOriginalRefusal := func(t *testing.T, applied int, err error, pattern string, notice *bytes.Buffer) {
		t.Helper()
		if err == nil {
			t.Fatal("MigrateUp() error = nil, want the original wrapped seed refusal")
		}
		if want := wrappedIgnoreSeedErr(pattern, refusedIgnoreSeedInsert()); err.Error() != want {
			t.Fatalf("MigrateUp() error = %q, want the original wrapped seed refusal unchanged: %q", err.Error(), want)
		}
		var mysqlErr *mysql.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1142 {
			t.Fatalf("MigrateUp() error = %v, want the preserved MySQL refusal as the cause", err)
		}
		if applied != 0 {
			t.Fatalf("MigrateUp() applied = %d, want 0", applied)
		}
		if notice.Len() != 0 {
			t.Fatalf("a continuation warning must not print when migration work is pending, got %q", notice.String())
		}
	}

	const pattern = "__temp__%"

	t.Run("main-lane migration pending", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		// A pre-0062-flip database: the version-gated events pattern is not
		// a seed candidate yet, and the main cursor is one below the flip.
		const cursor = eventsFlipVersion - 1
		expectRefusedIgnoreSeed(mock, cursor, refusedIgnoreSeedInsert(), pattern)
		// migrationWorkNeeded reads the main cursor once and reports work;
		// nothing else may run.
		expectCursorProbe(mock, "schema_migrations", true)
		expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", cursor)

		notice := captureIgnoreSeedNotice(t)
		applied, err := MigrateUp(context.Background(), db)
		assertOriginalRefusal(t, applied, err, pattern, notice)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations (no reconciliation, migration statement or status read may follow a refused seed with work pending): %v", err)
		}
	})

	t.Run("ignored-lane migration pending with main current", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		expectRefusedIgnoreSeed(mock, LatestVersion(), refusedIgnoreSeedInsert(), pattern)
		expectCursorProbe(mock, "schema_migrations", true)
		expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
		expectCursorProbe(mock, "ignored_schema_migrations", true)
		expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion()-1)
		expectIgnoredSentinelProbes(mock, true)

		notice := captureIgnoreSeedNotice(t)
		applied, err := MigrateUp(context.Background(), db)
		assertOriginalRefusal(t, applied, err, pattern, notice)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("content-hash backfill pending with main current", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		expectRefusedIgnoreSeed(mock, LatestVersion(), refusedIgnoreSeedInsert(), pattern)
		expectCursorProbe(mock, "schema_migrations", true)
		expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
		expectCursorProbe(mock, "ignored_schema_migrations", true)
		expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
		expectIgnoredSentinelProbes(mock, true)
		// Both cursors are current but the main cursor table predates
		// content_hash (#4259): migrationWorkNeeded reports work.
		mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations LIKE 'content_hash'`).
			WillReturnRows(showColumnsRows())

		notice := captureIgnoreSeedNotice(t)
		applied, err := MigrateUp(context.Background(), db)
		assertOriginalRefusal(t, applied, err, pattern, notice)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}

// seedDenialObserver counts the statements issued after the seed's first
// failed dolt_ignore INSERT, so a test can pin that a non-refusal seed error
// is returned without a single fallback probe: the eligibility check in
// degradeRefusedIgnoreSeed must run before migrationWorkNeeded reads
// anything.
type seedDenialObserver struct {
	*sql.DB
	sawFailedInsert bool
	afterFailed     int
}

func (o *seedDenialObserver) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := o.DB.ExecContext(ctx, query, args...)
	if err != nil && strings.Contains(query, "INSERT IGNORE INTO dolt_ignore") {
		o.sawFailedInsert = true
	}
	return res, err
}

func (o *seedDenialObserver) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if o.sawFailedInsert {
		o.afterFailed++
	}
	return o.DB.QueryContext(ctx, query, args...)
}

func (o *seedDenialObserver) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if o.sawFailedInsert {
		o.afterFailed++
	}
	return o.DB.QueryRowContext(ctx, query, args...)
}

// TestMigrateUpSeedErrorsThatAreNotRefusalsStayFatal pins AC-3: only a
// privilege refusal degrades. A connection fault or a server error in the
// seed's INSERT is a real failure and comes back exactly as the seed wrapped
// it — before any fallback probe or warning — preserving the underlying
// error for errors.Is/errors.As. The 1105 case carries a denial-shaped
// message on purpose: dberrors.IsAccessDenied classifies typed MySQL errors
// by code, not message.
func TestMigrateUpSeedErrorsThatAreNotRefusalsStayFatal(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"connection reset", errors.New("connection reset during INSERT")},
		{"typed 1105 with a denial phrase but a non-denial code", &mysql.MySQLError{Number: 1105, Message: "INSERT command denied to user 'bd_lego_pilot_op'@'%' for table 'dolt_ignore'"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			failOnSwallowedAdvisory(t)
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer db.Close()

			const pattern = "__temp__%"
			obs := &seedDenialObserver{DB: db}
			expectRefusedIgnoreSeed(mock, LatestVersion(), tt.err, pattern)

			notice := captureIgnoreSeedNotice(t)
			applied, err := MigrateUp(context.Background(), obs)
			if err == nil {
				t.Fatal("MigrateUp() error = nil, want the seed failure: only a privilege refusal may degrade")
			}
			if want := wrappedIgnoreSeedErr(pattern, tt.err); err.Error() != want {
				t.Fatalf("MigrateUp() error = %q, want the existing wrapped seed failure unchanged, before any fallback probe: %q", err.Error(), want)
			}
			if !errors.Is(err, tt.err) {
				t.Fatalf("MigrateUp() error = %v, want errors.Is against the original seed cause", err)
			}
			if obs.afterFailed != 0 {
				t.Fatalf("%d statements ran after the failed seed INSERT, want 0 (no fallback probe, status read or warning may run for a non-refusal)", obs.afterFailed)
			}
			if applied != 0 {
				t.Fatalf("MigrateUp() applied = %d, want 0", applied)
			}
			if notice.Len() != 0 {
				t.Fatalf("no continuation warning may print for a non-refusal seed failure, got %q", notice.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}
