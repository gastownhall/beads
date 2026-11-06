//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedMigrate0075FrozenIgnoredStepsPrecedeRepair pins AC-6 of
// gastownhall/beads#7037: when the ignored plane is deliberately behind — held
// at the sentinel floor (18), so the whole frozen block 0019..0031 is still
// pending — and the tracked plane holds both a 0059-era over-set and a pending
// dependency re-key with a #5268 duplicate edge, ONE consented MigrateUp pass
// must run every prerequisite before ignored 0032 recomputes flags. The frozen
// steps replay and record in order, the re-key collapses its duplicate, and
// only then the repair computes from that converged state — leaving both
// planes matching a full recompute exactly, the obligation consumed at HEAD,
// and every ignored cursor row contiguous through 32.
//
// On a locally-built fixture every frozen step's table effect also has a main
// twin (0019/0022/0030 exist via 0062/0064/0074, the wisp columns via
// 0060/0067/0070/0073): the frozen block's own observable is its cursor rows
// plus the survival of wisp rows seeded in the at-18 shape across the replay's
// rebuilds — the re-key is the prerequisite whose ordering the flags prove,
// because pr-dup's correct flag is true only against the pre-dedup graph.

// seedFrozenPrereqStore builds the AC-6 fixture with the connection closed:
// main at 74, the ignored plane frozen at 18, stale flags on both planes, and
// a #5268 duplicate edge pending re-key — ready for one production MigrateUp
// on a fresh engine handle.
func seedFrozenPrereqStore(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 74)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	// The pattern a production store holds from its first open (the canonical
	// seeder asserts it): planted so commitFixture's DOLT_ADD('-A') cannot
	// swallow the ignored cursor rows into tracked history (#4356).
	mustExecOn(t, ctx, conn, "INSERT INTO dolt_ignore VALUES ('ignored_schema_migrations', true)")

	// The ignored plane is held at 18 — the events sentinel floor — so the
	// pass ahead owes the whole frozen block 0019..0031 before 0032.
	if _, err := schema.MigrateIgnoredUpTo(ctx, conn, 18); err != nil {
		t.Fatalf("MigrateIgnoredUpTo(18): %v", err)
	}

	// The tracked plane: a genuine blocker, a relates-to over-set, and the
	// duplicate edge whose re-key collapse flips pr-dup's correct flag.
	rows := []stampedIssue{
		{id: "pr-blocker", status: "open", stamp: historicalStamp(1)},
		{id: "pr-blocked", status: "open", stamp: historicalStamp(2), wantBlocked: true},
		{id: "pr-related", status: "open", stamp: historicalStamp(3)},
		{id: "pr-twin", status: "open", stamp: historicalStamp(4)},
		{id: "pr-dup", status: "open", stamp: historicalStamp(5), wantBlocked: true},
	}
	insertStampedIssues(t, ctx, conn, "issues", rows, true)
	insertIssueEdges(t, ctx, conn, [][3]string{
		{"pr-blocked", "pr-blocker", "blocks"},
		{"pr-related", "pr-blocked", "relates-to"},
	})
	// The duplicated edge, pre-#4259 random ids so the pending re-key owns
	// them: issue-typed relates-to survivor against wisp-typed blocks loser,
	// both resolving to the id pr-twin holds on both planes.
	mustExecOn(t, ctx, conn, `
INSERT INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, type, created_by) VALUES
  (UUID(), 'pr-dup', 'pr-twin', NULL, 'relates-to', 'fixture'),
  (UUID(), 'pr-dup', NULL, 'pr-twin', 'blocks', 'fixture')
`)
	mustExecOn(t, ctx, conn, "UPDATE issues SET is_blocked = 1, updated_at = updated_at WHERE id = 'pr-related'")

	// The wisp plane, clone-local working set, seeded in the at-18 shape: an
	// over-set edgeless wisp and a genuinely blocked wisp under-set. These
	// rows must survive the frozen block's rebuilds and be repaired by 0032.
	mustExecOn(t, ctx, conn, `
INSERT INTO wisps (id, title, status, is_blocked, updated_at) VALUES
  ('pr-twin', 'open wisp twin of the issue id', 'open', 0, ?),
  ('wf-free', 'no edges', 'open', 1, ?),
  ('wf-waited', 'waits on pr-blocker', 'open', 0, ?)
`, historicalStamp(6), historicalStamp(7), historicalStamp(8))
	mustExecOn(t, ctx, conn, `
INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_by)
VALUES ('wdep-1', 'wf-waited', 'pr-blocker', 'blocks', 'fixture')
`)
	commitFixture(t, ctx, conn, "test: v74/ignored-18 store with stale flags, a duplicate edge, and a frozen ignored plane")
	return dataDir
}

