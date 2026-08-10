package schema

import (
	"context"
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

// TestCursorContradictedBySchema is the gh 5033 / gh 4356 regression: the
// cursor is a claim about the schema, and a claim the schema contradicts must
// not be believed. Before this, a database whose clone-local wisp tables were
// absent but whose (also clone-local) cursor rows survived reported at-latest,
// migrationWorkNeeded() short-circuited, the ignored series never re-ran, and
// the damage surfaced much later as "table not found: wisp_dependencies".
func TestCursorContradictedBySchema(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		present map[string]bool
		want    bool
	}{
		{
			name:    "no wisp tables at all",
			present: map[string]bool{},
			want:    true,
		},
		{
			// Partial materialization must count as contradicted; checking
			// only the first sentinel would miss exactly gh 5033's shape,
			// where wisp_dependencies is the missing one.
			name:    "wisps present, wisp_dependencies absent",
			present: map[string]bool{"wisps": true},
			want:    true,
		},
		{
			name:    "wisp_dependencies present, wisps absent",
			present: map[string]bool{"wisp_dependencies": true},
			want:    true,
		},
		{
			name:    "schema corroborates the cursor",
			present: map[string]bool{"wisps": true, "wisp_dependencies": true},
			want:    false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubSentinelTables(t, tc.present, nil)
			got, err := ignoredSource.cursorContradictedBySchema(ctx, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("cursorContradictedBySchema = %v, want %v", got, tc.want)
			}
		})
	}
}

// A probe failure must not be laundered into "contradicted" — that would
// re-run the whole series on a database whose state could not be read.
func TestCursorProbeErrorIsNotTreatedAsContradicted(t *testing.T) {
	boom := errors.New("information_schema unavailable")
	stubSentinelTables(t, nil, boom)

	got, err := ignoredSource.cursorContradictedBySchema(context.Background(), nil)
	if !errors.Is(err, boom) {
		t.Errorf("probe error was swallowed: err = %v", err)
	}
	if got {
		t.Error("an unreadable probe reported the cursor as contradicted")
	}
}

// The main series is deliberately unguarded: its tables are not clone-local,
// so it cannot reach the contradicted state, and probing would add queries to
// every store open for nothing. cursorContradictedBySchema must be a no-op for
// it — including never probing.
func TestMainSourceIsNotProbed(t *testing.T) {
	probed := stubSentinelTables(t, map[string]bool{}, nil)

	got, err := mainSource.cursorContradictedBySchema(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("mainSource reported contradicted; it has no sentinels and must never be")
	}
	if len(*probed) != 0 {
		t.Errorf("mainSource probed %v; it was deliberately left unguarded", *probed)
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

	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
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
