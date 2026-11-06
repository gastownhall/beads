//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// depGraphAt renders the dependencies table at rev ("" for the working set).
func depGraphAt(t *testing.T, ctx context.Context, conn *sql.Conn, rev string) []string {
	t.Helper()
	asOf := ""
	if rev != "" {
		asOf = " AS OF '" + rev + "'"
	}
	rows, err := conn.QueryContext(ctx, "SELECT id, issue_id, COALESCE(depends_on_issue_id, ''), COALESCE(depends_on_external, ''), type FROM dependencies"+asOf+" ORDER BY issue_id, id")
	if err != nil {
		t.Fatalf("read dependencies%s: %v", asOf, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, issueID, target, external, typ string
		if err := rows.Scan(&id, &issueID, &target, &external, &typ); err != nil {
			t.Fatalf("scan dependencies: %v", err)
		}
		out = append(out, strings.Join([]string{id, issueID, target, external, typ}, " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("dependencies rows: %v", err)
	}
	return out
}

// TestEmbeddedMigration0075RepairCommitCarriesItsGraph is AC-4: the dependency
// rekey of the same pass rewrites random ids and collapses cross-column
// duplicates, and the repair commit publishes that graph together with the
// flags derived from it, changing nothing else but the owed key.
func TestEmbeddedMigration0075RepairCommitCarriesItsGraph(t *testing.T) {
	requireEmbedded(t)

	t.Run("rekey rewrites ids and drops an external twin", func(t *testing.T) {
		ctx := t.Context()
		_, conn, closeConn := seedRewoundStore(t, ctx, 74, 31)
		defer closeConn()
		insertRepairRows(t, ctx, conn, "issues", repairGraph)
		// Random UUID ids: the rekey must rewrite every one of them.
		insertIssueEdges(t, ctx, conn, repairEdges)
		// The external twin of rp-blocked -blocks-> rp-blocker, already sitting on
		// the deterministic id: the rekey drops it and moves the issue ref there.
		seedDependency(t, ctx, conn, depid.New("rp-blocked", "rp-blocker"), "rp-blocked", nil, nil, strptr("rp-blocker"))
		commitFixture(t, ctx, conn, "test: v74 store with stale flags and a pre-rekey graph")

		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp: %v", err)
		}

		repair := repairCommitHash(t, ctx, conn)
		final := depGraphAt(t, ctx, conn, "")
		if len(final) != len(repairEdges) {
			t.Fatalf("dependencies after the pass = %v, want %d deduplicated edges", final, len(repairEdges))
		}
		if got := depGraphAt(t, ctx, conn, repair); !slices.Equal(got, final) {
			t.Errorf("dependencies at the repair commit:\n got %v\nwant the graph its flags were derived from %v", got, final)
		}
		for _, r := range repairGraph {
			if got := isBlockedAt(t, ctx, conn, repair, r.id); got != r.wantBlocked {
				t.Errorf("issues %s at the repair commit: is_blocked = %v, want %v", r.id, got, r.wantBlocked)
			}
		}
		requireStamps(t, ctx, conn, "issues", repairGraph)
		requireRepairCommitScope(t, ctx, conn, repair)
		requireFullRecomputeCorrectsNothing(t, ctx, conn)
	})

	// The #5268 shape: rd-dup holds one logical edge twice, issue-typed
	// relates-to (never blocks) and wisp-typed blocks against an open wisp
	// (blocks). Both derive one deterministic id; the rekey keeps the
	// issue-typed survivor, and that collapse alone flips rd-dup from blocked
	// to free. Flags published without the deduplicated graph (or the reverse)
	// would disagree with each other at the repair commit.
	t.Run("cross-column dedup changes the derived flag", func(t *testing.T) {
		ctx := t.Context()
		_, conn, closeConn := seedRewoundStore(t, ctx, 74, 31)
		defer closeConn()
		insertStampedIssues(t, ctx, conn, "issues", []stampedIssue{
			{id: "rd-blocker", status: "open", stamp: historicalStamp(1)},
			{id: "rd-blocked", status: "open", stamp: historicalStamp(2), wantBlocked: true},
			// Stale: a 0059-era over-set.
			{id: "rd-related", status: "open", stamp: historicalStamp(3), wantBlocked: true},
			{id: "rd-twin", status: "open", stamp: historicalStamp(4)},
			// Correct for the pre-dedup graph, wrong once the duplicate collapses.
			{id: "rd-dup", status: "open", stamp: historicalStamp(5), wantBlocked: true},
		}, true)
		mustExecOn(t, ctx, conn,
			"INSERT INTO wisps (id, title, status, is_blocked, updated_at) VALUES ('rd-twin', 'open wisp twin of the issue id', 'open', 0, ?)",
			historicalStamp(6))
		insertIssueEdges(t, ctx, conn, [][3]string{
			{"rd-blocked", "rd-blocker", "blocks"},
			{"rd-related", "rd-blocked", "relates-to"},
		})
		mustExecOn(t, ctx, conn, `
INSERT INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, type, created_at, created_by, metadata) VALUES
  (UUID(), 'rd-dup', 'rd-twin', NULL, 'relates-to', NOW(), 'fixture', JSON_OBJECT()),
  (UUID(), 'rd-dup', NULL, 'rd-twin', 'blocks', NOW(), 'fixture', JSON_OBJECT())`)
		commitFixture(t, ctx, conn, "test: v74 store with a #5268 duplicate edge and stale flags")

		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp: %v", err)
		}

		repair := repairCommitHash(t, ctx, conn)
		var survivors int
		var survivorType string
		var issueTyped, wispTyped bool
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*), MAX(type), MAX(depends_on_issue_id IS NOT NULL), MAX(depends_on_wisp_id IS NOT NULL) FROM dependencies AS OF '"+repair+"' WHERE issue_id = 'rd-dup'").
			Scan(&survivors, &survivorType, &issueTyped, &wispTyped); err != nil {
			t.Fatalf("read rd-dup edges at the repair commit: %v", err)
		}
		if survivors != 1 || survivorType != "relates-to" || !issueTyped || wispTyped {
			t.Fatalf("rd-dup at the repair commit: %d edge(s), type %q, issue-typed %v, wisp-typed %v; want the one issue-typed relates-to survivor",
				survivors, survivorType, issueTyped, wispTyped)
		}
		for id, want := range map[string]bool{
			"rd-blocker": false,
			"rd-blocked": true,
			"rd-related": false,
			"rd-twin":    false,
			"rd-dup":     false,
		} {
			if got := isBlockedAt(t, ctx, conn, repair, id); got != want {
				t.Errorf("issues %s at the repair commit: is_blocked = %v, want %v (the flags of that commit's deduplicated graph)", id, got, want)
			}
		}
		requireStamp(t, ctx, conn, "issues", "rd-dup", historicalStamp(5))
		requireStamp(t, ctx, conn, "issues", "rd-related", historicalStamp(3))
		requireRepairCommitScope(t, ctx, conn, repair)
		requireFullRecomputeCorrectsNothing(t, ctx, conn)
	})
}