func TestEmbeddedMigrate0075FrozenIgnoredStepsPrecedeRepair(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedFrozenPrereqStore(t)

	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp: %v", err)
		}

		// The frozen block recorded contiguously 1..32: every ignored step
		// ran, in order, with 0032 recording last — its repair cannot have
		// run before the prerequisites it computes from.
		versions := ignoredCursorVersions(t, ctx, conn)
		if len(versions) != 32 {
			t.Fatalf("ignored cursor holds %d row(s) after the pass, want 32 (%v)", len(versions), versions)
		}
		for i, v := range versions {
			if v != i+1 {
				t.Fatalf("ignored cursor rows are not contiguous 1..32: position %d holds %d (%v)", i, v, versions)
			}
		}

		// The re-key prerequisite collapsed the duplicate before the repair
		// computed: exactly the issue-typed relates-to survivor remains, and
		// pr-dup's flag flipped with the collapse — it is true only against
		// the pre-dedup graph.
		var dupEdges, dupIssueTyped int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*), COALESCE(SUM(depends_on_issue_id IS NOT NULL), 0) FROM dependencies WHERE issue_id = 'pr-dup'").Scan(&dupEdges, &dupIssueTyped); err != nil {
			t.Fatalf("count pr-dup edges: %v", err)
		}
		if dupEdges != 1 || dupIssueTyped != 1 {
			t.Errorf("dependencies holds %d pr-dup edge(s) (%d issue-typed) after the pass, want exactly the one issue-typed survivor", dupEdges, dupIssueTyped)
		}

		// Both planes match a full recompute: named flags on each plane,
		// including the two stale wisp seeds repaired across the frozen
		// block's rebuilds...
		wantFlags := map[string]map[string]bool{
			"issues": {"pr-blocker": false, "pr-blocked": true, "pr-related": false, "pr-twin": false, "pr-dup": false},
			"wisps":  {"pr-twin": false, "wf-free": false, "wf-waited": true},
		}
		for table, flags := range wantFlags {
			for id, want := range flags {
				if got := readIsBlocked(t, ctx, conn, table, id); got != want {
					t.Errorf("%s %s: is_blocked = %v after the pass, want %v", table, id, got, want)
				}
			}
		}
		// ...and the read-only oracle agrees on zero remaining, spanning both
		// planes (issues through dependencies, wisps through
		// wisp_dependencies).
		if corrected, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, conn); err != nil {
			t.Fatalf("counting is_blocked inconsistencies: %v", err)
		} else if corrected != 0 {
			t.Errorf("full recompute still corrects %d row(s) after the pass", corrected)
		}

		requireKeyConsumedAtHead(t, ctx, conn)
		requireStamp(t, ctx, conn, "issues", "pr-related", historicalStamp(3))
		requireStamp(t, ctx, conn, "issues", "pr-dup", historicalStamp(5))
	})
}

// ignoredCursorVersions reads the ignored plane's cursor rows in version
// order.
func ignoredCursorVersions(t *testing.T, ctx context.Context, conn *sql.Conn) []int {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT version FROM ignored_schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("read ignored cursor: %v", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan ignored cursor row: %v", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate ignored cursor: %v", err)
	}
	return out
}
