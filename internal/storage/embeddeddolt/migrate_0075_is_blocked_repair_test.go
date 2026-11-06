//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
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

// insertCanonicalEdges seeds edges keyed by depid.New, the id the dependency
// rekey converges every edge to, so the migration pass has no rekey of its own
// to commit and anything it commits is the repair's.
func insertCanonicalEdges(t *testing.T, ctx context.Context, conn *sql.Conn, table, targetColumn string, edges [][3]string) {
	t.Helper()
	for _, e := range edges {
		//nolint:gosec // G202: table and column are test constants.
		mustExecOn(t, ctx, conn,
			"INSERT INTO "+table+" (id, issue_id, "+targetColumn+", type, created_at, created_by, metadata) VALUES (?, ?, ?, ?, NOW(), 'fixture', JSON_OBJECT())",
			depid.New(e[0], e[1]), e[0], e[1], e[2])
	}
}

// staleFlags returns rows with wantBlocked set to the seeded (stale) flag, for
// asserting a plane the repair must not touch.
func staleFlags(rows []repairRow) []repairRow {
	out := make([]repairRow, len(rows))
	for i, r := range rows {
		out[i] = r
		out[i].wantBlocked = r.stale
	}
	return out
}

// TestEmbeddedMigration0075WispOnlyRepairWithoutKey is AC-2: a clone that
// adopted main 75 by pull (no owed key at HEAD) still runs ignored 0032, which
// repairs its clone-local wisps and writes nothing tracked: issue flags,
// dependencies, metadata and HEAD stay as they were, and no journal row is
// written for the wisp flips.
func TestEmbeddedMigration0075WispOnlyRepairWithoutKey(t *testing.T) {
	requireEmbedded(t)

	t.Run("key-free store at main 75", func(t *testing.T) {
		ctx := t.Context()
		_, conn, closeConn := seedRewoundStore(t, ctx, 75, 31)
		defer closeConn()
		insertRepairRows(t, ctx, conn, "issues", repairGraph)
		insertCanonicalEdges(t, ctx, conn, "dependencies", "depends_on_issue_id", repairEdges)
		insertRepairRows(t, ctx, conn, "wisps", wispRepairGraph)
		insertCanonicalEdges(t, ctx, conn, "wisp_dependencies", "depends_on_wisp_id", wispRepairEdges)
		seedJournal(t, ctx, conn)
		commitFixture(t, ctx, conn, "test: pull-adopted v75 clone with stale local wisps")
		if n := repairKeyCount(t, ctx, conn, " AS OF 'HEAD'"); n != 0 {
			t.Fatalf("fixture: repair key committed at HEAD (%d row(s)); want a key-free clone", n)
		}
		headBefore := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')")
		journalBefore := journalDump(t, ctx, conn)

		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp: %v", err)
		}

		requireFlags(t, ctx, conn, "wisps", wispRepairGraph)
		requireStamps(t, ctx, conn, "wisps", wispRepairGraph)
		requireFlags(t, ctx, conn, "issues", staleFlags(repairGraph))
		requireStamps(t, ctx, conn, "issues", repairGraph)
		if got := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')"); got != headBefore {
			t.Errorf("HEAD moved from %s to %s; the key-free repair must not commit", headBefore, got)
		}
		for _, table := range dirtyTableNames(t, ctx, conn) {
			if table == "issues" || table == "metadata" || table == "dependencies" {
				t.Errorf("%s dirty after the key-free repair; want it untouched", table)
			}
		}
		if n := repairKeyCount(t, ctx, conn, ""); n != 0 {
			t.Errorf("key-free repair wrote the repair key (%d row(s))", n)
		}
		if got := journalDump(t, ctx, conn); got != journalBefore {
			t.Errorf("journal changed during the wisp-only repair:\n got %s\nwant %s", got, journalBefore)
		}
		if got := scalarInt(t, ctx, conn, "SELECT MAX(version) FROM ignored_schema_migrations"); got != schema.LatestIgnoredVersion() {
			t.Errorf("ignored cursor = %d, want %d", got, schema.LatestIgnoredVersion())
		}
	})

	t.Run("pull-adopted clone", func(t *testing.T) {
		requirePullAdoptedCloneRepairsOnlyWisps(t, t.Context())
	})
}

