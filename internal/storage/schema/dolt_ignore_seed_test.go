package schema

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
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

func upperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}
