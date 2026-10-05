//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// Shape convergence for bd_events_journal (BEADS-JOURNAL-PLAN.md §4.2b/§4.3,
// PR A2). ignored/0028 closes the exact gap ignored/0023 left open (probing
// DATA_TYPE alone cannot see a column that is entirely absent): these tests
// reproduce every historical shape the plan's evidence base found, prove each
// one converges on canonical without perturbing a single row or the seq
// counter (T2.5), pin the sentinel-triggered replay's floor at exactly 21
// (T2.6), and pin that the same replay is a true no-op everywhere outside
// bd_events_journal itself when nothing else is damaged (T2.7).

// newShape3666e5026Store reproduces the events-journal-3666e5026 release
// shape (BEADS-JOURNAL-PLAN.md §1.1, "where that shape came from"):
// issue_json LONGTEXT, dep_json TEXT, no comment_json, no actor, no ts index.
// This predates both comment_json and actor outright.
func newShape3666e5026Store(t *testing.T, prefix string) *testEnv {
	t.Helper()
	te := newTestEnv(t, prefix)
	ctx := context.Background()
	te.exec(t, ctx, "ALTER TABLE bd_events_journal DROP COLUMN comment_json")
	te.exec(t, ctx, "ALTER TABLE bd_events_journal DROP COLUMN actor")
	te.exec(t, ctx, "ALTER TABLE bd_events_journal MODIFY COLUMN dep_json TEXT")
	te.exec(t, ctx, "DROP INDEX idx_bd_events_journal_ts ON bd_events_journal")
	return te
}

// newForkIgnored0017ShapeStore reproduces the enterprise fork's
// ignored/0017_create_events_journal shape (0023's own header, bd-t9ovd):
// both payload columns TEXT, no actor (actor postdates this shape; it is
// bd OSS migration 0025), no ts index. ignored/0023 already fully heals this
// one — dropping actor is what forces the sentinel-triggered replay that
// exercises 0023's widen-if-TEXT step here, the same mechanism a lineage
// that genuinely never ran 0023/0025 before reaching this shape would hit.
func newForkIgnored0017ShapeStore(t *testing.T, prefix string) *testEnv {
	t.Helper()
	te := newTestEnv(t, prefix)
	ctx := context.Background()
	te.exec(t, ctx, "ALTER TABLE bd_events_journal DROP COLUMN actor")
	te.exec(t, ctx, "ALTER TABLE bd_events_journal MODIFY COLUMN dep_json TEXT")
	te.exec(t, ctx, "ALTER TABLE bd_events_journal MODIFY COLUMN comment_json TEXT")
	te.exec(t, ctx, "DROP INDEX idx_bd_events_journal_ts ON bd_events_journal")
	return te
}

// openDB opens a short-lived raw SQL connection to te's store and registers
// its cleanup with t.Cleanup, for tests that need to hold the connection
// across several calls (schema.CurrentIgnoredVersion, schema.MigrateUp,
// direct probes) rather than te.exec's one-shot open/close.
func (te *testEnv) openDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })
	return db
}

// journalCoreRow is the subset of bd_events_journal's columns every observed
// shape (canonical or historical) carries. comment_json and actor are
// deliberately excluded: they are exactly what this migration adds back, so
// a before/after comparison that is scoped to the columns every shape shares
// is what proves the healing touched only the columns it should.
type journalCoreRow struct {
	Seq       int64
	Ts        string
	Op        string
	IssueID   string
	IssueJSON sql.NullString
	DepJSON   sql.NullString
}