// repairCommitHash returns the one commit carrying the is_blocked repair.
func repairCommitHash(t *testing.T, ctx context.Context, conn *sql.Conn) string {
	t.Helper()
	if n := repairCommitCount(t, ctx, conn); n != 1 {
		t.Fatalf("repair commits = %d, want 1", n)
	}
	var repair string
	if err := conn.QueryRowContext(ctx, "SELECT commit_hash FROM dolt_log WHERE message = ?",
		"schema: repair is_blocked flags left by 0059 (gastownhall/beads#7037)").Scan(&repair); err != nil {
		t.Fatalf("find the repair commit: %v", err)
	}
	return repair
}

// isBlockedAt reads an issue's is_blocked as of a commit.
func isBlockedAt(t *testing.T, ctx context.Context, conn *sql.Conn, rev, id string) bool {
	t.Helper()
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT is_blocked FROM issues AS OF '"+rev+"' WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("read %s at %s: %v", id, rev, err)
	}
	return got
}

// requireRepairCommitScope asserts the repair commit changed dependencies,
// issue flags and the owed key, and nothing else: no other table, no issue row
// added, removed or re-stamped, no other metadata key.
func requireRepairCommitScope(t *testing.T, ctx context.Context, conn *sql.Conn, repair string) {
	t.Helper()
	parent := repair + "~1"
	rows, err := conn.QueryContext(ctx, "SELECT to_table_name FROM dolt_diff_summary(?, ?)", parent, repair)
	if err != nil {
		t.Fatalf("diff summary of the repair commit: %v", err)
	}
	var changed []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan diff summary: %v", err)
		}
		changed = append(changed, name)
	}
	rows.Close()
	sort.Strings(changed)
	if want := []string{"dependencies", "issues", "metadata"}; !slices.Equal(changed, want) {
		t.Errorf("repair commit changed tables %v, want %v", changed, want)
	}
	// dolt_diff takes no bind parameters; both revisions are commit hashes.
	diffOf := "dolt_diff('" + parent + "', '" + repair + "', "
	if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM "+diffOf+"'issues') WHERE diff_type <> 'modified' OR from_updated_at <> to_updated_at"); n != 0 {
		t.Errorf("repair commit added, removed or re-stamped %d issue row(s); want flag updates only", n)
	}
	if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM "+diffOf+"'metadata') WHERE COALESCE(to_key, from_key) <> ?", isBlockedRepairOwedKey); n != 0 {
		t.Errorf("repair commit changed %d metadata row(s) other than the owed key", n)
	}
}

