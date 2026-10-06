package schema

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// stubSentinelTables replaces the INFORMATION_SCHEMA probe with a fixed answer
// for the duration of a test, following the issueRowCounter seam convention in
// this package. The DBConn is never touched, so these run with no database.
func stubSentinelTables(t *testing.T, present map[string]bool, probeErr error) *[]string {
	t.Helper()
	var probed []string
	orig := sentinelTableExists
	t.Cleanup(func() { sentinelTableExists = orig })
	sentinelTableExists = func(_ context.Context, _ DBConn, table string) (bool, error) {
		probed = append(probed, table)
		if probeErr != nil {
			return false, probeErr
		}
		return present[table], nil
	}
	return &probed
}

func stubSentinelColumns(t *testing.T, present map[string]bool, probeErr error) *[]string {
	t.Helper()
	var probed []string
	orig := sentinelColumnExists
	t.Cleanup(func() { sentinelColumnExists = orig })
	sentinelColumnExists = func(_ context.Context, _ DBConn, table, column string) (bool, error) {
		key := table + "." + column
		probed = append(probed, key)
		if probeErr != nil {
			return false, probeErr
		}
		return present[key], nil
	}
	return &probed
}

// allSentinelTables is the "everything the table sentinels ask for is present"
// shape, so column-sentinel cases can say so without repeating the list.
func allSentinelTables() map[string]bool {
	present := make(map[string]bool, len(ignoredSource.sentinelTables))
	for _, table := range ignoredSource.sentinelTables {
		present[table] = true
	}
	return present
}

// TestCursorRealityFloor is the gh 5033 / gh 4356 regression: the cursor is a
// claim about the schema, and a claim the schema contradicts must not be
// believed. Before this, a database whose clone-local wisp tables were absent
// but whose (also clone-local) cursor rows survived reported at-latest,
// migrationWorkNeeded() short-circuited, the ignored series never re-ran, and
// the damage surfaced much later as "table not found: wisp_dependencies".
//
// A missing sentinel COLUMN is a weaker contradiction than a missing sentinel
// table: it floors the cursor at the sentinel's replayFloor instead of zeroing
// it, so the migrations that ran long before the column existed are not
// replayed. See TestCurrentVersionClampsToSentinelFloor for what that buys.
func TestCursorRealityFloor(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name        string
		present     map[string]bool
		columns     map[string]bool
		wantFloor   int
		wantLimited bool
	}{
		{
			name:        "no wisp tables at all",
			present:     map[string]bool{},
			columns:     map[string]bool{},
			wantFloor:   0,
			wantLimited: true,
		},
		{
			// Partial materialization must count as contradicted; checking
			// only the first sentinel would miss exactly gh 5033's shape,
			// where wisp_dependencies is the missing one.
			name:        "wisps present, wisp_dependencies absent",
			present:     map[string]bool{"wisps": true},
			columns:     map[string]bool{},
			wantFloor:   0,
			wantLimited: true,
		},
		{
			name:        "wisp_dependencies present, wisps absent",
			present:     map[string]bool{"wisp_dependencies": true},
			columns:     map[string]bool{},
			wantFloor:   0,
			wantLimited: true,
		},
		// These two cases document the two real-world shapes the stub seam
		// collapses: an absent leases table and a present-but-column-less leases
		// table both drive sentinelColumnExists to false
		// (columns["leases.granted_node"] == false), because the real
		// schemaColumnExists COUNT(*) is 0 for a missing table and a missing
		// column alike. They deliberately exercise the same branch — the naming
		// records intent, not a distinct seam path. Both floor at 11, which is
		// what lets the replay heal both (0012 re-creates leases, 0016 adds the
		// column) without reaching back to 0007.
		{
			name:        "leases table absent",
			present:     allSentinelTables(),
			columns:     map[string]bool{},
			wantFloor:   11,
			wantLimited: true,
		},
		{
			name:        "leases granted_node absent",
			present:     allSentinelTables(),
			columns:     map[string]bool{},
			wantFloor:   11,
			wantLimited: true,
		},
		{
			// A missing sentinel table outranks any column floor, and says so
			// without paying for the column probe at all.
			name:        "wisp tables and leases column both absent",
			present:     map[string]bool{},
			columns:     map[string]bool{},
			wantFloor:   0,
			wantLimited: true,
		},
		{
			name:        "schema corroborates the cursor",
			present:     allSentinelTables(),
			columns:     map[string]bool{"leases.granted_node": true},
			wantFloor:   0,
			wantLimited: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubSentinelTables(t, tc.present, nil)
			stubSentinelColumns(t, tc.columns, nil)
			floor, limited, err := ignoredSource.cursorRealityFloor(ctx, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if limited != tc.wantLimited {
				t.Errorf("cursorRealityFloor limited = %v, want %v", limited, tc.wantLimited)
			}
			if limited && floor != tc.wantFloor {
				t.Errorf("cursorRealityFloor floor = %d, want %d", floor, tc.wantFloor)
			}
		})
	}
}