// requirePullAdoptedCloneRepairsOnlyWisps drives a real adoption over a
// file:// remote. The origin publishes the 0059-era over-set at main 74 and
// the adopter clones it; the origin then migrates (recording and consuming
// the owed key in its repair commit) and pushes. The adopter pulls that
// repaired history while clean, so it holds main 75 with no key at HEAD, then
// plants stale clone-local wisps (one waiting on an issue across planes) and
// rewinds its ignored cursor to 31. Its open runs only the key-free 0032 pass.
func requirePullAdoptedCloneRepairsOnlyWisps(t *testing.T, ctx context.Context) {
	t.Helper()
	originData := seedMainSchemaAt(t, ctx, 74)
	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("create remote dir: %v", err)
	}
	remoteURL := "file://" + remoteDir

	withRawConn(t, originData, preflipDatabase, func(conn *sql.Conn) {
		insertStampedIssues(t, ctx, conn, "issues", []stampedIssue{
			{id: "wd-blocker", status: "open", stamp: historicalStamp(1)},
			{id: "wd-blocked", status: "open", stamp: historicalStamp(2), wantBlocked: true},
			{id: "wd-related", status: "open", stamp: historicalStamp(3), wantBlocked: true},
		}, true)
		insertIssueEdges(t, ctx, conn, [][3]string{
			{"wd-blocked", "wd-blocker", "blocks"},
			{"wd-related", "wd-blocked", "relates-to"},
		})
		commitFixture(t, ctx, conn, "test: v74 store crossed 0059 on an engine with dolt#11886")
		mustExecOn(t, ctx, conn, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})

	_, cloneData := cloneLineage(t, ctx, remoteURL)

	withRawConn(t, originData, preflipDatabase, func(conn *sql.Conn) {
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (origin): %v", err)
		}
		if n := repairCommitCount(t, ctx, conn); n != 1 {
			t.Fatalf("origin repair commits = %d, want 1", n)
		}
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})

	withRawConn(t, cloneData, preflipDatabase, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_PULL('origin')")
		if got := scalarInt(t, ctx, conn, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != 75 {
			t.Fatalf("clone main cursor = %d after pull, want 75", got)
		}
		if readIsBlocked(t, ctx, conn, "issues", "wd-related") {
			t.Fatal("pull delivered an uncorrected flag: wd-related still blocked at origin HEAD")
		}
		if n := repairKeyCount(t, ctx, conn, " AS OF 'HEAD'"); n != 0 {
			t.Fatalf("pulled history still owes the repair (%d key row(s)); want a key-free clone", n)
		}
		// The clone's first open builds its clone-local plane.
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (clone's first open): %v", err)
		}
		// w-free has no edges but carries a 0059-era over-set; w-waited waits
		// on the genuinely blocked issue wd-blocked and carries a stale clear.
		mustExecOn(t, ctx, conn,
			"INSERT INTO wisps (id, title, status, is_blocked, updated_at) VALUES ('w-free', 'no edges', 'open', 1, ?), ('w-waited', 'waits on wd-blocked', 'open', 0, ?)",
			historicalStamp(4), historicalStamp(5))
		mustExecOn(t, ctx, conn,
			"INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata) VALUES (?, 'w-waited', 'wd-blocked', 'blocks', NOW(), 'fixture', JSON_OBJECT())",
			depid.New("w-waited", "wd-blocked"))
		mustExecOn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version > 31")
		headBefore := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')")
		journalBefore := journalDump(t, ctx, conn)

		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (clone): %v", err)
		}

		if readIsBlocked(t, ctx, conn, "wisps", "w-free") {
			t.Error("wisps w-free: is_blocked = true after the key-free repair, want false")
		}
		if !readIsBlocked(t, ctx, conn, "wisps", "w-waited") {
			t.Error("wisps w-waited: is_blocked = false after the key-free repair, want true")
		}
		requireStamp(t, ctx, conn, "wisps", "w-free", historicalStamp(4))
		requireStamp(t, ctx, conn, "wisps", "w-waited", historicalStamp(5))
		requireStamp(t, ctx, conn, "issues", "wd-related", historicalStamp(3))
		if got := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')"); got != headBefore {
			t.Errorf("HEAD moved from %s to %s; the key-free repair must not commit", headBefore, got)
		}
		for _, table := range dirtyTableNames(t, ctx, conn) {
			if table == "issues" || table == "metadata" || table == "dependencies" {
				t.Errorf("%s dirty after the key-free repair; want it untouched", table)
			}
		}
		if n := repairKeyCount(t, ctx, conn, ""); n != 0 {
			t.Errorf("key-free repair wrote the repair key (%d row(s))", n)
		}
		if got := journalDump(t, ctx, conn); got != journalBefore {
			t.Errorf("journal changed during the wisp-only repair:\n got %s\nwant %s", got, journalBefore)
		}
		if !ignoredCursorHas(t, ctx, conn, 32) {
			t.Error("ignored cursor does not record 0032 after the key-free pass")
		}
		requireFullRecomputeCorrectsNothing(t, ctx, conn)
	})
}

// healthyFlags returns rows seeded with the flags their graph implies.
func healthyFlags(rows []repairRow) []repairRow {
	out := make([]repairRow, len(rows))
	for i, r := range rows {
		out[i] = r
		out[i].stale = r.wantBlocked
	}
	return out
}

