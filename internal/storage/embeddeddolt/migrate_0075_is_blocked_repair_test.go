//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// These tests pin the automatic is_blocked repair for gastownhall/beads#7037.
// Stores that crossed frozen migration 0059 (or its ignored twin) on an engine
// with dolthub/dolt#11886 hold is_blocked flags that do not match their graph.
// Main 0075 records that a full recompute is owed; ignored 0032 performs it on
// both planes and commits the tracked half.

const isBlockedRepairOwedKey = "is_blocked_repair_owed_0075"

// repairGraph is the issue-plane fixture, seeded with the flags a buggy 0059
// left behind (stale) rather than the flags the graph implies (want):
//
//	rp-blocked   -blocks->       rp-blocker   (genuine block)
//	rp-child     -parent-child-> rp-blocked   (inherits the block)
//	rp-related   -relates-to->   rp-blocked   (stale: never blocked)
//	rp-grandkid  -parent-child-> rp-related   (stale: parent is not blocked)
//	rp-underset  -blocks->       rp-blocker   (stale under-set: is blocked)
//	rp-closed    -relates-to->   rp-blocked   (closed and stale)
//	rp-free                                   (no edges)
var repairGraph = []repairRow{
	{stampedIssue{id: "rp-blocker", status: "open", stamp: historicalStamp(1)}, false},
	{stampedIssue{id: "rp-blocked", status: "open", stamp: historicalStamp(2), wantBlocked: true}, true},
	{stampedIssue{id: "rp-child", status: "open", stamp: historicalStamp(3), wantBlocked: true}, true},
	{stampedIssue{id: "rp-related", status: "open", stamp: historicalStamp(4)}, true},
	{stampedIssue{id: "rp-grandkid", status: "open", stamp: historicalStamp(5)}, true},
	{stampedIssue{id: "rp-underset", status: "open", stamp: historicalStamp(6), wantBlocked: true}, false},
	{stampedIssue{id: "rp-closed", status: "closed", stamp: historicalStamp(7)}, true},
	{stampedIssue{id: "rp-free", status: "open", stamp: historicalStamp(8)}, false},
}

var repairEdges = [][3]string{
	{"rp-blocked", "rp-blocker", "blocks"},
	{"rp-child", "rp-blocked", "parent-child"},
	{"rp-related", "rp-blocked", "relates-to"},
	{"rp-grandkid", "rp-related", "parent-child"},
	{"rp-underset", "rp-blocker", "blocks"},
	{"rp-closed", "rp-blocked", "relates-to"},
}

// wispRepairGraph is the same shape on the wisp plane.
var wispRepairGraph = []repairRow{
	{stampedIssue{id: "rw-blocker", status: "open", stamp: historicalStamp(11)}, false},
	{stampedIssue{id: "rw-blocked", status: "open", stamp: historicalStamp(12), wantBlocked: true}, true},
	{stampedIssue{id: "rw-related", status: "open", stamp: historicalStamp(13)}, true},
	{stampedIssue{id: "rw-underset", status: "open", stamp: historicalStamp(14), wantBlocked: true}, false},
}

var wispRepairEdges = [][3]string{
	{"rw-blocked", "rw-blocker", "blocks"},
	{"rw-related", "rw-blocked", "relates-to"},
	{"rw-underset", "rw-blocker", "blocks"},
}

// repairRow is a fixture row with the is_blocked value to seed.
type repairRow struct {
	stampedIssue
	stale bool
}

// seedRewoundStore builds a store at the latest schema and rewinds its cursors
// to mainAt/ignoredAt, the shape of a store this binary's predecessor
// migrated. Rows above the cursors are deleted, so MAX(version) reads them.
func seedRewoundStore(t *testing.T, ctx context.Context, mainAt, ignoredAt int) (string, *sql.Conn, func()) {
	t.Helper()
	dataDir := seedMainSchemaAt(t, ctx, mainAt)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		closeConn()
		t.Fatalf("MigrateUp seed: %v", err)
	}
	mustExecOn(t, ctx, conn, "DELETE FROM schema_migrations WHERE version > ?", mainAt)
	mustExecOn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version > ?", ignoredAt)
	return dataDir, conn, closeConn
}

// insertRepairRows seeds rows into table with their stale flags.
func insertRepairRows(t *testing.T, ctx context.Context, conn *sql.Conn, table string, rows []repairRow) {
	t.Helper()
	stamped := make([]stampedIssue, len(rows))
	for i, r := range rows {
		stamped[i] = r.stampedIssue
		stamped[i].wantBlocked = r.stale
	}
	insertStampedIssues(t, ctx, conn, table, stamped, true)
}

func insertWispEdges(t *testing.T, ctx context.Context, conn *sql.Conn, edges [][3]string) {
	t.Helper()
	for _, e := range edges {
		mustExecOn(t, ctx, conn,
			"INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by, metadata) VALUES (UUID(), ?, ?, ?, NOW(), 'fixture', JSON_OBJECT())",
			e[0], e[1], e[2])
	}
}

// seedJournal writes two journal rows and advances the seq counter, so a
// repair that journaled its flips would show up as new rows or a moved seq.
func seedJournal(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	for seq, id := range []string{"rp-blocked", "rp-related"} {
		mustExecOn(t, ctx, conn,
			"INSERT INTO bd_events_journal (seq, ts, op, issue_id, issue_json, actor) VALUES (?, '2025-03-01 09:30:00', 'update', ?, '{}', 'fixture')",
			seq+1, id)
	}
	mustExecOn(t, ctx, conn, "UPDATE bd_events_seq SET next_seq = 2 WHERE id = 0")
}