// snapshotJournalCore captures every bd_events_journal row (core columns
// only, see journalCoreRow) in seq order, plus bd_events_seq's counter.
func snapshotJournalCore(t *testing.T, ctx context.Context, db *sql.DB) ([]journalCoreRow, int64) {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT seq, ts, op, issue_id, issue_json, dep_json FROM bd_events_journal ORDER BY seq")
	if err != nil {
		t.Fatalf("snapshot bd_events_journal: %v", err)
	}
	defer rows.Close()
	var out []journalCoreRow
	for rows.Next() {
		var r journalCoreRow
		if err := rows.Scan(&r.Seq, &r.Ts, &r.Op, &r.IssueID, &r.IssueJSON, &r.DepJSON); err != nil {
			t.Fatalf("scan bd_events_journal row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate bd_events_journal: %v", err)
	}
	var nextSeq int64
	if err := db.QueryRowContext(ctx, "SELECT next_seq FROM bd_events_seq WHERE id = 0").Scan(&nextSeq); err != nil {
		t.Fatalf("snapshot bd_events_seq: %v", err)
	}
	return out, nextSeq
}

// assertIndexExists fails the test if the named index is missing.
func assertIndexExists(t *testing.T, ctx context.Context, db *sql.DB, table, index string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?`,
		table, index).Scan(&count); err != nil {
		t.Fatalf("probe index %s on %s: %v", index, table, err)
	}
	if count == 0 {
		t.Errorf("%s: index %s missing after convergence", table, index)
	}
}

// assertColumnShapeConverged diffs got against want (both from
// clonePlaneTableShape, migrate_ignored_plane_shape_test.go) with messaging
// specific to this file's "did this fixture converge on canonical" question,
// rather than that helper's own fresh-init-vs-fresh-clone framing.
func assertColumnShapeConverged(t *testing.T, fixtureName, table string, want, got map[string]clonePlaneColumn) {
	t.Helper()
	names := map[string]bool{}
	for name := range want {
		names[name] = true
	}
	for name := range got {
		names[name] = true
	}
	for name := range names {
		w, inWant := want[name]
		g, inGot := got[name]
		switch {
		case !inGot:
			t.Errorf("%s: %s.%s present on the canonical reference but missing after convergence", fixtureName, table, name)
		case !inWant:
			t.Errorf("%s: %s.%s present after convergence but not on the canonical reference", fixtureName, table, name)
		case w != g:
			t.Errorf("%s: %s.%s shape differs from canonical: want %s, got %s",
				fixtureName, table, name, formatClonePlaneColumn(w), formatClonePlaneColumn(g))
		}
	}
}

// TestIgnored0028ConvergesAllShapes is T2.5: every historical shape the
// plan's evidence base found ends up byte-identical to canonical after
// MigrateUp, and not a single row or the seq counter moved getting there.
// Kills: probing only DATA_TYPE (0023's gap — a dropped fixture here would
// stay missing comment_json/actor and fail the shape diff); any non-monotone
// write to the counter or rows (caught by the before/after snapshot; monotone
// writes are TestIgnored0028AloneLeavesCounterAndRowsUntouched's job — this
// test's forced replay always re-runs ignored/0022 too, whose own GREATEST
// raise would mask one).
func TestIgnored0028ConvergesAllShapes(t *testing.T) {
	// dbPrefix is separate from name (rather than derived from it): embedded
	// mode database names reject hyphens, but "fork-ignored-0017" is the
	// plan's own name for that fixture and reads better in -v output and
	// failure messages than a mangled version of it would.
	fixtures := []struct {
		name     string
		dbPrefix string
		build    func(t *testing.T, prefix string) *testEnv
		damaged  bool
	}{
		{name: "3666e5026", dbPrefix: "cv3666e5026", build: newShape3666e5026Store, damaged: true},
		{name: "gci", dbPrefix: "cvgci", build: newGciShapeStore, damaged: true},
		{name: "fork-ignored-0017", dbPrefix: "cvfork0017", build: newForkIgnored0017ShapeStore, damaged: true},
		{name: "canonical", dbPrefix: "cvcanon", build: func(t *testing.T, prefix string) *testEnv { return newTestEnv(t, prefix) }, damaged: false},
	}

	ctx := context.Background()
	reference := newTestEnv(t, "cvref")
	refDB := reference.openDB(t, ctx)
	refConn, err := refDB.Conn(ctx)
	if err != nil {
		t.Fatalf("reference Conn: %v", err)
	}
	defer refConn.Close()
	wantShape := clonePlaneTableShape(t, ctx, refConn, "bd_events_journal")

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			te := fx.build(t, fx.dbPrefix)

			// One real journaled event while the shape is still canonical (every
			// builder above damages bd_events_journal only AFTER newTestEnv
			// returns), so the snapshot below compares real data, not an empty
			// table.
			te.store.SetEventsJournalEnabled(true)
			if err := te.store.EventsJournalActivationError(); err != nil {
				t.Fatalf("activation before damaging the shape: %v", err)
			}
			issue := &types.Issue{Title: fx.name + " seed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("seed CreateIssue: %v", err)
			}

			db := te.openDB(t, ctx)
			beforeRows, beforeSeq := snapshotJournalCore(t, ctx, db)

			// MigrateUp's own return value cannot prove convergence here: it
			// reports only the main-plane applied count (plus a reconcile
			// flag), never the ignored-plane count, so the sentinel
			// contradiction and its heal are both checked through
			// CurrentIgnoredVersion instead (see TestSentinelJournalColumnsReplay).
			if fx.damaged {
				before, err := schema.CurrentIgnoredVersion(ctx, db)
				if err != nil {
					t.Fatalf("CurrentIgnoredVersion (before MigrateUp): %v", err)
				}
				if want := schema.LatestIgnoredVersion(); before >= want {
					t.Fatalf("CurrentIgnoredVersion before MigrateUp = %d, want below latest (%d): this fixture's damage did not float a sentinel contradiction", before, want)
				}
			}

			if _, err := schema.MigrateUp(ctx, db); err != nil {
				t.Fatalf("MigrateUp: %v", err)
			}

			if after, err := schema.CurrentIgnoredVersion(ctx, db); err != nil {
				t.Fatalf("CurrentIgnoredVersion (after MigrateUp): %v", err)
			} else if want := schema.LatestIgnoredVersion(); after != want {
				t.Fatalf("CurrentIgnoredVersion after MigrateUp = %d, want latest (%d): this fixture did not converge", after, want)
			}

			afterRows, afterSeq := snapshotJournalCore(t, ctx, db)
			if beforeSeq != afterSeq {
				t.Errorf("bd_events_seq.next_seq changed: before=%d after=%d", beforeSeq, afterSeq)
			}
			if !reflect.DeepEqual(beforeRows, afterRows) {
				t.Errorf("bd_events_journal rows changed across MigrateUp:\nbefore=%+v\nafter=%+v", beforeRows, afterRows)
			}

			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatalf("Conn: %v", err)
			}
			defer conn.Close()
			gotShape := clonePlaneTableShape(t, ctx, conn, "bd_events_journal")
			assertColumnShapeConverged(t, fx.name, "bd_events_journal", wantShape, gotShape)
			assertIndexExists(t, ctx, db, "bd_events_journal", "idx_bd_events_journal_ts")
			assertIndexExists(t, ctx, db, "bd_events_journal", "idx_bd_events_journal_issue")
		})
	}
}

// isolationRow is the row shape TestIgnored0028AloneLeavesCounterAndRowsUntouched
// compares before and after running ignored/0028 directly: the four columns
// every damaged fixture in that test shares, plus comment_json only on the
// one fixture (fork-ignored-0017) that still has it when 0028 starts.
type isolationRow struct {
	Seq                             int64
	Ts, Op, IssueID                 string
	IssueJSON, DepJSON, CommentJSON sql.NullString
}

// readIsolationRows reads isolationRows in seq order. comment_json is only
// selected when hasCommentJSON is true: on two of the three fixtures the
// column does not exist yet when this runs, so selecting it unconditionally
// would fail with "column not found" rather than compare anything.
func readIsolationRows(t *testing.T, ctx context.Context, conn *sql.Conn, hasCommentJSON bool) []isolationRow {
	t.Helper()
	query := "SELECT seq, ts, op, issue_id, issue_json, dep_json FROM bd_events_journal ORDER BY seq"
	if hasCommentJSON {
		query = "SELECT seq, ts, op, issue_id, issue_json, dep_json, comment_json FROM bd_events_journal ORDER BY seq"
	}
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("read bd_events_journal: %v", err)
	}
	defer rows.Close()
	var out []isolationRow
	for rows.Next() {
		var r isolationRow
		dest := []any{&r.Seq, &r.Ts, &r.Op, &r.IssueID, &r.IssueJSON, &r.DepJSON}
		if hasCommentJSON {
			dest = append(dest, &r.CommentJSON)
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatalf("scan bd_events_journal row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate bd_events_journal: %v", err)
	}
	return out
}

func assertIsolationRowsEqual(t *testing.T, before, after []isolationRow) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("journal row count = %d, want %d (0028 alone must not touch data)", len(after), len(before))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("journal row seq %d changed: before %+v, after %+v", before[i].Seq, before[i], after[i])
		}
	}
}

// TestIgnored0028AloneLeavesCounterAndRowsUntouched is T2.5b. T2.5 forces its
// replay through a sentinel contradiction, which always re-runs ignored/0022
// too (every fixture there drops actor), so a monotone GREATEST write to the
// counter by 0028 itself would be invisible: 0022's own identical GREATEST
// raise on the same pass would mask it. Running 0028's frozen bytes directly,
// with the counter seeded below MAX(seq), closes that gap: any counter write
// at all, monotone or not, shows. Kills: any counter or row write in 0028,
// including a monotone GREATEST.
func TestIgnored0028AloneLeavesCounterAndRowsUntouched(t *testing.T) {
	fixtures := []struct {
		name           string
		dbPrefix       string
		build          func(t *testing.T, prefix string) *testEnv
		hasCommentJSON bool
	}{
		{name: "3666e5026", dbPrefix: "i28a3666e5026", build: newShape3666e5026Store, hasCommentJSON: false},
		{name: "gci", dbPrefix: "i28agci", build: newGciShapeStore, hasCommentJSON: false},
		{name: "fork-ignored-0017", dbPrefix: "i28afork0017", build: newForkIgnored0017ShapeStore, hasCommentJSON: true},
	}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			te := fx.build(t, fx.dbPrefix)
			ctx := context.Background()

			db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
			if err != nil {
				t.Fatalf("OpenSQL: %v", err)
			}
			defer func() { _ = cleanup() }()
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatalf("Conn: %v", err)
			}
			defer conn.Close()

			seed := []int64{1, 2, 3}
			for _, seq := range seed {
				ts := tsForSeq(seq)
				query := "INSERT INTO bd_events_journal (seq, ts, op, issue_id, issue_json, dep_json) VALUES (?, ?, 'create', 'bd-i28a', ?, ?)"
				args := []any{seq, ts, `{"id":"bd-i28a"}`, `[]`}
				if fx.hasCommentJSON {
					query = "INSERT INTO bd_events_journal (seq, ts, op, issue_id, issue_json, dep_json, comment_json) VALUES (?, ?, 'create', 'bd-i28a', ?, ?, ?)"
					args = append(args, `{"text":"hi"}`)
				}
				if _, err := conn.ExecContext(ctx, query, args...); err != nil {
					t.Fatalf("seed journal row %d: %v", seq, err)
				}
			}
			execFrozenGuard(t, ctx, conn, "UPDATE bd_events_seq SET next_seq = 1 WHERE id = 0")
			rowsBefore := readIsolationRows(t, ctx, conn, fx.hasCommentJSON)
			if len(rowsBefore) != len(seed) {
				t.Fatalf("seeded journal rows = %d, want %d", len(rowsBefore), len(seed))
			}

			sqlText, err := schema.IgnoredMigrationSQL("0028_converge_events_journal_shape.up.sql")
			if err != nil {
				t.Fatalf("read ignored 0028 migration: %v", err)
			}
			execFrozenGuard(t, ctx, conn, sqlText)

			if got := scalarInt(t, ctx, conn, "SELECT next_seq FROM bd_events_seq WHERE id = 0"); got != 1 {
				t.Errorf("next_seq = %d after applying 0028 alone, want 1: 0028 must never write the counter, not even a monotone raise", got)
			}
			assertIsolationRowsEqual(t, rowsBefore, readIsolationRows(t, ctx, conn, fx.hasCommentJSON))

			for _, column := range []string{"comment_json", "dep_json"} {
				if got := journalColumnDataType(t, ctx, conn, column); got != "longtext" {
					t.Errorf("bd_events_journal.%s DATA_TYPE = %q after 0028, want %q", column, got, "longtext")
				}
			}
			requireColumnCount(t, ctx, conn, "bd_events_journal", "actor", 1)
			requireIndex(t, ctx, conn, "bd_events_journal", "idx_bd_events_journal_ts", 1)
			requireIndex(t, ctx, conn, "bd_events_journal", "idx_bd_events_journal_issue", 1)
		})
	}
}

// TestSentinelJournalColumnsReplay is T2.6: a store already at the latest
// ignored cursor that loses comment_json out of band (a backup restored
// between doors, or damage out of band) floors at exactly 21 and heals on the
// next writable open. MigrateUp's own return value cannot prove this: it
// reports only the main-plane applied count (plus a reconcile flag), never
// the ignored-plane count, so every assertion here goes through
// CurrentIgnoredVersion instead. Kills: no sentinel registered (the floor
// read would stay at latest instead of dropping to 21, caught by the
// exact-21 assertion below); a floor of 22 or more (same assertion — 22
// would mean 0022, bd_events_journal's own creator, never replays, which
// cannot heal a shape where the table already exists but a column does
// not); a replay that runs but does not actually add comment_json back
// (caught by the final post-heal probe).
func TestSentinelJournalColumnsReplay(t *testing.T) {
	te := newTestEnv(t, "sj")
	ctx := context.Background()
	db := te.openDB(t, ctx)

	before, err := schema.CurrentIgnoredVersion(ctx, db)
	if err != nil {
		t.Fatalf("CurrentIgnoredVersion (before damage): %v", err)
	}
	if want := schema.LatestIgnoredVersion(); before != want {
		t.Fatalf("CurrentIgnoredVersion before damage = %d, want latest (%d)", before, want)
	}

	// Damage through the SAME connection db already holds open, not a second
	// embeddeddolt.OpenSQL handle (te.exec's own short-lived one): embedded
	// mode's engine-open path serializes on the directory, and a second
	// concurrent handle to it deadlocks against the first instead of simply
	// queuing.
	if _, err := db.ExecContext(ctx, "ALTER TABLE bd_events_journal DROP COLUMN comment_json"); err != nil {
		t.Fatalf("drop comment_json: %v", err)
	}

	floored, err := schema.CurrentIgnoredVersion(ctx, db)
	if err != nil {
		t.Fatalf("CurrentIgnoredVersion (after dropping comment_json): %v", err)
	}
	if floored != 21 {
		t.Fatalf("CurrentIgnoredVersion after dropping comment_json = %d, want exactly 21", floored)
	}

	if _, err := schema.MigrateUp(ctx, db); err != nil {
		t.Fatalf("MigrateUp (forced replay): %v", err)
	}

	healed, err := schema.CurrentIgnoredVersion(ctx, db)
	if err != nil {
		t.Fatalf("CurrentIgnoredVersion (after heal): %v", err)
	}
	if want := schema.LatestIgnoredVersion(); healed != want {
		t.Fatalf("CurrentIgnoredVersion after heal = %d, want latest (%d)", healed, want)
	}

	var commentType sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT DATA_TYPE FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bd_events_journal' AND COLUMN_NAME = 'comment_json'`,
	).Scan(&commentType); err != nil {
		t.Fatalf("probe comment_json after heal: %v", err)
	}
	if !commentType.Valid {
		t.Fatal("comment_json is still missing after the forced replay")
	}
}