// seedCanonicalCorruptStore is seedCorruptStore with canonically keyed edges
// and no wisps, so the pass has no rekey of its own and only the repair's
// guards decide what happens to a dirty working set.
func seedCanonicalCorruptStore(t *testing.T, ctx context.Context) (*sql.Conn, func()) {
	t.Helper()
	_, conn, closeConn := seedRewoundStore(t, ctx, 74, 31)
	insertRepairRows(t, ctx, conn, "issues", repairGraph)
	insertCanonicalEdges(t, ctx, conn, "dependencies", "depends_on_issue_id", repairEdges)
	commitFixture(t, ctx, conn, "test: v74 store with flags left by dolt#11886")
	return conn, closeConn
}

// refusalShape is a store an owed repair would run on, with where its main
// cursor and committed key stand before the pass.
type refusalShape struct {
	name      string
	seed      func(t *testing.T, ctx context.Context) (*sql.Conn, func())
	mainAt    int
	keyAtHead int
}

// requireNothingRepaired asserts a refused pass left the store as it was: the
// main cursor and the key (working set and HEAD) where they started, stale
// flags, HEAD where it started.
func requireNothingRepaired(t *testing.T, ctx context.Context, conn *sql.Conn, headBefore string, shape refusalShape) {
	t.Helper()
	if got := scalarInt(t, ctx, conn, "SELECT MAX(version) FROM schema_migrations"); got != shape.mainAt {
		t.Errorf("main cursor = %d after the refusal, want %d", got, shape.mainAt)
	}
	if n := repairKeyCount(t, ctx, conn, ""); n != shape.keyAtHead {
		t.Errorf("repair key rows = %d after the refusal, want %d", n, shape.keyAtHead)
	}
	if n := repairKeyCount(t, ctx, conn, " AS OF 'HEAD'"); n != shape.keyAtHead {
		t.Errorf("repair key rows at HEAD = %d after the refusal, want %d", n, shape.keyAtHead)
	}
	if got := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')"); got != headBefore {
		t.Errorf("HEAD moved from %s to %s before the refusal", headBefore, got)
	}
	if n := repairCommitCount(t, ctx, conn); n != 0 {
		t.Errorf("refused pass minted %d repair commit(s)", n)
	}
	requireFlags(t, ctx, conn, "issues", staleFlags(repairGraph))
}

// crossingShape is the 74/31 store whose pass crosses main 0075.
var crossingShape = refusalShape{name: "crossing", seed: seedCanonicalCorruptStore, mainAt: 74}

