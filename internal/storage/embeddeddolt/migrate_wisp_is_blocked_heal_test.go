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

// These tests pin gastownhall/beads#5257 end to end on the real engine: a
// fully-migrated store whose out-of-band copy lost wisps.is_blocked (and its
// index) behind an at-latest ignored cursor. The trusted cursor made the
// series look fully applied, so the column was never re-added and poisoned
// every blocked-state read and write. The fix is a column sentinel on
// wisps.is_blocked with replayFloor 14: the contradicted cursor is believed
// down to 14 — never to zero, so frozen 0007's unguarded recompute stays out
// of the replay — and the pre-0015 repair re-adds the column and index in
// code before guarded 0015 recomputes the flags.

// wispIsBlockedHealBlocker is the open issue the issue-blocked wisp hangs
// from. Its historical stamp is months old, second-precision, and unique to
// this fixture, like every stamp below.
var wispIsBlockedHealBlocker = stampedIssue{id: "hb-blocker", status: "open", stamp: historicalStamp(41)}

// wispIsBlockedHealWisps is a v-latest wisp fixture mirroring the graph the
// 0007-era tests use, with is_blocked seeded to each row's converged value:
//
//	hb-wisp-blocked          -blocks(issue)-> hb-blocker (open: blocked)
//	hb-wisp-blocked-by-wisp  -blocks(wisp)-> hb-wisp-blocker (open: blocked)
//	hb-wisp-blocker          (the wisp blocker itself: not blocked)
//	hb-wisp-free             (no edges)
var wispIsBlockedHealWisps = []stampedIssue{
	{id: "hb-wisp-blocked", status: "open", ephemeral: true, stamp: historicalStamp(42), wantBlocked: true},
	{id: "hb-wisp-free", status: "open", ephemeral: true, stamp: historicalStamp(43)},
	{id: "hb-wisp-blocker", status: "open", ephemeral: true, stamp: historicalStamp(44)},
	{id: "hb-wisp-blocked-by-wisp", status: "open", ephemeral: true, stamp: historicalStamp(45), wantBlocked: true},
}

// seedWispIsBlockedHealFixture writes the fixture rows and edges on a store
// that is fully migrated in both planes, with historical updated_at on every
// wisp, and commits them.
func seedWispIsBlockedHealFixture(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	insertStampedIssues(t, ctx, conn, "issues", []stampedIssue{wispIsBlockedHealBlocker}, true)
	insertStampedIssues(t, ctx, conn, "wisps", wispIsBlockedHealWisps, true)
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES (UUID(), ?, ?, 'blocks', 'fixture')",
		wispIsBlockedHealWisps[0].id, wispIsBlockedHealBlocker.id); err != nil {
		t.Fatalf("seed issue->wisp dependency: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type, created_by) VALUES (UUID(), ?, ?, 'blocks', 'fixture')",
		wispIsBlockedHealWisps[3].id, wispIsBlockedHealWisps[2].id); err != nil {
		t.Fatalf("seed wisp->wisp dependency: %v", err)
	}
	commitFixture(t, ctx, conn, "test: wisps with historical updated_at and converged is_blocked")
}

// dropWispIsBlockedForHeal reproduces the gh 5257 drift out of band: the
// column and its index are gone while the ignored cursor still says
// at-latest — the shape a store arrives in after a bad copy, restore, or
// partial upgrade. wisps is dolt-ignored, so the DDL lands nowhere in the
// working set; there is nothing to commit and no history of the loss, which
// is exactly why the drift could not be reconstructed from the log.
func dropWispIsBlockedForHeal(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	execFrozenGuard(t, ctx, conn, "DROP INDEX idx_wisps_is_blocked ON wisps")
	execFrozenGuard(t, ctx, conn, "ALTER TABLE wisps DROP COLUMN is_blocked")
}

