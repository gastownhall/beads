package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// stubSentinelTables replaces the table-existence probe with a fixed answer
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

// expectSentinelColumnProbe mocks the real column-sentinel probe in order: the
// carrier table's existence through sentinelTableExists (a SHOW TABLES read),
// then — only when the table is there — the SHOW COLUMNS read. An absent column
// is an empty SHOW COLUMNS result set, not a zero count.
func expectSentinelColumnProbe(mock sqlmock.Sqlmock, sc schemaSentinelColumn, tablePresent, columnPresent bool) {
	expectShowTable(mock, sc.table, tablePresent)
	if !tablePresent {
		return
	}
	rows := sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"})
	if columnPresent {
		rows.AddRow(sc.column, "varchar(255)", "YES", "", nil, "")
	}
	mock.ExpectQuery(regexp.QuoteMeta("SHOW COLUMNS FROM " + sc.table + " LIKE '" + sc.column + "'")).
		WillReturnRows(rows)
}

// allSentinelTables is the "everything the table sentinels ask for is present"
// shape, so column-sentinel cases can say so without repeating the list. Both
// the unconditional-floor-0 tables and the floored tables share the same
// sentinelTableExists probe, so both must be marked present here or a case
// meant to isolate one sentinel would spuriously trip another.
func allSentinelTables() map[string]bool {
	present := make(map[string]bool, len(ignoredSource.sentinelTables)+len(ignoredSource.sentinelFlooredTables))
	for _, table := range ignoredSource.sentinelTables {
		present[table] = true
	}
	for _, st := range ignoredSource.sentinelFlooredTables {
		present[st.table] = true
	}
	return present
}

// allSentinelTablesExcept is allSentinelTables with the named tables absent,
// for cases that need every sentinel healthy except the one(s) under test.
// Variadic so a case can contradict two sentinels of the same kind at once,
// which is what pins the minimum a single loop accumulates.
func allSentinelTablesExcept(tables ...string) map[string]bool {
	present := allSentinelTables()
	for _, table := range tables {
		delete(present, table)
	}
	return present
}

// allSentinelColumns is the column-sentinel analogue of allSentinelTables:
// every sentinel column the probe asks for is present. stubSentinelColumns
// treats an omitted key as absent, so a case isolating one column must say
// the others are healthy or it would spuriously contradict them too.
func allSentinelColumns() map[string]bool {
	present := make(map[string]bool, len(ignoredSource.sentinelColumns))
	for _, sc := range ignoredSource.sentinelColumns {
		present[sc.table+"."+sc.column] = true
	}
	return present
}

