//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests pin gastownhall/beads#5257 on a real engine: a clone whose
// ignored cursor claims ignored/0006 ran, whose wisps and wisp_dependencies
// tables exist, but whose wisps.is_blocked column (and idx_wisps_is_blocked)
// is gone. Before the wisps.is_blocked sentinel the cursor was believed,
// MigrateUp had nothing to do, and every blocked-state recompute failed with
// `table "p" does not have column "is_blocked"`.

// lostIsBlockedIssueBlocker is the one durable issue in the fixture: an open
// issue a wisp waits on.
var lostIsBlockedIssueBlocker = stampedIssue{id: "hb-issue-blocker", status: "open", stamp: historicalStamp(41)}

// lostIsBlockedWisps is the wisp side: one blocked by an issue, one blocked by
// another wisp, that wisp blocker itself, and a free bystander, each with a
// distinct months-old updated_at.
var lostIsBlockedWisps = []stampedIssue{
	{id: "hb-wisp-by-issue", status: "open", ephemeral: true, stamp: historicalStamp(42), wantBlocked: true},
	{id: "hb-wisp-blocker", status: "open", ephemeral: true, stamp: historicalStamp(43)},
	{id: "hb-wisp-by-wisp", status: "open", ephemeral: true, stamp: historicalStamp(44), wantBlocked: true},
	{id: "hb-wisp-free", status: "open", ephemeral: true, stamp: historicalStamp(45)},
}

// seedLostIsBlockedStore builds a current embedded store (main and ignored
// series at latest), seeds the mixed issue/wisp blocking graph with
// historical timestamps, and commits the tracked half so the migration's
// dirty-table gate sees a clean working set. Wisps, their dependencies and
// labels are clone-local (dolt_ignored) and need no commit.
func seedLostIsBlockedStore(t *testing.T, ctx context.Context) *sql.Conn {
	t.Helper()
	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	t.Cleanup(closeConn)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp to latest: %v", err)
	}
	if got := ignoredCursor(t, ctx, conn); got != schema.LatestIgnoredVersion() {
		t.Fatalf("ignored cursor after the seeding MigrateUp = %d, want latest %d", got, schema.LatestIgnoredVersion())
	}

	insertStampedIssues(t, ctx, conn, "issues", []stampedIssue{lostIsBlockedIssueBlocker}, false)
	insertStampedIssues(t, ctx, conn, "wisps", lostIsBlockedWisps, true)
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES (UUID(), ?, ?, 'blocks', 'fixture')",
		"hb-wisp-by-issue", lostIsBlockedIssueBlocker.id); err != nil {
		t.Fatalf("seed wisp-to-issue dependency: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type, created_by) VALUES (UUID(), ?, ?, 'blocks', 'fixture')",
		"hb-wisp-by-wisp", "hb-wisp-blocker"); err != nil {
		t.Fatalf("seed wisp-to-wisp dependency: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_labels (issue_id, label) VALUES (?, ?)", "hb-wisp-free", "kept"); err != nil {
		t.Fatalf("seed wisp label: %v", err)
	}
	commitFixture(t, ctx, conn, "test: mixed issue/wisp blocking graph")
	return conn
}

// dropWispIsBlocked produces gh 5257's shape: the index, then the column it
// covers, go away while every cursor row stays.
func dropWispIsBlocked(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	execFrozenGuard(t, ctx, conn, "DROP INDEX idx_wisps_is_blocked ON wisps")
	execFrozenGuard(t, ctx, conn, "ALTER TABLE wisps DROP COLUMN is_blocked")
	if wispIsBlockedColumnPresent(t, ctx, conn) || wispIsBlockedIndexPresent(t, ctx, conn) {
		t.Fatal("fixture: wisps.is_blocked or idx_wisps_is_blocked survived the drop")
	}
	if got := ignoredCursor(t, ctx, conn); got != schema.LatestIgnoredVersion() {
		t.Fatalf("fixture: ignored cursor after the drop = %d, want it still claiming latest %d", got, schema.LatestIgnoredVersion())
	}
}

// migrateUpRecordingSteps runs the production MigrateUp and returns every
// migration version it applied, in order, through the per-step test hook
// (which observes and never faults here). The main series is at latest in
// these fixtures, so the versions are the ignored series' replay.
func migrateUpRecordingSteps(t *testing.T, ctx context.Context, conn *sql.Conn) []int {
	t.Helper()
	var applied []int
	restore := schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, v int) error {
		applied = append(applied, v)
		return nil
	})
	defer restore()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	return applied
}