// tsForSeq gives TestSentinelReplayCounterConverges's and
// TestIgnored0028AloneLeavesCounterAndRowsUntouched's seeded rows distinct,
// ordered timestamps without threading one more field through every fixture
// for a value no assertion in either test reads.
func tsForSeq(seq int64) string {
	return fmt.Sprintf("2026-08-01 00:00:%02d", seq)
}

// counterConvergeState is one bd_events_journal/bd_events_seq relationship
// TestSentinelReplayCounterConverges seeds: the rows to leave in the journal,
// the counter value to seed, and the counter the sentinel-triggered replay
// must converge to (GREATEST(before, MAX(seq))).
type counterConvergeState struct {
	name   string
	code   string // short, hyphen/underscore-free token for database name prefixes
	seqs   []int64
	before int64
	want   int64
}

var counterConvergeStates = []counterConvergeState{
	{name: "behind", code: "behind", seqs: []int64{1, 2, 3}, before: 1, want: 3},
	{name: "equal", code: "equal", seqs: []int64{1, 2, 3}, before: 3, want: 3},
	{name: "ahead_journal_restored_older", code: "ajro", seqs: []int64{1, 2, 3}, before: 10, want: 10},
	{name: "empty_journal_counter_ahead", code: "ejca", seqs: nil, before: 7, want: 7},
	{name: "hole_above_counter", code: "hac", seqs: []int64{1, 3}, before: 1, want: 3},
}