// journalDump renders every journal row and the seq counter.
func journalDump(t *testing.T, ctx context.Context, conn *sql.Conn) string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT seq, op, issue_id, COALESCE(issue_json, '') FROM bd_events_journal ORDER BY seq")
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var seq int64
		var op, id, body string
		if err := rows.Scan(&seq, &op, &id, &body); err != nil {
			t.Fatalf("scan journal: %v", err)
		}
		fmt.Fprintf(&b, "%d %s %s %s\n", seq, op, id, body)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("journal rows: %v", err)
	}
	fmt.Fprintf(&b, "next_seq=%d", scalarInt(t, ctx, conn, "SELECT next_seq FROM bd_events_seq WHERE id = 0"))
	return b.String()
}

func repairReadyIDs(t *testing.T, ctx context.Context, conn *sql.Conn) []string {
	t.Helper()
	ready, err := issueops.GetReadyWorkInTx(ctx, conn, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWorkInTx: %v", err)
	}
	ids := make([]string, 0, len(ready))
	for _, issue := range ready {
		ids = append(ids, issue.ID)
	}
	sort.Strings(ids)
	return ids
}

func repairKeyCount(t *testing.T, ctx context.Context, conn *sql.Conn, asOf string) int {
	t.Helper()
	return scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM metadata"+asOf+" WHERE `key` = ?", isBlockedRepairOwedKey)
}

func requireFlags(t *testing.T, ctx context.Context, conn *sql.Conn, table string, rows []repairRow) {
	t.Helper()
	for _, r := range rows {
		if got := readIsBlocked(t, ctx, conn, table, r.id); got != r.wantBlocked {
			t.Errorf("%s %s: is_blocked = %v, want %v", table, r.id, got, r.wantBlocked)
		}
	}
}

func requireStamps(t *testing.T, ctx context.Context, conn *sql.Conn, table string, rows []repairRow) {
	t.Helper()
	for _, r := range rows {
		requireStamp(t, ctx, conn, table, r.id, r.stamp)
	}
}

// requireFullRecomputeCorrectsNothing is the oracle: a full recompute over a
// repaired store must find nothing left to flip.
func requireFullRecomputeCorrectsNothing(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	n, err := issueops.RecomputeAllIsBlockedInTx(ctx, conn)
	if err != nil {
		t.Fatalf("RecomputeAllIsBlockedInTx: %v", err)
	}
	if n != 0 {
		t.Errorf("full recompute after migration corrected %d row(s), want 0", n)
	}
}

// seedCorruptStore is the AC-1 store: main 74 / ignored 31 with stale flags on
// both planes, committed, plus a seeded journal.
func seedCorruptStore(t *testing.T, ctx context.Context) (*sql.Conn, func()) {
	t.Helper()
	_, conn, closeConn := seedRewoundStore(t, ctx, 74, 31)
	insertRepairRows(t, ctx, conn, "issues", repairGraph)
	insertIssueEdges(t, ctx, conn, repairEdges)
	insertRepairRows(t, ctx, conn, "wisps", wispRepairGraph)
	insertWispEdges(t, ctx, conn, wispRepairEdges)
	seedJournal(t, ctx, conn)
	commitFixture(t, ctx, conn, "test: v74 store with flags left by dolt#11886")
	return conn, closeConn
}

// TestEmbeddedMigration0075HealsHistoricalBlockedFlags is AC-1: a consented
// upgrade of a 74/31 store with corrupt flags on both planes returns the false
// blockers to ready, keeps genuine blocks, leaves nothing for a full recompute
// to correct, and writes no journal rows for the flips.
func TestEmbeddedMigration0075HealsHistoricalBlockedFlags(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn, closeConn := seedCorruptStore(t, ctx)
	defer closeConn()
	journalBefore := journalDump(t, ctx, conn)

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	requireFlags(t, ctx, conn, "issues", repairGraph)
	requireFlags(t, ctx, conn, "wisps", wispRepairGraph)
	requireStamps(t, ctx, conn, "issues", repairGraph)
	requireStamps(t, ctx, conn, "wisps", wispRepairGraph)
	ready := repairReadyIDs(t, ctx, conn)
	for _, id := range []string{"rp-related", "rp-grandkid", "rp-free", "rp-blocker"} {
		if !contains(ready, id) {
			t.Errorf("%s missing from ready work after the repair: %v", id, ready)
		}
	}
	for _, id := range []string{"rp-blocked", "rp-child", "rp-underset"} {
		if contains(ready, id) {
			t.Errorf("%s is genuinely blocked but in ready work: %v", id, ready)
		}
	}
	if got := journalDump(t, ctx, conn); got != journalBefore {
		t.Errorf("journal changed during the repair:\n got %s\nwant %s", got, journalBefore)
	}
	if n := repairKeyCount(t, ctx, conn, ""); n != 0 {
		t.Errorf("repair key still present after the repair (%d row(s))", n)
	}
	if n := repairKeyCount(t, ctx, conn, " AS OF 'HEAD'"); n != 0 {
		t.Errorf("repair key still committed at HEAD after the repair (%d row(s))", n)
	}
	for _, table := range dirtyTableNames(t, ctx, conn) {
		if table == "issues" || table == "metadata" || table == "dependencies" {
			t.Errorf("%s left dirty after the repair; want it committed", table)
		}
	}
	requireFullRecomputeCorrectsNothing(t, ctx, conn)
}