func repairCommitCount(t *testing.T, ctx context.Context, conn *sql.Conn) int {
	t.Helper()
	return scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log WHERE message = ?",
		"schema: repair is_blocked flags left by 0059 (gastownhall/beads#7037)")
}

// TestEmbeddedMigration0075HealthyStoreIsUnchanged is AC-3: upgrading a 74/31
// store whose flags already match its graph changes no flag, timestamp or
// journal row on either plane, consumes the owed key in exactly one repair
// commit, and a reopen on a fresh engine handle finds nothing owed and
// commits nothing.
//
// The journal baseline includes a real journal-activated mutation (a project
// with journaling on), so an exemption that only held while journaling was
// off would show up as a new row or a moved seq.
func TestEmbeddedMigration0075HealthyStoreIsUnchanged(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir, conn, closeConn := seedRewoundStore(t, ctx, 74, 31)
	closed := false
	defer func() {
		if !closed {
			closeConn()
		}
	}()
	issues, wisps := healthyFlags(repairGraph), healthyFlags(wispRepairGraph)
	insertRepairRows(t, ctx, conn, "issues", issues)
	insertCanonicalEdges(t, ctx, conn, "dependencies", "depends_on_issue_id", repairEdges)
	insertRepairRows(t, ctx, conn, "wisps", wisps)
	insertCanonicalEdges(t, ctx, conn, "wisp_dependencies", "depends_on_wisp_id", wispRepairEdges)
	seedJournal(t, ctx, conn)
	insertStampedIssues(t, ctx, conn, "issues", []stampedIssue{{id: "rp-watched", status: "open", stamp: historicalStamp(9)}}, true)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	unscope := issueops.ScopeEventsJournalTransaction(tx, true)
	if err := issueops.MergeMetadataInTx(ctx, tx, "rp-watched", "watched-by", json.RawMessage(`"consumer-cursor"`), "fixture"); err != nil {
		unscope()
		_ = tx.Rollback()
		t.Fatalf("journaled MergeMetadataInTx: %v", err)
	}
	unscope()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit journaled mutation: %v", err)
	}
	commitFixture(t, ctx, conn, "test: healthy v74 store")
	journalBefore := journalDump(t, ctx, conn)
	if !strings.Contains(journalBefore, "rp-watched") {
		t.Fatalf("fixture: journaled mutation wrote no journal row:\n%s", journalBefore)
	}
	// The journaled mutation re-stamped rp-watched; that stamp is the baseline.
	watchedStamp := readUpdatedAt(t, ctx, conn, "issues", "rp-watched")

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	requireFlags(t, ctx, conn, "issues", issues)
	requireFlags(t, ctx, conn, "wisps", wisps)
	requireStamps(t, ctx, conn, "issues", issues)
	requireStamps(t, ctx, conn, "wisps", wisps)
	requireStamp(t, ctx, conn, "issues", "rp-watched", watchedStamp)
	if got := journalDump(t, ctx, conn); got != journalBefore {
		t.Errorf("journal changed on a healthy store:\n got %s\nwant %s", got, journalBefore)
	}
	if n := repairKeyCount(t, ctx, conn, ""); n != 0 {
		t.Errorf("repair key present after the upgrade (%d row(s))", n)
	}
	if n := repairKeyCount(t, ctx, conn, " AS OF 'HEAD'"); n != 0 {
		t.Errorf("repair key committed at HEAD after the upgrade (%d row(s))", n)
	}
	if n := repairCommitCount(t, ctx, conn); n != 1 {
		t.Fatalf("repair commits after the upgrade = %d, want 1 (the one that consumes the owed key)", n)
	}
	headBefore := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')")
	closeConn()
	closed = true

	// Reopen on a fresh engine handle: everything it sees is what the first
	// pass published.
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("reopen MigrateUp: %v", err)
		}
		if got := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')"); got != headBefore {
			t.Errorf("reopen moved HEAD from %s to %s; want no commit", headBefore, got)
		}
		if n := repairCommitCount(t, ctx, conn); n != 1 {
			t.Errorf("repair commits after reopen = %d, want 1", n)
		}
		requireFlags(t, ctx, conn, "issues", issues)
		requireFlags(t, ctx, conn, "wisps", wisps)
		requireStamps(t, ctx, conn, "issues", issues)
		requireStamps(t, ctx, conn, "wisps", wisps)
		requireStamp(t, ctx, conn, "issues", "rp-watched", watchedStamp)
		if got := journalDump(t, ctx, conn); got != journalBefore {
			t.Errorf("journal changed on reopen:\n got %s\nwant %s", got, journalBefore)
		}
		if n := repairKeyCount(t, ctx, conn, " AS OF 'HEAD'"); n != 0 {
			t.Errorf("repair key committed at HEAD after reopen (%d row(s))", n)
		}
	})
}