// TestSentinelReplayCounterConverges is T2.6b: across every counter/journal
// relationship #5496's evidence base found, a sentinel-triggered replay of
// ignored 22..28 must leave bd_events_seq at exactly
// GREATEST(seeded, MAX(seq)) — never lower, never past the high-water mark —
// and then the very next real write must land contiguously right after it.
// Run against both sentinel columns: dropping actor across all five states,
// plus dropping comment_json on "behind" alone to prove that sentinel also
// reaches the counter (it is registered on the same table, same floor).
//
// Kills: the sentinel floor sitting at 22 or above (behind stays at 1, and
// hole_above_counter's next write lands at the stale seq 2, a late arrival);
// a replayed statement that lowers the counter, e.g. a future
// `SET next_seq = COALESCE(MAX(seq), 0)` with no GREATEST (the ahead_* and
// empty_* cases fail); an over-raise such as MAX(seq)+1 (behind and equal
// read 4, and the first new row is 5, a gap); dropping the actor sentinel
// (actor/behind stays at 1 and the column is never restored).
func TestSentinelReplayCounterConverges(t *testing.T) {
	type subtest struct {
		name     string
		dbPrefix string
		state    counterConvergeState
		column   string
	}
	var subtests []subtest
	for _, state := range counterConvergeStates {
		subtests = append(subtests, subtest{
			name:     state.name + "/actor",
			dbPrefix: "scc" + state.code + "a",
			state:    state,
			column:   "actor",
		})
	}
	subtests = append(subtests, subtest{
		name:     "behind/comment_json",
		dbPrefix: "sccbehindc",
		state:    counterConvergeStates[0],
		column:   "comment_json",
	})

	for _, st := range subtests {
		t.Run(st.name, func(t *testing.T) {
			te := newTestEnv(t, st.dbPrefix)
			ctx := context.Background()

			// A raw handle with an explicit cleanup, not te.openDB: this test
			// closes it early (below) and reopens afterward, which te.openDB's
			// t.Cleanup-deferred-to-test-end handle cannot do.
			db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
			if err != nil {
				t.Fatalf("OpenSQL: %v", err)
			}
			closed := false
			closeDB := func() {
				if !closed {
					_ = cleanup()
					closed = true
				}
			}
			defer closeDB()

			if _, err := db.ExecContext(ctx, "DELETE FROM bd_events_journal"); err != nil {
				t.Fatalf("clear bd_events_journal: %v", err)
			}
			for _, seq := range st.state.seqs {
				if _, err := db.ExecContext(ctx,
					"INSERT INTO bd_events_journal (seq, ts, op, issue_id) VALUES (?, ?, 'create', 'bd-scc')",
					seq, tsForSeq(seq)); err != nil {
					t.Fatalf("seed journal row %d: %v", seq, err)
				}
			}
			if _, err := db.ExecContext(ctx, "UPDATE bd_events_seq SET next_seq = ? WHERE id = 0", st.state.before); err != nil {
				t.Fatalf("seed next_seq: %v", err)
			}
			//nolint:gosec // G201: st.column is one of two test-local literals ("actor", "comment_json").
			if _, err := db.ExecContext(ctx, "ALTER TABLE bd_events_journal DROP COLUMN "+st.column); err != nil {
				t.Fatalf("drop %s: %v", st.column, err)
			}

			if floored, err := schema.CurrentIgnoredVersion(ctx, db); err != nil {
				t.Fatalf("CurrentIgnoredVersion (after dropping %s): %v", st.column, err)
			} else if floored != 21 {
				t.Fatalf("CurrentIgnoredVersion after dropping %s = %d, want exactly 21", st.column, floored)
			}
			rowsBefore, _ := snapshotJournalCore(t, ctx, db)

			if _, err := schema.MigrateUp(ctx, db); err != nil {
				t.Fatalf("MigrateUp (forced replay): %v", err)
			}

			if after, err := schema.CurrentIgnoredVersion(ctx, db); err != nil {
				t.Fatalf("CurrentIgnoredVersion (after replay): %v", err)
			} else if want := schema.LatestIgnoredVersion(); after != want {
				t.Fatalf("CurrentIgnoredVersion after replay = %d, want latest (%d)", after, want)
			}
			var colCount int
			if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bd_events_journal' AND COLUMN_NAME = ?`, st.column).Scan(&colCount); err != nil {
				t.Fatalf("probe %s after replay: %v", st.column, err)
			}
			if colCount == 0 {
				t.Errorf("bd_events_journal.%s is still missing after the forced replay", st.column)
			}

			rowsAfter, nextSeq := snapshotJournalCore(t, ctx, db)
			if nextSeq != st.state.want {
				t.Errorf("next_seq = %d after replay, want %d (GREATEST(seeded %d, MAX(seq)))", nextSeq, st.state.want, st.state.before)
			}
			if !reflect.DeepEqual(rowsBefore, rowsAfter) {
				t.Errorf("bd_events_journal rows changed across the replay:\nbefore=%+v\nafter=%+v", rowsBefore, rowsAfter)
			}

			// Close the raw handle before any te.store call: embedded opens
			// serialize on the directory and a second concurrent handle
			// deadlocks against this one instead of queuing (see
			// TestStoreAtIgnored28WritesAndJournals).
			closeDB()

			te.store.SetEventsJournalEnabled(true)
			if err := te.store.EventsJournalActivationError(); err != nil {
				t.Fatalf("activation after convergence: %v", err)
			}
			issue := &types.Issue{Title: st.name + " seed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("CreateIssue after convergence: %v", err)
			}

			reopened := te.openDB(t, ctx)
			var minSeq, maxSeq, count int64
			if err := reopened.QueryRowContext(ctx,
				"SELECT COALESCE(MIN(seq),0), COALESCE(MAX(seq),0), COUNT(*) FROM bd_events_journal WHERE seq > ?",
				st.state.want).Scan(&minSeq, &maxSeq, &count); err != nil {
				t.Fatalf("probe new journal rows: %v", err)
			}
			if count == 0 {
				t.Fatal("CreateIssue after convergence did not journal a row above the converged counter")
			}
			if minSeq != st.state.want+1 {
				t.Errorf("lowest new seq = %d, want %d (want + 1)", minSeq, st.state.want+1)
			}
			if maxSeq-minSeq+1 != count {
				t.Errorf("new seqs are not contiguous: min=%d max=%d count=%d", minSeq, maxSeq, count)
			}
			if st.state.name == "hole_above_counter" {
				var staleCount int64
				if err := reopened.QueryRowContext(ctx, "SELECT COUNT(*) FROM bd_events_journal WHERE seq = 2").Scan(&staleCount); err != nil {
					t.Fatalf("probe seq 2: %v", err)
				}
				if staleCount != 0 {
					t.Errorf("seq 2 (the hole below the converged counter) has a row; a late arrival filled it instead of the new write landing above the counter")
				}
			}
		})
	}
}