// allSentinelColumnsExcept is allSentinelColumns with the named
// table.column keys absent, for cases that contradict exactly those.
func allSentinelColumnsExcept(columns ...string) map[string]bool {
	present := allSentinelColumns()
	for _, column := range columns {
		delete(present, column)
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
		// wantNoColumnProbes: the missing sentinel table short-circuits
		// before any column probe runs — the gh 5257 column must not add a
		// probe to that cheapest path (it runs on every store open).
		wantNoColumnProbes bool
	}{
		{
			name:               "no wisp tables at all",
			present:            map[string]bool{},
			columns:            map[string]bool{},
			wantFloor:          0,
			wantLimited:        true,
			wantNoColumnProbes: true,
		},
		{
			// Partial materialization must count as contradicted; checking
			// only the first sentinel would miss exactly gh 5033's shape,
			// where wisp_dependencies is the missing one.
			name:               "wisps present, wisp_dependencies absent",
			present:            map[string]bool{"wisps": true},
			columns:            map[string]bool{},
			wantFloor:          0,
			wantLimited:        true,
			wantNoColumnProbes: true,
		},
		{
			name:               "wisp_dependencies present, wisps absent",
			present:            map[string]bool{"wisp_dependencies": true},
			columns:            map[string]bool{},
			wantFloor:          0,
			wantLimited:        true,
			wantNoColumnProbes: true,
		},
		// These two cases document the two real-world shapes the stub seam
		// collapses: an absent leases table and a present-but-column-less leases
		// table both drive sentinelColumnExists to false
		// (columns["leases.granted_node"] == false), because the real probe
		// reports a missing table (its existence read) and a missing column (its
		// SHOW COLUMNS read) alike. They deliberately exercise the same branch —
		// the naming records intent, not a distinct seam path; the probe itself
		// is pinned by TestSentinelColumnProbeNeverAsksAMissingTable. Both floor
		// at 11, which is what lets the replay heal both (0012 re-creates leases,
		// 0016 adds the column) without reaching back to 0007.
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
			columns:     allSentinelColumns(),
			wantFloor:   0,
			wantLimited: false,
		},
		{
			name:        "events absent",
			present:     allSentinelTablesExcept("events"),
			columns:     allSentinelColumns(),
			wantFloor:   18,
			wantLimited: true,
		},
		{
			name:        "bd_events_journal absent",
			present:     allSentinelTablesExcept("bd_events_journal"),
			columns:     allSentinelColumns(),
			wantFloor:   21,
			wantLimited: true,
		},
		{
			name:        "bd_events_seq absent",
			present:     allSentinelTablesExcept("bd_events_seq"),
			columns:     allSentinelColumns(),
			wantFloor:   21,
			wantLimited: true,
		},
		{
			// Two floored sentinels contradicted at once: the minimum must win
			// WITHIN the floored loop, not only across loops. Without this
			// case every case removes exactly one floored table, so the loop
			// only ever reaches its !limited arm and `st.replayFloor < floor`
			// is never evaluated against a floor an earlier absent sentinel
			// already set — inverting it to `>` leaves the package green. The
			// floors happen to ascend in slice order (18, 21, 21) today, so
			// this is what keeps a future reordering or lower-floored addition
			// from silently regressing to "last absent sentinel wins".
			name:        "events and bd_events_journal both absent",
			present:     allSentinelTablesExcept("events", "bd_events_journal"),
			columns:     allSentinelColumns(),
			wantFloor:   18,
			wantLimited: true,
		},
		{
			// Combined contradiction across two different sentinel kinds: the
			// true minimum (11, from the column) must win over the higher
			// floored-table floor (18) the table loop finds first, proving the
			// accumulator takes the minimum across both loops rather than
			// whichever ran first.
			name:        "events absent and leases granted_node absent",
			present:     allSentinelTablesExcept("events"),
			columns:     map[string]bool{},
			wantFloor:   11,
			wantLimited: true,
		},
		{
			// gh 5257: both table sentinels corroborate the cursor and the
			// leases column is healthy, but wisps.is_blocked — added by
			// ignored/0006, whose guarded ALTER is exactly what a
			// NULL-hash-stamped cursor row can skip — is gone. Table-only
			// sentinels believed that cursor forever; the column floors it at
			// 14, the highest floor that still replays 0015 and the repair
			// registered immediately before it (which re-adds the column and
			// index in code) while staying above frozen 0007's unguarded
			// recompute.
			name:        "wisps is_blocked absent",
			present:     allSentinelTables(),
			columns:     allSentinelColumnsExcept("wisps.is_blocked"),
			wantFloor:   14,
			wantLimited: true,
		},
		{
			// Both column sentinels contradicted: the minimum (11, leases)
			// wins, so a single missing column never drags the replay lower
			// than the other column needs.
			name:        "both column sentinels absent",
			present:     allSentinelTables(),
			columns:     map[string]bool{},
			wantFloor:   11,
			wantLimited: true,
		},
		{
			// The minimum across sentinel kinds with the gh 5257 column:
			// events floors at 18, wisps.is_blocked at 14 — the lower wins.
			name:        "events absent and wisps is_blocked absent",
			present:     allSentinelTablesExcept("events"),
			columns:     allSentinelColumnsExcept("wisps.is_blocked"),
			wantFloor:   14,
			wantLimited: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubSentinelTables(t, tc.present, nil)
			columnProbed := stubSentinelColumns(t, tc.columns, nil)
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
			if tc.wantNoColumnProbes && len(*columnProbed) != 0 {
				t.Errorf("column sentinels %v were probed after a missing sentinel table; the floor-0 short-circuit must stay probe-free", *columnProbed)
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
			// THE gh 5257 regression: an at-latest cursor whose only
			// contradiction is the missing wisps.is_blocked column clamps to
			// 14 — never to zero — so the replay is the 0015–0031 tail whose
			// pre-0015 repair re-adds the column and index, and frozen 0007's
			// unguarded recompute stays out of it.
			name:    "at-latest cursor, wisps is_blocked absent",
			raw:     latest,
			present: allSentinelTables(),
			columns: allSentinelColumnsExcept("wisps.is_blocked"),
			want:    14,
		},
		{
			// The floor is a ceiling on belief: a raw cursor one above the
			// floor comes back down to it, so 0015 (and its repair) are
			// pending on exactly the stores the column is missing from.
			name:    "cursor 15, wisps is_blocked absent",
			raw:     15,
			present: allSentinelTables(),
			columns: allSentinelColumnsExcept("wisps.is_blocked"),
			want:    14,
		},
		{
			// ...but never a promotion: cursors at or below the floor are
			// believed as read (their own already-pending repairs, like the
			// ignored/0007 one, still reach them; see
			// ensureWispIsBlockedForRecompute).
			name:    "cursor 14, wisps is_blocked absent",
			raw:     14,
			present: allSentinelTables(),
			columns: allSentinelColumnsExcept("wisps.is_blocked"),
			want:    14,
		},
		{
			name:    "cursor 6, wisps is_blocked absent",
			raw:     6,
			present: allSentinelTables(),
			columns: allSentinelColumnsExcept("wisps.is_blocked"),
			want:    6,
		},
		{
			// Both columns contradicted: the leases minimum (11) wins over
			// the is_blocked floor (14).
			name:    "both column sentinels absent, at-latest cursor",
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
			columns: allSentinelColumns(),
			want:    latest,
		},
		{
			name:    "events absent, at-latest cursor",
			raw:     latest,
			present: allSentinelTablesExcept("events"),
			columns: allSentinelColumns(),
			want:    18,
		},
		{
			name:    "bd_events_journal absent, at-latest cursor",
			raw:     latest,
			present: allSentinelTablesExcept("bd_events_journal"),
			columns: allSentinelColumns(),
			want:    21,
		},
		{
			name:    "bd_events_seq absent, at-latest cursor",
			raw:     latest,
			present: allSentinelTablesExcept("bd_events_seq"),
			columns: allSentinelColumns(),
			want:    21,
		},
		{
			// Combined contradiction across two different sentinel kinds: the
			// true minimum (11, from the column) must win over the higher
			// floored-table floor (18).
			name:    "events absent and leases column absent, at-latest cursor",
			raw:     latest,
			present: allSentinelTablesExcept("events"),
			columns: map[string]bool{},
			want:    11,
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

// The floored-table loop must surface a probe failure the same way, and the
// test above cannot reach it: its blanket error fails the floor-0 wisps probe
// and returns there. So each case reads every other sentinel table present and
// fails exactly one floored table. Reading that error as "absent" would clamp a
// healthy at-latest cursor to the table's replayFloor and replay the tail on a
// catalog nobody could read; skipping it would believe the cursor over a table
// that may be missing.
func TestFlooredTableProbeErrorIsNotTreatedAsContradicted(t *testing.T) {
	if len(ignoredSource.sentinelFlooredTables) == 0 {
		t.Fatal("ignoredSource has no floored sentinel tables; this guard is inert")
	}
	boom := errors.New("information_schema unavailable")
	for _, failing := range ignoredSource.sentinelFlooredTables {
		t.Run(failing.table, func(t *testing.T) {
			present := allSentinelTables()
			orig := sentinelTableExists
			t.Cleanup(func() { sentinelTableExists = orig })
			sentinelTableExists = func(_ context.Context, _ DBConn, table string) (bool, error) {
				if table == failing.table {
					return false, boom
				}
				return present[table], nil
			}
			columnProbed := stubSentinelColumns(t, allSentinelColumns(), nil)

			_, limited, err := ignoredSource.cursorRealityFloor(context.Background(), nil)
			if !errors.Is(err, boom) {
				t.Errorf("floored-table probe error was swallowed: err = %v", err)
			}
			if limited {
				t.Error("an unreadable floored-table probe reported the cursor as contradicted")
			}
			if len(*columnProbed) != 0 {
				t.Errorf("column sentinels %v were probed after a floored-table probe failed", *columnProbed)
			}
		})
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
// A column whose SQL creator sits at or below its floor (wisps.is_blocked:
// created by ignored/0006, healed from floor 14) may instead lean on a
// registered pre-migration repair ABOVE the floor that restores the column in
// code (preMigrationRepairs, here ensureWispIsBlockedForRecompute before
// 0015), provided its carrier is also a floor-0 sentinel table — the
// table-absent shape then floors to zero through the table sentinel and
// replays the carrier's creator regardless. The helper below exercises the
// registered repair against a missing-column fixture rather than trusting the
// registry entry's existence, so removing the registration, pointing it at
// the wrong function, or raising the floor above the healer's version all
// fail here.
//
// Versions are parsed per file rather than matched against the concatenated
// series precisely so the floor can be checked, not just the existence.
func TestSentinelColumnsAreCreatedByTheSeries(t *testing.T) {
	if len(ignoredSource.sentinelColumns) == 0 {
		t.Fatal("ignoredSource has no sentinel columns; the granted_node guard is inert")
	}

	t.Run("the registered series satisfies the invariants", func(t *testing.T) {
		for _, problem := range checkSentinelColumnsAgainstSeries(t, ignoredSource.sentinelColumns, preMigrationRepairs) {
			t.Errorf("%s", problem)
		}
	})
	t.Run("raising a healer-backed floor above its healer fails", func(t *testing.T) {
		raised := slices.Clone(ignoredSource.sentinelColumns)
		healerBacked := false
		for i := range raised {
			// The one shape this mutation is meaningful for: a column whose
			// heal is delegated to a repair (adder at or below its floor).
			// Floor 14 -> 15 makes 0015's repair unreachable from the clamp.
			if raised[i].table == "wisps" && raised[i].column == "is_blocked" {
				raised[i].replayFloor = 15
				healerBacked = true
			}
		}
		if !healerBacked {
			t.Skip("no healer-backed sentinel column to raise")
		}
		if problems := checkSentinelColumnsAgainstSeries(t, raised, preMigrationRepairs); len(problems) == 0 {
			t.Fatal("a floor of 15 for wisps.is_blocked passed; the clamped replay would skip ignored/0015 and its column-healing repair")
		}
	})
	t.Run("removing the healer registration fails", func(t *testing.T) {
		pruned := make(map[repairKey]func(context.Context, DBConn) error, len(preMigrationRepairs))
		for key, repair := range preMigrationRepairs {
			if key == (repairKey{cursorTable: ignoredSource.cursorTable, version: 15}) {
				continue
			}
			pruned[key] = repair
		}
		if problems := checkSentinelColumnsAgainstSeries(t, ignoredSource.sentinelColumns, pruned); len(problems) == 0 {
			t.Fatal("dropping the pre-0015 repair registration passed; a wisps.is_blocked heal would have nothing to restore the column")
		}
	})
}

// checkSentinelColumnsAgainstSeries is the body of
// TestSentinelColumnsAreCreatedByTheSeries, parameterized over the column
// list and the repair registry so the test can also prove its own guards fire
// on the mutations they exist to catch. It returns the invariant violations
// found; an empty slice is a healthy registration.
func checkSentinelColumnsAgainstSeries(t *testing.T, columns []schemaSentinelColumn, repairs map[repairKey]func(context.Context, DBConn) error) []string {
	t.Helper()

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

	var problems []string
	for _, sc := range columns {
		// ignored/0016 adds the column with a guarded, idempotent
		// `ALTER TABLE <table> ADD COLUMN <column>`, which is also why
		// re-running it repairs rather than clobbers. Bind the table so a
		// sentinel naming a column the series only adds to some other table
		// (or never adds at all) fails here rather than in the field.
		adder := findVersion(regexp.MustCompile(`(?i)ALTER TABLE\s+` + regexp.QuoteMeta(sc.table) +
			`\s+ADD COLUMN\s+` + regexp.QuoteMeta(sc.column)))
		// ignored/0012 builds the carrier table as __temp__<name> and renames
		// it into place only when <name> is absent. The single COLUMNS probe
		// cannot tell "table missing" from "column missing", so the replay has
		// to be able to heal both.
		creator := findVersion(regexp.MustCompile(regexp.QuoteMeta("__temp__" + sc.table)))
		if adder == 0 {
			problems = append(problems, fmt.Sprintf(
				"sentinel column %s.%s is never added by the %s series; re-running it could not repair that column",
				sc.table, sc.column, ignoredSource.dir))
			continue
		}
		if creator == 0 {
			problems = append(problems, fmt.Sprintf(
				"carrier table %s for sentinel column %s.%s is never created by the %s series",
				sc.table, sc.table, sc.column, ignoredSource.dir))
			continue
		}
		if sc.replayFloor < unguarded {
			problems = append(problems, fmt.Sprintf(
				"sentinel %s.%s replayFloor %d does not exclude ignored/%04d, whose unguarded UPDATE restamps wisps.updated_at",
				sc.table, sc.column, sc.replayFloor, unguarded))
		}

		// The SQL path: both the adder and the carrier's creator sit above
		// the floor, so the clamped replay heals the column on its own.
		if adder > sc.replayFloor && creator > sc.replayFloor {
			continue
		}
		// The healer path (gh 5257): the SQL creator sits at or below the
		// floor, so a registered pre-migration repair above the floor must
		// restore the column in code, and the carrier must be a floor-0
		// sentinel table so the table-absent shape the column probe collapses
		// still floors to zero and replays the carrier's creator.
		_, healerVersion := registeredColumnHealer(sc, repairs)
		if healerVersion <= sc.replayFloor {
			problems = append(problems, fmt.Sprintf(
				"sentinel column %s.%s is added by ignored/%04d, at or below its replayFloor %d, and no pre-migration repair above that floor restores it; the clamped replay would skip every migration that heals it",
				sc.table, sc.column, adder, sc.replayFloor))
		}
		if !slices.Contains(ignoredSource.sentinelTables, sc.table) {
			problems = append(problems, fmt.Sprintf(
				"carrier table %s for healer-backed sentinel %s.%s is not a floor-0 sentinel table; a store missing the table entirely would never be repaired",
				sc.table, sc.table, sc.column))
		}
	}
	return problems
}

// registeredColumnHealer reports the lowest pre-migration repair version
// strictly above sc.replayFloor whose function actually restores sc's column,
// proved by exercising it against a missing-column fixture: the carrier table
// exists, the column does not, and the guarded ADD COLUMN the healer must
// issue fails with a distinctive marker. Returns (nil, 0) when no registered
// repair above the floor restores the column.
func registeredColumnHealer(sc schemaSentinelColumn, repairs map[repairKey]func(context.Context, DBConn) error) (func(context.Context, DBConn) error, int) {
	type candidate struct {
		version int
		repair  func(context.Context, DBConn) error
	}
	var candidates []candidate
	for key, repair := range repairs {
		if key.cursorTable == ignoredSource.cursorTable && key.version > sc.replayFloor {
			candidates = append(candidates, candidate{version: key.version, repair: repair})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].version < candidates[j].version })
	for _, c := range candidates {
		db, mock, err := sqlmock.New()
		if err != nil {
			continue
		}
		// The fixture: carrier present, column absent. The marker error is
		// the proof the repair reached its guarded ADD COLUMN.
		marker := errors.New("healer issued the guarded add column")
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
			WithArgs(sc.table).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS`).
			WithArgs(sc.table, sc.column).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectExec(regexp.QuoteMeta("ALTER TABLE " + sc.table + " ADD COLUMN " + sc.column)).
			WillReturnError(marker)
		err = c.repair(context.Background(), db)
		_ = db.Close()
		if errors.Is(err, marker) {
			return c.repair, c.version
		}
	}
	return nil, 0
}

// TestSentinelFlooredTablesAreCreatedByTheSeries is the floored-table
// analogue of TestSentinelColumnsAreCreatedByTheSeries: every sentinel in
// sentinelFlooredTables must be created by the series, strictly above its own
// replayFloor, and that floor must exclude ignored/0007's unguarded restamp
// the same way the column floor does.
//
// events does not use the __temp__<table> + RENAME pattern the other
// creators do: it carries the named, database-unique constraint
// fk_events_issue, so a temp twin errors "duplicate foreign key constraint
// name" on Dolt 2.2.2 (see ignored/0019's own comment). Its creator guards a
// plain CREATE TABLE behind a PREPAREd conditional on an INFORMATION_SCHEMA
// existence probe instead, so both creation shapes are detected here.
func TestSentinelFlooredTablesAreCreatedByTheSeries(t *testing.T) {
	if len(ignoredSource.sentinelFlooredTables) == 0 {
		t.Fatal("ignoredSource has no sentinel floored tables; the events/bd_events_journal/bd_events_seq guard is inert")
	}

	bodies := make(map[int]string)
	for _, mf := range ignoredSource.list() {
		blob, err := ignoredSource.files.ReadFile(ignoredSource.dir + "/" + mf.name)
		if err != nil {
			t.Fatalf("read %s: %v", mf.name, err)
		}
		bodies[mf.version] = string(blob)
	}

	// findVersion returns the lowest migration version whose body matches, or
	// 0 when nothing does.
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

	unguarded := findVersion(regexp.MustCompile(`(?i)UPDATE\s+wisps\s+SET\s+is_blocked\s*=\s*0\s*;`))
	if unguarded == 0 {
		t.Fatal("the unguarded `UPDATE wisps SET is_blocked = 0` the replay floor exists to exclude is gone; re-derive the floor")
	}

	for _, st := range ignoredSource.sentinelFlooredTables {
		// Detect every creation shape and accept whichever the series actually
		// uses, rather than keying on the table's name: the PREPAREd
		// conditional CREATE is required of any creator whose table carries a
		// named, database-unique constraint (ignored/0019's own comment), so a
		// future floored sentinel can legitimately arrive in either shape and
		// must not fall through to the misleading "never created" failure.
		// findVersion already returns the lowest match, so take the earlier of
		// the two shapes if a table somehow uses both.
		prepared := findVersion(regexp.MustCompile(
			`(?i)IF\(@\w+\s*=\s*0,\s*'CREATE TABLE ` + regexp.QuoteMeta(st.table) + `\s*\(`))
		temp := findVersion(regexp.MustCompile(regexp.QuoteMeta("__temp__" + st.table)))
		// The third shape: a plain IF NOT EXISTS create, which is enough for a
		// table carrying no named constraint (ignored/0030).
		ifNotExists := findVersion(regexp.MustCompile(
			`(?i)CREATE TABLE IF NOT EXISTS ` + regexp.QuoteMeta(st.table) + `\s*\(`))
		creator := prepared
		for _, candidate := range []int{temp, ifNotExists} {
			if creator == 0 || (candidate != 0 && candidate < creator) {
				creator = candidate
			}
		}
		if creator == 0 {
			t.Errorf("sentinel floored table %q is never created by the %s series; re-running it could not repair that table",
				st.table, ignoredSource.dir)
			continue
		}
		if creator <= st.replayFloor {
			t.Errorf("sentinel floored table %q is created by ignored/%04d, at or below its replayFloor %d; the clamped replay would skip the migration that heals it",
				st.table, creator, st.replayFloor)
		}
		if st.replayFloor < unguarded {
			t.Errorf("sentinel floored table %q replayFloor %d does not exclude ignored/%04d, whose unguarded UPDATE restamps wisps.updated_at",
				st.table, st.replayFloor, unguarded)
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
	expectShowTable(mock, ignoredSource.sentinelTables[0], false)

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
	for _, table := range ignoredSource.sentinelTables {
		expectShowTable(mock, table, true)
	}
	// The floored-table sentinels probe the same way and must all corroborate
	// too, ahead of the column probe below.
	for _, st := range ignoredSource.sentinelFlooredTables {
		expectShowTable(mock, st.table, true)
	}
	// leases is present; only its granted_node column is missing.
	for _, sc := range ignoredSource.sentinelColumns {
		expectSentinelColumnProbe(mock, sc, true, false)
	}

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

// TestMigrationWorkNeededWhenWispIsBlockedAbsent is gh 5257 end to end through
// the MigrateUp short-circuit: both cursors read at-latest, every table
// sentinel (including wisps and wisp_dependencies) corroborates them, the
// leases column does too — and only wisps.is_blocked is missing. Before the
// column sentinel, migrationWorkNeeded returned false right there, the
// ignored series never re-ran, and the missing column surfaced much later as
// `table "p" does not have column "is_blocked"` on every blocked-state write.
func TestMigrationWorkNeededWhenWispIsBlockedAbsent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	for _, table := range ignoredSource.sentinelTables {
		expectShowTable(mock, table, true)
	}
	for _, st := range ignoredSource.sentinelFlooredTables {
		expectShowTable(mock, st.table, true)
	}
	// leases.granted_node corroborates the cursor; wisps is present with its
	// is_blocked column missing (an empty SHOW COLUMNS result set).
	missing := map[string]bool{"wisps.is_blocked": true}
	for _, sc := range ignoredSource.sentinelColumns {
		expectSentinelColumnProbe(mock, sc, true, !missing[sc.table+"."+sc.column])
	}

	needed, err := migrationWorkNeeded(context.Background(), db)
	if err != nil {
		t.Fatalf("migrationWorkNeeded: %v", err)
	}
	if !needed {
		t.Fatal("migrationWorkNeeded = false with an at-latest cursor and missing wisps.is_blocked; the pre-0015 repair would never run (gh 5257)")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet ordered sentinel probes: %v", err)
	}
}

// TestIgnoredCursorReadWithLeasesAbsentIssuesNoFailingStatement is the
// missing-carrier shape end to end through the guarded cursor read every
// write-capable open performs. Every released tree (v1.1.0, v1.1.2, v1.2.x)
// has no leases table at all, and an out-of-band copy can arrive at-latest
// without it. The ordered mock admits exactly the carrier's existence read for
// the column sentinel: a SHOW COLUMNS sent to the absent table is unexpected,
// and currentVersion surfaces that rather than swallowing it the way atLatest
// does, so this fails if the probe ever issues the statement that pins a pooled
// Dolt session (be-bv7x).
func TestIgnoredCursorReadWithLeasesAbsentIssuesNoFailingStatement(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	for _, table := range ignoredSource.sentinelTables {
		expectShowTable(mock, table, true)
	}
	for _, st := range ignoredSource.sentinelFlooredTables {
		expectShowTable(mock, st.table, true)
	}
	for _, sc := range ignoredSource.sentinelColumns {
		expectSentinelColumnProbe(mock, sc, false, false)
	}

	got, err := ignoredSource.currentVersion(context.Background(), db)
	if err != nil {
		t.Fatalf("currentVersion with leases absent: %v", err)
	}
	if want := 11; got != want {
		t.Errorf("currentVersion = %d, want the leases.granted_node replayFloor %d", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet ordered sentinel probes: %v", err)
	}
}

// TestSentinelColumnProbeNeverAsksAMissingTable pins the probe mechanism behind
// the column sentinel, through the real sentinelColumnExists and a stubbed
// sentinelTableExists. SHOW COLUMNS errors on a missing table, and with the
// store's multiStatements DSN a failing statement leaves the pooled Dolt
// session pinned to its pre-statement catalog, so tables that other
// connections create afterwards stay invisible to it (be-bv7x; reproduced on
// dolt 2.1.8 and 2.2.0). The carrier table must therefore be established by
// the existence probe, which answers an empty set rather than an error, before
// any SHOW COLUMNS is sent. The mock has no expectations: any statement at all
// fails the probe.
func TestSentinelColumnProbeNeverAsksAMissingTable(t *testing.T) {
	if len(ignoredSource.sentinelColumns) == 0 {
		t.Fatal("ignoredSource has no sentinel columns; this guard is inert")
	}
	// Every carrier must read absent: wisps is itself a sentinel table (and so
	// present in allSentinelTables), but this test's contract is the probe's
	// behavior when the carrier is GONE — the shape a floor-0 table-sentinel
	// contradiction floors on before any column probe runs.
	present := allSentinelTables()
	for _, sc := range ignoredSource.sentinelColumns {
		delete(present, sc.table)
	}
	probed := stubSentinelTables(t, present, nil)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	for _, sc := range ignoredSource.sentinelColumns {
		got, err := sentinelColumnExists(context.Background(), db, sc.table, sc.column)
		if err != nil {
			t.Fatalf("sentinelColumnExists(%s.%s) with the table absent: %v", sc.table, sc.column, err)
		}
		if got {
			t.Errorf("sentinelColumnExists(%s.%s) = true with the table absent", sc.table, sc.column)
		}
		if !slices.Contains(*probed, sc.table) {
			t.Errorf("carrier table %s was never probed for existence; probed %v", sc.table, *probed)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestSentinelColumnProbeReadsAPresentTableExactly covers the other half: once
// the carrier exists, SHOW COLUMNS answers, and its Field is compared exactly
// because '_' is a LIKE single-character wildcard.
func TestSentinelColumnProbeReadsAPresentTableExactly(t *testing.T) {
	present := allSentinelTables()
	for _, sc := range ignoredSource.sentinelColumns {
		present[sc.table] = true
	}
	probed := stubSentinelTables(t, present, nil)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	for _, sc := range ignoredSource.sentinelColumns {
		sc := sc
		t.Run(sc.table+"."+sc.column, func(t *testing.T) {
			showColumns := regexp.QuoteMeta("SHOW COLUMNS FROM " + sc.table + " LIKE '" + sc.column + "'")
			columns := []string{"Field", "Type", "Null", "Key", "Default", "Extra"}
			// A LIKE single-character-wildcard lookalike (grantedXnode for
			// granted_node), then an empty set, then the exact name.
			lookalike := strings.Replace(sc.column, "_", "X", 1)
			mock.ExpectQuery(showColumns).WillReturnRows(sqlmock.NewRows(columns).AddRow(lookalike, "varchar(255)", "YES", "", nil, ""))
			mock.ExpectQuery(showColumns).WillReturnRows(sqlmock.NewRows(columns))
			mock.ExpectQuery(showColumns).WillReturnRows(sqlmock.NewRows(columns).AddRow(sc.column, "varchar(255)", "YES", "", nil, ""))

			for i, want := range []bool{false, false, true} {
				got, err := sentinelColumnExists(context.Background(), db, sc.table, sc.column)
				if err != nil {
					t.Fatalf("probe %d: sentinelColumnExists: %v", i, err)
				}
				if got != want {
					t.Errorf("probe %d: sentinelColumnExists = %v, want %v", i, got, want)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
	if want := len(ignoredSource.sentinelColumns) * 3; len(*probed) != want {
		t.Errorf("carrier existence probes = %d, want %d (one ahead of every SHOW COLUMNS)", len(*probed), want)
	}
}

// A failed carrier probe must surface, not read as "column absent": that would
// clamp a healthy at-latest cursor to the column's replayFloor and replay the
// tail on a catalog nobody could read — the same rule
// TestCursorProbeErrorIsNotTreatedAsContradicted pins for the table sentinels.
func TestSentinelColumnCarrierProbeErrorIsNotTreatedAsContradicted(t *testing.T) {
	boom := errors.New("information_schema unavailable")
	present := allSentinelTables()
	orig := sentinelTableExists
	t.Cleanup(func() { sentinelTableExists = orig })
	sentinelTableExists = func(_ context.Context, _ DBConn, table string) (bool, error) {
		for _, sc := range ignoredSource.sentinelColumns {
			if table == sc.table {
				return false, boom
			}
		}
		return present[table], nil
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	_, limited, err := ignoredSource.cursorRealityFloor(context.Background(), db)
	if !errors.Is(err, boom) {
		t.Errorf("carrier probe error was swallowed: err = %v", err)
	}
	if limited {
		t.Error("an unreadable carrier probe reported the cursor as contradicted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
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
	expectShowTable(mock, ignoredSource.sentinelTables[0], false)

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
	for _, table := range ignoredSource.sentinelTables {
		expectShowTable(mock, table, true)
	}
	// The floored-table sentinels probe the same way and must all corroborate
	// too, ahead of the column probe below.
	for _, st := range ignoredSource.sentinelFlooredTables {
		expectShowTable(mock, st.table, true)
	}
	for _, sc := range ignoredSource.sentinelColumns {
		expectSentinelColumnProbe(mock, sc, true, false)
	}

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

// TestMigrateResumesAtTheWispIsBlockedHealer is the gh 5257 applier-side half:
// with only wisps.is_blocked missing behind an at-latest cursor, the replay
// must resume at ignored/0015 — whose registered pre-migration repair
// (ensureWispIsBlockedForRecompute) re-adds the column and its index in code —
// and never reach 0006 or 0007 (a floor of 5 would re-run 0006 itself, but it
// would drag frozen 0007's unguarded recompute into every heal; see
// TestCurrentVersionClampsToSentinelFloor). The mock walks migrate() to the
// first statement the heal applies and fails it distinctively; attribution to
// 0015, not 0006/0007, is the proof.
func TestMigrateResumesAtTheWispIsBlockedHealer(t *testing.T) {
	expectGuardedRead := func(t *testing.T, mock sqlmock.Sqlmock) {
		t.Helper()
		mock.ExpectExec(regexp.QuoteMeta("CREATE TABLE IF NOT EXISTS ignored_schema_migrations")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta("SHOW COLUMNS FROM ignored_schema_migrations LIKE 'content_hash'")).
			WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
				AddRow("content_hash", "char(64)", "YES", "", nil, ""))
		expectCursorProbe(mock, "ignored_schema_migrations", true)
		expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
		for _, table := range ignoredSource.sentinelTables {
			expectShowTable(mock, table, true)
		}
		for _, st := range ignoredSource.sentinelFlooredTables {
			expectShowTable(mock, st.table, true)
		}
		missing := map[string]bool{"wisps.is_blocked": true}
		for _, sc := range ignoredSource.sentinelColumns {
			expectSentinelColumnProbe(mock, sc, true, !missing[sc.table+"."+sc.column])
		}
	}

	t.Run("healer restores the column before 0015 runs", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		expectGuardedRead(t, mock)

		// The repair registered before 0015 runs first: wisps exists, the
		// column does not, the index does not — so both guarded statements
		// fire in code, exactly mirroring ignored/0006's own two.
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
			WithArgs("wisps").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS`).
			WithArgs("wisps", "is_blocked").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectExec(regexp.QuoteMeta("ALTER TABLE wisps ADD COLUMN is_blocked")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.STATISTICS`).
			WithArgs("wisps", "idx_wisps_is_blocked").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectExec(regexp.QuoteMeta("CREATE INDEX idx_wisps_is_blocked ON wisps")).
			WillReturnResult(sqlmock.NewResult(0, 0))

		// Then 0015's own body starts; fail its first statement distinctively.
		boom := errors.New("stop here: the healer ran and 0015 applied")
		mock.ExpectExec("UPDATE wisps SET is_blocked = 0").WillReturnError(boom)

		_, _, err = ignoredSource.migrate(context.Background(), db, 0)
		if err == nil || !errors.Is(err, boom) {
			t.Fatalf("migrate err = %v, want the injected failure inside ignored/0015", err)
		}
		if !strings.Contains(err.Error(), "0015_recompute") {
			t.Errorf("replay did not resume at the 0015 heal: %v", err)
		}
		if strings.Contains(err.Error(), "0006_") || strings.Contains(err.Error(), "0007_") {
			t.Errorf("replay reached the pre-floor files 0006/0007; 0007's unguarded UPDATE would restamp wisps.updated_at: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("a failing repair query propagates with 0015 context", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		expectGuardedRead(t, mock)

		repairBoom := errors.New("information_schema unavailable")
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
			WithArgs("wisps").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS`).
			WithArgs("wisps", "is_blocked").
			WillReturnError(repairBoom)

		_, _, err = ignoredSource.migrate(context.Background(), db, 0)
		if !errors.Is(err, repairBoom) {
			t.Fatalf("migrate err = %v, want the repair's own failure to propagate", err)
		}
		if !strings.Contains(err.Error(), "pre-repair for migration 0015_") {
			t.Errorf("repair failure not attributed to ignored/0015: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
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

// TestShowTableExistsAnswersLikeInformationSchema pins showTableExists to the
// INFORMATION_SCHEMA.TABLES count it replaced. Dolt's SHOW TABLES LIKE filter
// matches case-sensitively, while INFORMATION_SCHEMA — like every table
// reference — resolves names case-insensitively, so a LIKE-filtered probe
// reads a table stored as BD_EVENTS_SEQ as absent (dolt 2.1.8 and 2.2.0). The
// probe therefore reads the unfiltered listing and compares names
// case-insensitively and otherwise exactly: a '_' lookalike is not the table,
// and an empty listing is an absent table rather than an error.
func TestShowTableExistsAnswersLikeInformationSchema(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	cases := []struct {
		name   string
		tables []string
		probe  string
		want   bool
	}{
		{name: "a LIKE lookalike is not the table", tables: []string{"issues", "wispxdependencies"}, probe: "wisp_dependencies", want: false},
		{name: "a name containing the table is not the table", tables: []string{"bd_events_journal", "bd_events_seq"}, probe: "events", want: false},
		{name: "empty database", tables: nil, probe: "wisp_dependencies", want: false},
		{name: "exact name among others", tables: []string{"issues", "wisp_dependencies", "wisps"}, probe: "wisp_dependencies", want: true},
		{name: "stored in another case", tables: []string{"issues", "BD_EVENTS_SEQ"}, probe: "bd_events_seq", want: true},
	}
	for _, tc := range cases {
		rows := sqlmock.NewRows([]string{"Tables_in_testdb"})
		for _, table := range tc.tables {
			rows.AddRow(table)
		}
		mock.ExpectQuery("^SHOW TABLES$").WillReturnRows(rows)
	}
	for _, tc := range cases {
		got, err := showTableExists(context.Background(), db, tc.probe)
		if err != nil {
			t.Fatalf("%s: showTableExists: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: showTableExists(%q) = %v, want %v", tc.name, tc.probe, got, tc.want)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestCurrentVersionWithoutAUsableDatabaseIssuesNoFailingStatement is the
// be-bv7x rule for the cursor probe itself, which must succeed on any session.
// SHOW TABLES fails with no database selected (1105) and on a session whose
// database was dropped under it (1049), and a failing statement pins the
// pooled session to its pre-statement catalog. The schemata read answers 0 in
// both cases instead, so the probe stops there and reads the cursor as absent,
// exactly as the INFORMATION_SCHEMA.TABLES count did: the strict mock admits
// nothing after it.
func TestCurrentVersionWithoutAUsableDatabaseIssuesNoFailingStatement(t *testing.T) {
	for _, src := range []migrationSource{mainSource, ignoredSource} {
		t.Run(src.cursorTable, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = DATABASE()")).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

			got, err := src.currentVersion(context.Background(), db)
			if err != nil {
				t.Fatalf("currentVersion without a usable database: %v", err)
			}
			if got != 0 {
				t.Errorf("currentVersion without a usable database = %d, want 0", got)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestCurrentVersionDisbelievesCursorWhenWispIsBlockedAbsent is the gh 5257
// repro: a fully-up-to-date ignored cursor whose wisps.is_blocked column is
// missing. Before the fix the cursor was trusted wholesale, the series looked
// fully applied, and the missing column went on poisoning every
// is_blocked-involving read and write on the store. After it, the missing
// column contradicts the cursor down to is_blocked's replay floor (14), so
// the next open replays exactly the 0015–latest tail, whose pre-0015 repair
// re-adds the column and index in code. The floor keeps frozen 0007's
// unguarded `UPDATE wisps SET is_blocked = 0` — the wisp-restamp regression
// of #5981 — out of that replay. Exactly 14: any lower answer re-runs
// migrations whose restamping no history can undo, any higher skips the
// repair that restores the column.
//
// The triage repro pinned `currentVersion <= 5` (cursor zeroed, whole series
// replayed); this test pins the approved design's floor instead — the whole-
// series replay is precisely what the leases.granted_node floor-11 precedent
// exists to avoid.
func TestCurrentVersionDisbelievesCursorWhenWispIsBlockedAbsent(t *testing.T) {
	stubSentinelTables(t, allSentinelTables(), nil)
	stubSentinelColumns(t, allSentinelColumnsExcept("wisps.is_blocked"), nil)
	db := mockIgnoredCursorRead(t, LatestIgnoredVersion())

	got, err := ignoredSource.currentVersion(context.Background(), db)
	if err != nil {
		t.Fatalf("currentVersion: %v", err)
	}
	if got != 14 {
		t.Errorf("currentVersion with wisps.is_blocked absent = %d, want exactly 14 (is_blocked's replay floor)", got)
	}
}