// mockIgnoredCursorRead stands up a database that answers exactly the two
// queries currentVersion issues for the ignored series before it consults the
// sentinels: the cursor-table existence probe and the MAX(version) read.
func mockIgnoredCursorRead(t *testing.T, raw int) *sql.DB {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", raw)
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet cursor-read expectations: %v", err)
		}
		db.Close()
	})
	return db
}

// TestCurrentVersionClampsToSentinelFloor is the wisp-restamp regression
// (#5981 harm class, #5366 follow-up). Every released tree — v1.1.0, v1.1.2,
// v1.2.x — tops the ignored series at 0011 and has no leases table, so the
// leases.granted_node sentinel reads absent on the very first open by a 1.3.0
// binary. Zeroing the cursor there replays 0001–0025, and ignored/0007's
// unguarded `UPDATE wisps SET is_blocked = 0` re-fires wisps.updated_at's
// ON UPDATE CURRENT_TIMESTAMP across a plane with no history to restore from.
// The cursor must be believed up to the floor instead, so only the genuinely
// pending tail applies.
func TestCurrentVersionClampsToSentinelFloor(t *testing.T) {
	ctx := context.Background()
	latest := LatestIgnoredVersion()

	for _, tc := range []struct {
		name    string
		raw     int
		present map[string]bool
		columns map[string]bool
		want    int
	}{
		{
			// THE regression: the release lineage. Believed 11 ⇒ pending set is
			// 0012–0025, which is the correct pending set for these stores.
			name:    "release lineage upgrade, cursor 11, leases absent",
			raw:     11,
			present: allSentinelTables(),
			columns: map[string]bool{},
			want:    11,
		},
		{
			// The ga-usd3k ordinal collision: leases exists, the column does
			// not. Clamped to 11, so 0012–0015 re-run guarded/idempotent and
			// 0016 heals — without 0007.
			name:    "ordinal collision, cursor 17, column absent",
			raw:     17,
			present: allSentinelTables(),
			columns: map[string]bool{},
			want:    11,
		},
		{
			name:    "at-latest cursor, column absent",
			raw:     latest,
			present: allSentinelTables(),
			columns: map[string]bool{},
			want:    11,
		},
		{
			// A cursor already below the floor is believed as read: the floor
			// is a ceiling on belief, never a promotion.
			name:    "cursor below the floor is untouched",
			raw:     8,
			present: allSentinelTables(),
			columns: map[string]bool{},
			want:    8,
		},
		{
			// gh 5033 is bit-for-bit unchanged: table sentinels floor at 0, so
			// the whole series still replays for the shape it was written for.
			name:    "wisp tables absent, at-latest cursor",
			raw:     latest,
			present: map[string]bool{},
			columns: map[string]bool{},
			want:    0,
		},
		{
			name:    "wisp tables and leases column both absent",
			raw:     latest,
			present: map[string]bool{},
			columns: map[string]bool{},
			want:    0,
		},
		{
			name:    "fully corroborated cursor is believed as read",
			raw:     latest,
			present: allSentinelTables(),
			columns: map[string]bool{"leases.granted_node": true},
			want:    latest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubSentinelTables(t, tc.present, nil)
			stubSentinelColumns(t, tc.columns, nil)
			db := mockIgnoredCursorRead(t, tc.raw)

			got, err := ignoredSource.currentVersion(ctx, db)
			if err != nil {
				t.Fatalf("currentVersion: %v", err)
			}
			if got != tc.want {
				t.Errorf("currentVersion = %d, want %d (raw cursor %d)", got, tc.want, tc.raw)
			}
		})
	}
}