// ignoredPlaneTablesExceptJournal lists every clone-local (dolt_ignore) table
// other than bd_events_journal itself, matching
// TestEmbeddedIgnoredSeriesConvergesWithFreshInitShape's table list
// (migrate_ignored_plane_shape_test.go). bd_events_journal's own shape
// legitimately changes during the replay this test forces; every one of
// these must not.
var ignoredPlaneTablesExceptJournal = []string{
	"wisps", "wisp_labels", "wisp_dependencies", "wisp_events",
	"wisp_comments", "wisp_child_counters", "events", "leases",
	"repo_mtimes", "local_metadata", "bd_events_seq",
}

// dumpTable renders every row of table as an order-stable, type-agnostic
// snapshot: each value read as text (or "<NULL>"), ordered by the table's
// first column. It exists so TestReplay22To28NoOpOnHealthyStore can compare
// whole tables it otherwise knows nothing about the schema of, rather than
// hand-maintaining a column list per table.
func dumpTable(t *testing.T, ctx context.Context, db *sql.DB, table string) []map[string]string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+table+" ORDER BY 1")
	if err != nil {
		t.Fatalf("dump %s: %v", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("dump %s columns: %v", table, err)
	}
	var out []map[string]string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("dump %s row: %v", table, err)
		}
		row := make(map[string]string, len(cols))
		for i, c := range cols {
			if vals[i].Valid {
				row[c] = vals[i].String
			} else {
				row[c] = "<NULL>"
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s: %v", table, err)
	}
	return out
}