// requireWispIsBlockedHealed asserts the healed shape: the column and its
// index exist, every wisp's flag follows the graph, every seeded row kept its
// historical updated_at, and the ignored cursor reads latest.
func requireWispIsBlockedHealed(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	if !wispIsBlockedColumnPresent(t, ctx, conn) {
		t.Fatal("wisps.is_blocked is still missing after MigrateUp (gh 5257)")
	}
	if !wispIsBlockedIndexPresent(t, ctx, conn) {
		t.Error("idx_wisps_is_blocked is still missing after MigrateUp")
	}
	for _, w := range lostIsBlockedWisps {
		requireStamp(t, ctx, conn, "wisps", w.id, w.stamp)
		if got := readIsBlocked(t, ctx, conn, "wisps", w.id); got != w.wantBlocked {
			t.Errorf("wisps %s: is_blocked = %v, want %v", w.id, got, w.wantBlocked)
		}
	}
	requireStamp(t, ctx, conn, "issues", lostIsBlockedIssueBlocker.id, lostIsBlockedIssueBlocker.stamp)
	if got := ignoredCursor(t, ctx, conn); got != schema.LatestIgnoredVersion() {
		t.Errorf("ignored cursor = %d, want latest %d", got, schema.LatestIgnoredVersion())
	}
}

// wantHealReplay is the ignored replay the wisps.is_blocked sentinel's floor
// (14) schedules: ignored/0015 (whose pre-migration repair restores the column
// and index) through the latest, and never frozen 0006 or 0007.
func wantHealReplay() []int {
	var want []int
	for v := 15; v <= schema.LatestIgnoredVersion(); v++ {
		want = append(want, v)
	}
	return want
}