func countWispIsBlockedColumn(t *testing.T, ctx context.Context, conn *sql.Conn) int {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'is_blocked'`).Scan(&n); err != nil {
		t.Fatalf("read wisps.is_blocked presence: %v", err)
	}
	return n
}

func countWispIsBlockedIndex(t *testing.T, ctx context.Context, conn *sql.Conn) int {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT INDEX_NAME) FROM INFORMATION_SCHEMA.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND INDEX_NAME = 'idx_wisps_is_blocked'`).Scan(&n); err != nil {
		t.Fatalf("read idx_wisps_is_blocked presence: %v", err)
	}
	return n
}

// migrateUpRecordingSteps runs MigrateUp and returns the versions it applied,
// in order, via the per-step fault hook seam.
func migrateUpRecordingSteps(t *testing.T, ctx context.Context, conn *sql.Conn) ([]int, error) {
	t.Helper()
	var applied []int
	restore := schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, v int) error {
		applied = append(applied, v)
		return nil
	})
	defer restore()
	_, err := schema.MigrateUp(ctx, conn)
	return applied, err
}

// wispIsBlockedHealTail is the pending set the floor-14 clamp describes:
// everything after 14 up to the ignored series' latest.
func wispIsBlockedHealTail() []int {
	tail := make([]int, 0, schema.LatestIgnoredVersion()-14)
	for v := 15; v <= schema.LatestIgnoredVersion(); v++ {
		tail = append(tail, v)
	}
	return tail
}

// openFullyMigratedStore seeds a fresh store with the main plane at latest
// and brings the ignored plane up too, leaving both cursors at latest on a
// committed-clean working set.
func openFullyMigratedStore(t *testing.T, ctx context.Context) (*sql.Conn, func()) {
	t.Helper()
	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		closeConn()
		t.Fatalf("bootstrap MigrateUp (ignored plane): %v", err)
	}
	return conn, closeConn
}

// requireHealedWispIsBlocked asserts the post-heal end state: column and
// index back, flags recomputed from the graph, stamps intact, edges intact,
// cursor at latest.
func requireHealedWispIsBlocked(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	if n := countWispIsBlockedColumn(t, ctx, conn); n != 1 {
		t.Errorf("wisps.is_blocked presence = %d, want 1 (the pre-0015 repair re-adds it)", n)
	}
	if n := countWispIsBlockedIndex(t, ctx, conn); n != 1 {
		t.Errorf("idx_wisps_is_blocked presence = %d, want 1 (the pre-0015 repair re-adds it)", n)
	}
	for _, w := range wispIsBlockedHealWisps {
		if got := readIsBlocked(t, ctx, conn, "wisps", w.id); got != w.wantBlocked {
			t.Errorf("wisps %s: is_blocked = %v, want %v recomputed from the graph", w.id, got, w.wantBlocked)
		}
		requireStamp(t, ctx, conn, "wisps", w.id, w.stamp)
	}
	var edges int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id LIKE 'hb-%'").Scan(&edges); err != nil {
		t.Fatalf("count fixture wisp_dependencies: %v", err)
	}
	if edges != 2 {
		t.Errorf("fixture wisp_dependencies rows = %d, want 2 (the heal must not touch edges)", edges)
	}
	var cursor int
	if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations").Scan(&cursor); err != nil {
		t.Fatalf("read ignored cursor: %v", err)
	}
	if cursor != schema.LatestIgnoredVersion() {
		t.Errorf("ignored cursor = %d, want %d (the heal replays pending versions; it never rewinds recorded ones)", cursor, schema.LatestIgnoredVersion())
	}
}

// TestEmbeddedIgnoredSentinelHealsALostWispIsBlockedColumn is the gh 5257
// repro on the real engine: after the out-of-band loss, one write-capable
// open re-adds the column and index and recomputes the flags, applying
// exactly the 0015..latest tail — never 0006/0007, whose unguarded recompute
// would restamp every wisp's updated_at.
func TestEmbeddedIgnoredSentinelHealsALostWispIsBlockedColumn(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn, closeConn := openFullyMigratedStore(t, ctx)
	defer closeConn()
	seedWispIsBlockedHealFixture(t, ctx, conn)

	dropWispIsBlockedForHeal(t, ctx, conn)
	if n := countWispIsBlockedColumn(t, ctx, conn); n != 0 {
		t.Fatalf("fixture did not lose wisps.is_blocked (presence = %d)", n)
	}
	if n := countWispIsBlockedIndex(t, ctx, conn); n != 0 {
		t.Fatalf("fixture did not lose idx_wisps_is_blocked (presence = %d)", n)
	}

	applied, err := migrateUpRecordingSteps(t, ctx, conn)
	if err != nil {
		t.Fatalf("heal MigrateUp: %v", err)
	}
	if !slices.Equal(applied, wispIsBlockedHealTail()) {
		t.Errorf("heal applied %v, want exactly the 0015..%d tail (floor 14; zero would restamp via 0007)",
			applied, schema.LatestIgnoredVersion())
	}
	requireHealedWispIsBlocked(t, ctx, conn)
}