func snapshotIgnoredPlaneTablesExceptJournal(t *testing.T, ctx context.Context, db *sql.DB) map[string][]map[string]string {
	t.Helper()
	snap := make(map[string][]map[string]string, len(ignoredPlaneTablesExceptJournal))
	for _, table := range ignoredPlaneTablesExceptJournal {
		snap[table] = dumpTable(t, ctx, db, table)
	}
	return snap
}

// TestReplay22To28NoOpOnHealthyStore is T2.7: forcing the sentinel-triggered
// replay of ignored 22..28 must perturb nothing outside bd_events_journal
// itself — not a single row in any other clone-local table, and critically
// not wisps.updated_at, which an unguarded statement in this range would
// restamp via ON UPDATE CURRENT_TIMESTAMP (the #5981 harm class the
// replay-floor's two placement constraints exist to prevent; see schema.go's
// schemaSentinelColumn doc comment). Kills: an unguarded statement above the
// floor touching a table or row it should not.
func TestReplay22To28NoOpOnHealthyStore(t *testing.T) {
	te := newTestEnv(t, "rp")
	ctx := context.Background()

	wispID := "wisp-rp-1"
	te.exec(t, ctx,
		"INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral) VALUES (?, 'w', '', '', '', '', 'open', 2, 'task', 1)",
		wispID)

	db := te.openDB(t, ctx)
	before := snapshotIgnoredPlaneTablesExceptJournal(t, ctx, db)

	// Force the replay the only way it ever happens in production: contradict
	// a sentinel, which floors the cursor to 21 and makes 22..28 pending again
	// (TestSentinelJournalColumnsReplay proves the floor and the heal; this
	// test only cares what else the resulting replay does or does not touch).
	// Damage through db itself, not a second embeddeddolt.OpenSQL handle: see
	// the matching comment in TestSentinelJournalColumnsReplay.
	if _, err := db.ExecContext(ctx, "ALTER TABLE bd_events_journal DROP COLUMN comment_json"); err != nil {
		t.Fatalf("drop comment_json: %v", err)
	}
	// MigrateUp's own return value cannot prove the replay ran: it reports
	// only the main-plane applied count, never the ignored-plane count (see
	// TestSentinelJournalColumnsReplay), so the contradiction and its heal are
	// both checked through CurrentIgnoredVersion instead.
	if floored, err := schema.CurrentIgnoredVersion(ctx, db); err != nil {
		t.Fatalf("CurrentIgnoredVersion (after damage): %v", err)
	} else if floored != 21 {
		t.Fatalf("CurrentIgnoredVersion after dropping comment_json = %d, want exactly 21", floored)
	}
	if _, err := schema.MigrateUp(ctx, db); err != nil {
		t.Fatalf("MigrateUp (forced 22..28 replay): %v", err)
	}
	if healed, err := schema.CurrentIgnoredVersion(ctx, db); err != nil {
		t.Fatalf("CurrentIgnoredVersion (after replay): %v", err)
	} else if want := schema.LatestIgnoredVersion(); healed != want {
		t.Fatalf("CurrentIgnoredVersion after replay = %d, want latest (%d): the replay did not run", healed, want)
	}

	after := snapshotIgnoredPlaneTablesExceptJournal(t, ctx, db)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("replaying ignored 22..28 perturbed a table it should never touch:\nbefore=%+v\nafter=%+v", before, after)
	}
}