func ignoredCursor(t *testing.T, ctx context.Context, conn *sql.Conn) int {
	t.Helper()
	return scalarInt(t, ctx, conn, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations")
}

func wispIsBlockedColumnPresent(t *testing.T, ctx context.Context, conn *sql.Conn) bool {
	t.Helper()
	return scalarInt(t, ctx, conn, `SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'is_blocked'`) > 0
}

func wispIsBlockedIndexPresent(t *testing.T, ctx context.Context, conn *sql.Conn) bool {
	t.Helper()
	return scalarInt(t, ctx, conn, `SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND INDEX_NAME = 'idx_wisps_is_blocked'`) > 0
}

// TestEmbeddedIgnoredSentinelHealsALostWispIsBlockedColumn is gh 5257's AC-2:
// an otherwise-current store that lost wisps.is_blocked and its index behind
// an at-latest ignored cursor gets both back from the production MigrateUp,
// with blocked flags matching the mixed issue/wisp graph and every historical
// updated_at intact. The replay starts at ignored/0015, so frozen 0006 and
// 0007 never run, and the rows the replay range touches (wisps, their
// dependencies and labels, the issue, the events plane) all survive.
func TestEmbeddedIgnoredSentinelHealsALostWispIsBlockedColumn(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn := seedLostIsBlockedStore(t, ctx)

	countRows := func() map[string]int {
		counts := map[string]int{}
		for _, table := range []string{"wisps", "wisp_dependencies", "wisp_labels", "issues", "events"} {
			counts[table] = scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM "+table)
		}
		return counts
	}
	before := countRows()
	dropWispIsBlocked(t, ctx, conn)

	applied := migrateUpRecordingSteps(t, ctx, conn)
	if !slices.Equal(applied, wantHealReplay()) {
		t.Errorf("MigrateUp applied ignored versions %v, want %v (resume at the pre-0015 repair, never 0006/0007)", applied, wantHealReplay())
	}
	requireWispIsBlockedHealed(t, ctx, conn)

	after := countRows()
	for table, n := range before {
		if after[table] != n {
			t.Errorf("%s rows after the heal = %d, want the %d seeded before it", table, after[table], n)
		}
	}
	if got := scalarInt(t, ctx, conn,
		"SELECT COUNT(*) FROM wisp_labels WHERE issue_id = 'hb-wisp-free' AND label = 'kept'"); got != 1 {
		t.Errorf("seeded wisp label rows after the heal = %d, want 1", got)
	}
}

// TestEmbeddedRepairedWispIsBlockedServesBlockedStateRecompute is gh 5257's
// AC-3: once MigrateUp has healed the lost column, the runtime blocked-state
// paths the report saw fail with `does not have column "is_blocked"` work
// again: the doctor consistency count, a status change that unblocks a wisp's
// dependent, the per-row recompute, and the full repair, each leaving the
// derived flags consistent with the graph.
func TestEmbeddedRepairedWispIsBlockedServesBlockedStateRecompute(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn := seedLostIsBlockedStore(t, ctx)
	dropWispIsBlocked(t, ctx, conn)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	// The repair itself must not stamp the rows; read before any user write.
	for _, w := range lostIsBlockedWisps {
		requireStamp(t, ctx, conn, "wisps", w.id, w.stamp)
	}

	requireConsistent := func(when string) {
		t.Helper()
		n, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, conn)
		if err != nil {
			t.Fatalf("doctor blocked-state count %s: %v", when, err)
		}
		if n != 0 {
			t.Errorf("doctor blocked-state count %s = %d stale rows, want 0", when, n)
		}
	}
	requireConsistent("right after the repair")

	// Status transition: closing the wisp blocker unblocks its dependent.
	if _, err := issueops.CloseIssueInTx(ctx, conn, "hb-wisp-blocker", "done", "fixture", ""); err != nil {
		t.Fatalf("close the wisp blocker: %v", err)
	}
	if readIsBlocked(t, ctx, conn, "wisps", "hb-wisp-by-wisp") {
		t.Error("hb-wisp-by-wisp is still blocked after its only blocker closed")
	}
	if !readIsBlocked(t, ctx, conn, "wisps", "hb-wisp-by-issue") {
		t.Error("hb-wisp-by-issue lost its block on an open issue")
	}
	requireConsistent("after the status change")

	ids := make([]string, 0, len(lostIsBlockedWisps))
	for _, w := range lostIsBlockedWisps {
		ids = append(ids, w.id)
	}
	if err := issueops.RecomputeIsBlockedInTx(ctx, conn, []string{lostIsBlockedIssueBlocker.id}, ids, "fixture"); err != nil {
		t.Fatalf("per-row blocked-state recompute: %v", err)
	}
	requireConsistent("after the per-row recompute")

	// Full repair: a stale flag the count sees is one the recompute fixes.
	execFrozenGuard(t, ctx, conn, "UPDATE wisps SET is_blocked = 1 WHERE id = 'hb-wisp-free'")
	if n, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, conn); err != nil || n == 0 {
		t.Fatalf("doctor blocked-state count over a stale flag = %d, %v; want > 0, nil", n, err)
	}
	if _, err := issueops.RecomputeAllIsBlockedInTx(ctx, conn); err != nil {
		t.Fatalf("full blocked-state recompute: %v", err)
	}
	if readIsBlocked(t, ctx, conn, "wisps", "hb-wisp-free") {
		t.Error("full recompute left the free wisp blocked")
	}
	requireConsistent("after the full recompute")
}