// TestEmbeddedHealedWispIsBlockedServesBlockedStateAPIs pins the other half
// of the gh 5257 harm: the missing column did not just break migrations, it
// poisoned the blocked-state surface (bd doctor's check, the targeted and
// full recomputes). After the heal all three work against the re-added
// column, and a realistic post-heal drift -- a blocker closed by raw SQL,
// bypassing the recompute -- is detected, repaired by the targeted recompute,
// and left converged for the full one.
func TestEmbeddedHealedWispIsBlockedServesBlockedStateAPIs(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn, closeConn := openFullyMigratedStore(t, ctx)
	defer closeConn()
	seedWispIsBlockedHealFixture(t, ctx, conn)
	dropWispIsBlockedForHeal(t, ctx, conn)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("heal MigrateUp: %v", err)
	}

	// The doctor check reads the healed, converged store at zero.
	if n, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, conn); err != nil {
		t.Fatalf("count inconsistencies on the healed store: %v", err)
	} else if n != 0 {
		t.Fatalf("inconsistencies on the healed store = %d, want 0", n)
	}

	// Post-heal drift the recompute APIs exist for: closing the blocker
	// issue out of band leaves the issue-blocked wisp flagged though nothing
	// blocks it anymore. (The wisp-blocked wisp stays blocked: its wisp
	// blocker is still open.)
	execFrozenGuard(t, ctx, conn,
		"UPDATE issues SET status = 'closed', closed_at = '2025-05-02 09:00:00' WHERE id = '"+wispIsBlockedHealBlocker.id+"'")
	commitFixture(t, ctx, conn, "test: blocker issue closed out of band")
	if n, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, conn); err != nil {
		t.Fatalf("count inconsistencies after out-of-band close: %v", err)
	} else if n != 1 {
		t.Fatalf("inconsistencies after out-of-band close = %d, want 1 (the issue-blocked wisp)", n)
	}

	// The targeted recompute repairs exactly the wisps it is handed.
	if err := issueops.RecomputeIsBlockedInTx(ctx, conn, nil,
		[]string{wispIsBlockedHealWisps[0].id, wispIsBlockedHealWisps[3].id}, "fixture"); err != nil {
		t.Fatalf("targeted recompute: %v", err)
	}
	if got := readIsBlocked(t, ctx, conn, "wisps", wispIsBlockedHealWisps[0].id); got {
		t.Errorf("wisps %s: is_blocked = true after its blocker closed, want false", wispIsBlockedHealWisps[0].id)
	}
	if got := readIsBlocked(t, ctx, conn, "wisps", wispIsBlockedHealWisps[3].id); !got {
		t.Errorf("wisps %s: is_blocked = false though its wisp blocker is open, want true", wispIsBlockedHealWisps[3].id)
	}
	if n, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, conn); err != nil {
		t.Fatalf("count inconsistencies after targeted recompute: %v", err)
	} else if n != 0 {
		t.Fatalf("inconsistencies after targeted recompute = %d, want 0", n)
	}
	// No commit here: the recompute wrote only the dolt-ignored wisps plane.

	// The full repair (bd doctor --fix) succeeds on the healed store and
	// changes nothing further.
	if n, err := issueops.RecomputeAllIsBlockedInTx(ctx, conn); err != nil {
		t.Fatalf("full recompute on the healed store: %v", err)
	} else if n != 0 {
		t.Errorf("full recompute changed %d rows on a converged store, want 0", n)
	}
}