// TestEmbeddedMigration0075RefusesBeforeRepairWrites is AC-5: without main
// consent the pass refuses with the typed consent error, and with consent an
// uncommitted change to any repair input (issues, dependencies, metadata)
// refuses with the typed dirty-table error naming exactly that table, both
// before the key, a flag or a cursor is written. Dirt in a table the repair
// never commits does not refuse.
func TestEmbeddedMigration0075RefusesBeforeRepairWrites(t *testing.T) {
	requireEmbedded(t)

	t.Run("no main consent", func(t *testing.T) {
		ctx := t.Context()
		conn, closeConn := seedCanonicalCorruptStore(t, ctx)
		defer closeConn()
		headBefore := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')")
		dirtyBefore := dirtyTableNames(t, ctx, conn)
		schema.SetLocalMigrateConsent(false)
		schema.SetForceAllowRemoteMigrate(false)
		t.Cleanup(func() { schema.SetLocalMigrateConsent(true) })
		t.Setenv(schema.AllowMigrateEnv, "")
		t.Setenv(schema.AllowRemoteMigrateEnv, "")

		_, err := schema.MigrateUp(ctx, conn)
		var consentErr *schema.MigrateConsentError
		if !errors.As(err, &consentErr) {
			t.Fatalf("MigrateUp without consent: err = %v, want *schema.MigrateConsentError", err)
		}
		requireNothingRepaired(t, ctx, conn, headBefore, crossingShape)
		if dirty := dirtyTableNames(t, ctx, conn); !slices.Equal(dirty, dirtyBefore) {
			t.Errorf("refused pass changed the dirty tables from %v to %v; consent precedes every write", dirtyBefore, dirty)
		}
	})

	// Each edit is paired with a probe that reads 1 while the user's value is
	// still in the working set.
	dirtyEdits := map[string][2]string{
		"issues": {
			"UPDATE issues SET title = 'edited by hand' WHERE id = 'rp-free'",
			"SELECT COUNT(*) FROM issues WHERE id = 'rp-free' AND title = 'edited by hand'",
		},
		"dependencies": {
			"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata) VALUES ('" + depid.New("rp-free", "rp-blocker") + "', 'rp-free', 'rp-blocker', 'relates-to', NOW(), 'fixture', JSON_OBJECT())",
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = 'rp-free' AND depends_on_issue_id = 'rp-blocker' AND type = 'relates-to'",
		},
		"metadata": {
			"INSERT INTO metadata (`key`, value) VALUES ('fixture_note', 'uncommitted')",
			"SELECT COUNT(*) FROM metadata WHERE `key` = 'fixture_note' AND value = 'uncommitted'",
		},
	}
	for _, shape := range []refusalShape{crossingShape} {
		for _, table := range []string{"issues", "dependencies", "metadata"} {
			t.Run(shape.name+"/dirty "+table, func(t *testing.T) {
				ctx := t.Context()
				conn, closeConn := shape.seed(t, ctx)
				defer closeConn()
				headBefore := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')")
				ignoredHad32 := ignoredCursorHas(t, ctx, conn, 32)
				mustExecOn(t, ctx, conn, dirtyEdits[table][0])

				_, err := schema.MigrateUp(ctx, conn)
				var dirtyErr *schema.DirtyTablesError
				if !errors.As(err, &dirtyErr) {
					t.Fatalf("MigrateUp over dirty %s: err = %v, want *schema.DirtyTablesError", table, err)
				}
				if !slices.Equal(dirtyErr.Tables, []string{table}) {
					t.Errorf("DirtyTablesError names %v, want exactly [%s]", dirtyErr.Tables, table)
				}
				requireNothingRepaired(t, ctx, conn, headBefore, shape)
				if got := ignoredCursorHas(t, ctx, conn, 32); got != ignoredHad32 {
					t.Errorf("ignored cursor row 32 present = %v after the refusal, want %v", got, ignoredHad32)
				}
				if got := scalarInt(t, ctx, conn, dirtyEdits[table][1]); got != 1 {
					t.Errorf("user edit to %s lost by the refused pass", table)
				}
				if !contains(dirtyTableNames(t, ctx, conn), table) {
					t.Errorf("user edit to %s no longer uncommitted after the refused pass", table)
				}
			})
		}
	}

	t.Run("unrelated dirt does not refuse", func(t *testing.T) {
		ctx := t.Context()
		conn, closeConn := seedCanonicalCorruptStore(t, ctx)
		defer closeConn()
		mustExecOn(t, ctx, conn, "INSERT INTO custom_statuses (name, category) VALUES ('user-hold', 'unspecified')")

		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp with only custom_statuses dirty: %v", err)
		}
		requireFlags(t, ctx, conn, "issues", healthyFlags(repairGraph))
		if n := repairKeyCount(t, ctx, conn, ""); n != 0 {
			t.Errorf("repair key rows = %d after the repair, want 0", n)
		}
		if n := repairCommitCount(t, ctx, conn); n != 1 {
			t.Errorf("repair commits = %d, want 1", n)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM custom_statuses WHERE name = 'user-hold'"); got != 1 {
			t.Errorf("unrelated user edit lost by the pass (%d rows)", got)
		}
		if !contains(dirtyTableNames(t, ctx, conn), "custom_statuses") {
			t.Error("unrelated user edit was committed by the pass; it must stay uncommitted")
		}
	})
}