// TestStoreAtIgnored28WritesAndJournals is the real-store half of T2.9
// (BEADS-JOURNAL-PLAN.md §4.3, PR A2; schema.TestOldIgnoredCeilingOpensAfter0028
// is the other half, pinning architecturally that no open-path guard compares
// a binary's own ignored ceiling against the stored cursor). A fresh store
// migrates to ignored 28 on its own, with no ignored-ceiling input from the
// caller at all; this just confirms that store opens, writes, and journals
// normally once there, matching the plan's own framing of the claim.
func TestStoreAtIgnored28WritesAndJournals(t *testing.T) {
	te := newTestEnv(t, "oldceil")
	ctx := context.Background()

	// Short-lived connection for the pre-check only: te.store's own calls
	// below (SetEventsJournalEnabled, CreateIssue) each open and close their
	// own connection to the same directory, and OpenSQL's backoff waits
	// until ctx cancellation (never), so holding a second connection open
	// concurrently via te.openDB here would deadlock them forever instead of
	// failing. te.openDB is only safe to call once nothing else still needs
	// to open its own connection to this store (see T2.5/T2.6/T2.7 above,
	// which all finish their te.store.* calls first).
	func() {
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer func() { _ = cleanup() }()
		if got, err := schema.CurrentIgnoredVersion(ctx, db); err != nil {
			t.Fatalf("CurrentIgnoredVersion: %v", err)
		} else if want := schema.LatestIgnoredVersion(); got != want {
			t.Fatalf("CurrentIgnoredVersion = %d, want latest (%d) on a freshly initialized store", got, want)
		}
	}()

	te.store.SetEventsJournalEnabled(true)
	if err := te.store.EventsJournalActivationError(); err != nil {
		t.Fatalf("activation at ignored 28: %v", err)
	}
	issue := &types.Issue{Title: "t2.9 seed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue at ignored 28: %v", err)
	}

	db := te.openDB(t, ctx)
	var journaled int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bd_events_journal WHERE issue_id = ?", issue.ID).Scan(&journaled); err != nil {
		t.Fatalf("probe bd_events_journal: %v", err)
	}
	if journaled == 0 {
		t.Error("CreateIssue at ignored 28 did not journal")
	}
}