// A probe failure must not be laundered into a contradiction — that would
// re-run the series on a database whose state could not be read.
func TestCursorProbeErrorIsNotTreatedAsContradicted(t *testing.T) {
	boom := errors.New("information_schema unavailable")
	stubSentinelTables(t, nil, boom)

	_, limited, err := ignoredSource.cursorRealityFloor(context.Background(), nil)
	if !errors.Is(err, boom) {
		t.Errorf("probe error was swallowed: err = %v", err)
	}
	if limited {
		t.Error("an unreadable probe reported the cursor as contradicted")
	}
}

// The main series is deliberately unguarded: its tables are not clone-local,
// so it cannot reach the contradicted state, and probing would add queries to
// every store open for nothing. cursorRealityFloor must be a no-op for it —
// including never probing.
func TestMainSourceIsNotProbed(t *testing.T) {
	probed := stubSentinelTables(t, map[string]bool{}, nil)
	columnProbed := stubSentinelColumns(t, map[string]bool{}, nil)

	_, limited, err := mainSource.cursorRealityFloor(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limited {
		t.Error("mainSource reported contradicted; it has no sentinels and must never be")
	}
	if len(*probed) != 0 {
		t.Errorf("mainSource probed %v; it was deliberately left unguarded", *probed)
	}
	if len(*columnProbed) != 0 {
		t.Errorf("mainSource probed column sentinels %v; it was deliberately left unguarded", *columnProbed)
	}
	if len(ignoredSource.sentinelTables) == 0 {
		t.Error("ignoredSource has no sentinel tables; the gh 5033 guard is inert")
	}
}

// TestSentinelTablesAreCreatedByTheSeries keeps the sentinel list honest. A
// sentinel this series does not create could never be repaired by re-running
// it, so the cursor would be rejected on every single open — turning a silent
// wrong answer into a permanent re-migration loop.
func TestSentinelTablesAreCreatedByTheSeries(t *testing.T) {
	entries, err := ignoredSource.files.ReadDir(ignoredSource.dir)
	if err != nil {
		t.Fatalf("read %s: %v", ignoredSource.dir, err)
	}
	var all strings.Builder
	for _, e := range entries {
		blob, err := ignoredSource.files.ReadFile(ignoredSource.dir + "/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		all.Write(blob)
	}
	body := all.String()

	for _, table := range ignoredSource.sentinelTables {
		// ignored/0001 builds each table as __temp__<name> and renames it into
		// place only when <name> is absent, which is also why re-running the
		// series repairs rather than clobbers.
		if !strings.Contains(body, "__temp__"+table) {
			t.Errorf("sentinel %q is never created by the %s series; re-running it could not repair that table",
				table, ignoredSource.dir)
		}
	}
}

// TestSentinelColumnsAreCreatedByTheSeries is the column-sentinel analogue of
// TestSentinelTablesAreCreatedByTheSeries, plus the floor-honesty check the
// replay clamp depends on. Two things have to hold for every sentinel column:
//
//   - The series must ADD the column, and must CREATE the table carrying it.
//     A sentinel the series cannot repair would be rejected on every open —
//     a permanent re-migration loop in the field.
//   - Both of those files must sit strictly ABOVE the sentinel's replayFloor,
//     or the clamped replay would skip the very migrations that heal it. That
//     is what makes floor 11 correct today (0012 creates leases, 0016 adds
//     granted_node) and what makes a future renumbering of either file fail
//     here instead of stranding users.
//
// Versions are parsed per file rather than matched against the concatenated
// series precisely so the floor can be checked, not just the existence.
func TestSentinelColumnsAreCreatedByTheSeries(t *testing.T) {
	if len(ignoredSource.sentinelColumns) == 0 {
		t.Fatal("ignoredSource has no sentinel columns; the granted_node guard is inert")
	}

	bodies := make(map[int]string)
	for _, mf := range ignoredSource.list() {
		blob, err := ignoredSource.files.ReadFile(ignoredSource.dir + "/" + mf.name)
		if err != nil {
			t.Fatalf("read %s: %v", mf.name, err)
		}
		bodies[mf.version] = string(blob)
	}

	// findVersion returns the lowest migration version whose body matches,
	// or 0 when nothing does.
	findVersion := func(re *regexp.Regexp) int {
		found := 0
		for version, body := range bodies {
			if !re.MatchString(body) {
				continue
			}
			if found == 0 || version < found {
				found = version
			}
		}
		return found
	}

	// The floor exists to exclude ignored/0007's unguarded recompute, whose
	// bare `UPDATE wisps SET is_blocked = 0` re-fires wisps.updated_at's
	// ON UPDATE CURRENT_TIMESTAMP. Its guarded successor 0015 writes
	// `is_blocked = 0, updated_at = updated_at` and deliberately does not
	// match. Pinning the file rather than a bare literal keeps the floor
	// honest if the pair is ever renumbered.
	unguarded := findVersion(regexp.MustCompile(`(?i)UPDATE\s+wisps\s+SET\s+is_blocked\s*=\s*0\s*;`))
	if unguarded == 0 {
		t.Fatal("the unguarded `UPDATE wisps SET is_blocked = 0` the replay floor exists to exclude is gone; re-derive the floor")
	}

	for _, sc := range ignoredSource.sentinelColumns {
		// ignored/0016 adds the column with a guarded, idempotent
		// `ALTER TABLE <table> ADD COLUMN <column>`, which is also why
		// re-running it repairs rather than clobbers. Bind the table so a
		// sentinel naming a column the series only adds to some other table
		// (or never adds at all) fails here rather than in the field.
		adder := findVersion(regexp.MustCompile(`(?i)ALTER TABLE\s+` + regexp.QuoteMeta(sc.table) +
			`\s+ADD COLUMN\s+` + regexp.QuoteMeta(sc.column)))
		if adder == 0 {
			t.Errorf("sentinel column %s.%s is never added by the %s series; re-running it could not repair that column",
				sc.table, sc.column, ignoredSource.dir)
			continue
		}
		// ignored/0012 builds the carrier table as __temp__<name> and renames
		// it into place only when <name> is absent. The single COLUMNS probe
		// cannot tell "table missing" from "column missing", so the replay has
		// to be able to heal both.
		creator := findVersion(regexp.MustCompile(regexp.QuoteMeta("__temp__" + sc.table)))
		if creator == 0 {
			t.Errorf("carrier table %s for sentinel column %s.%s is never created by the %s series",
				sc.table, sc.table, sc.column, ignoredSource.dir)
			continue
		}

		if adder <= sc.replayFloor {
			t.Errorf("sentinel column %s.%s is added by ignored/%04d, at or below its replayFloor %d; the clamped replay would skip the migration that heals it",
				sc.table, sc.column, adder, sc.replayFloor)
		}
		if creator <= sc.replayFloor {
			t.Errorf("carrier table %s is created by ignored/%04d, at or below sentinel %s.%s's replayFloor %d; a store missing the table entirely would never be repaired",
				sc.table, creator, sc.table, sc.column, sc.replayFloor)
		}
		if sc.replayFloor < unguarded {
			t.Errorf("sentinel %s.%s replayFloor %d does not exclude ignored/%04d, whose unguarded UPDATE restamps wisps.updated_at",
				sc.table, sc.column, sc.replayFloor, unguarded)
		}
	}
}

// TestMigrationWorkNeededWhenWispTablesAbsent is gh 5033 end to end, through
// the real short-circuit that caused it. Both cursors read at-latest, so
// before this change migrationWorkNeeded returned false, MigrateUp did
// nothing, and the missing wisp tables were never recreated — the user met the
// bug much later as "table not found: wisp_dependencies" on `bd close`.
//
// The first sentinel probe returning 0 is enough to decide, so exactly one
// probe is expected: proving the check short-circuits is part of the contract,
// since it runs on every store open.
func TestMigrationWorkNeededWhenWispTablesAbsent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	mock.ExpectQuery(regexp.QuoteMeta("FROM INFORMATION_SCHEMA.TABLES")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	needed, err := migrationWorkNeeded(context.Background(), db)
	if err != nil {
		t.Fatalf("migrationWorkNeeded: %v", err)
	}
	if !needed {
		t.Fatal("migrationWorkNeeded = false with an at-latest cursor and no wisp tables; the ignored series would never re-run (gh 5033)")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestMigrationWorkNeededWhenLeaseGrantedNodeAbsent keeps the historical
// ignored-v16 ordinal collision on the actual MigrateUp short-circuit path:
// both cursors claim at-latest and both table sentinels corroborate that
// claim, but the clone-local leases shape does not.
func TestMigrationWorkNeededWhenLeaseGrantedNodeAbsent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	for range ignoredSource.sentinelTables {
		mock.ExpectQuery(regexp.QuoteMeta("FROM INFORMATION_SCHEMA.TABLES")).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	}
	mock.ExpectQuery(regexp.QuoteMeta("FROM INFORMATION_SCHEMA.COLUMNS")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	needed, err := migrationWorkNeeded(context.Background(), db)
	if err != nil {
		t.Fatalf("migrationWorkNeeded: %v", err)
	}
	if !needed {
		t.Fatal("migrationWorkNeeded = false with an at-latest cursor and missing leases.granted_node; frozen ignored 0016 would never replay")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet ordered sentinel probes: %v", err)
	}
}

// TestMigrateAppliesUnderContradictedCursor closes the loop the work-needed
// test cannot: deciding "work needed" is worthless if the applier then
// re-reads the cursor raw, believes it, and applies nothing — MigrateUp would
// run its whole pass preamble on every store open, heal nothing, and repeat
// forever. The applier must read through the same guarded currentVersion.
//
// The mock walks migrate() up to the first statement of ignored/0001 and fails
// it with a distinctive error: reaching that statement at all proves the
// contradicted cursor was disbelieved by the APPLIER (an at-latest raw read
// would have returned "nothing to do" without touching migration SQL), and the
// ordered expectations prove it happened via the sentinel probe.
func TestMigrateAppliesUnderContradictedCursor(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// migrate() preamble: cursor-table bootstrap, content_hash presence check.
	mock.ExpectExec(regexp.QuoteMeta("CREATE TABLE IF NOT EXISTS ignored_schema_migrations")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SHOW COLUMNS FROM ignored_schema_migrations LIKE 'content_hash'")).
		WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
			AddRow("content_hash", "char(64)", "YES", "", nil, ""))

	// The guarded read: cursor claims at-latest, sentinel probe contradicts it.
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	mock.ExpectQuery(regexp.QuoteMeta("FROM INFORMATION_SCHEMA.TABLES")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	// The proof: the applier reaches ignored/0001's SQL. Fail it distinctively
	// rather than mocking the whole series.
	boom := errors.New("stop here: the applier disbelieved the cursor")
	mock.ExpectExec(".*").WillReturnError(boom)

	_, _, err = ignoredSource.migrate(context.Background(), db, 0)
	if err == nil {
		t.Fatal("migrate returned nil with a contradicted at-latest cursor; the applier believed the raw cursor and healed nothing (gh 5033)")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("migrate failed for another reason before applying: %v", err)
	}
	if !strings.Contains(err.Error(), "0001") {
		t.Errorf("failure not attributed to the first ignored migration: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestMigrateStartsAboveTheFloorUnderColumnContradiction is the applier-side
// half of the clamp, and the sibling of TestMigrateAppliesUnderContradictedCursor
// (which keeps covering the table-sentinel shape). Deciding the believed cursor
// correctly is worthless if the applier then starts the replay somewhere else:
// the harm this fix exists to stop is executed BY the applier, when it re-runs
// ignored/0007.
//
// The mock walks migrate() to the first statement it applies and fails it
// distinctively. Attribution to 0012 — not 0001 — is the proof: the applier
// skipped 0001–0011 (0007 among them) and resumed at the leases creator, which
// is exactly the pending set a clamped cursor of 11 describes.
func TestMigrateStartsAboveTheFloorUnderColumnContradiction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// migrate() preamble: cursor-table bootstrap, content_hash presence check.
	mock.ExpectExec(regexp.QuoteMeta("CREATE TABLE IF NOT EXISTS ignored_schema_migrations")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SHOW COLUMNS FROM ignored_schema_migrations LIKE 'content_hash'")).
		WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
			AddRow("content_hash", "char(64)", "YES", "", nil, ""))

	// The guarded read: cursor claims at-latest, both table sentinels
	// corroborate it, and only the leases column does not.
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	for range ignoredSource.sentinelTables {
		mock.ExpectQuery(regexp.QuoteMeta("FROM INFORMATION_SCHEMA.TABLES")).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	}
	mock.ExpectQuery(regexp.QuoteMeta("FROM INFORMATION_SCHEMA.COLUMNS")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	boom := errors.New("stop here: the applier resumed above the floor")
	mock.ExpectExec(".*").WillReturnError(boom)

	_, _, err = ignoredSource.migrate(context.Background(), db, 0)
	if err == nil {
		t.Fatal("migrate returned nil with a column-contradicted at-latest cursor; the heal for ignored 0012/0016 never ran")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("migrate failed for another reason before applying: %v", err)
	}
	if !strings.Contains(err.Error(), "0012") {
		t.Errorf("replay did not resume at the leases creator: %v", err)
	}
	if strings.Contains(err.Error(), "0001_") {
		t.Errorf("replay reached back into the pre-floor range; ignored/0007 would restamp wisps.updated_at: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestPendingVersionsUnderFloorDoNotReArmAuxMarkers guards the collateral the
// clamp also fixes. rekeyAuxRowIDsAllPasses gates each convergence pass on its
// marker version appearing in ignoredSource.pendingVersions, which reads the
// same guarded cursor. Zeroing put marker 9 back in the pending set on every
// release-lineage upgrade, spuriously re-running the pass-1 aux row-id rewrite
// over the synced events/comments/snapshots planes — a convergent no-op by
// design, but wasted work and needless churn risk. The clamp makes the marker
// gate see the true pending set.
func TestPendingVersionsUnderFloorDoNotReArmAuxMarkers(t *testing.T) {
	stubSentinelTables(t, allSentinelTables(), nil)
	stubSentinelColumns(t, map[string]bool{}, nil)
	db := mockIgnoredCursorRead(t, 11)

	pending, err := ignoredSource.pendingVersions(context.Background(), db)
	if err != nil {
		t.Fatalf("pendingVersions: %v", err)
	}

	pendingSet := make(map[int]bool, len(pending))
	for _, v := range pending {
		pendingSet[v] = true
	}

	for _, pass := range auxRekeyPasses {
		// Marker 9 is below the floor and must stay applied; marker 18 is
		// genuinely pending on a cursor-11 store and must stay armed.
		want := pass.markerVersion > 11
		if pendingSet[pass.markerVersion] != want {
			t.Errorf("aux rekey marker %d pending = %v, want %v (cursor 11 clamped at floor 11)",
				pass.markerVersion, pendingSet[pass.markerVersion], want)
		}
	}
	if !pendingSet[12] {
		t.Error("ignored/0012 is not pending on a cursor-11 store; the leases heal would never run")
	}
	if pendingSet[7] {
		t.Error("ignored/0007 is pending on a cursor-11 store; its unguarded UPDATE would restamp wisps.updated_at")
	}
}
