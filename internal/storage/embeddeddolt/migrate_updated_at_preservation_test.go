//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests pin gastownhall/beads#5981: frozen migrations that rewrite issue
// or wisp rows without assigning updated_at must not let the column's
// ON UPDATE CURRENT_TIMESTAMP clause overwrite historical last-modified times.
// They run the real migrations through the real runner on an embedded store
// seeded below the affected step.

// historicalStamp is a distinct, second-precision, months-old timestamp per
// fixture row, so a migration-time stamp can never collide with it.
func historicalStamp(n int) time.Time {
	return time.Date(2025, time.March, 1+n, 9, 30, n, 0, time.UTC)
}

type stampedIssue struct {
	id, status  string
	issueType   string
	ephemeral   bool
	stamp       time.Time
	wantBlocked bool
}

// blockingGraph is a v45+ issue fixture with open and closed blockers and a
// parent-child edge under a blocked parent:
//
//	blocked    -blocks-> blocker-open    (open blocker: blocked)
//	unblocked  -blocks-> blocker-closed  (closed blocker: not blocked)
//	child      -parent-child-> blocked   (inherits its parent's block)
//	closed-dep -blocks-> blocker-open    (closed itself: never blocked)
//	free                                 (no edges)
var blockingGraph = []stampedIssue{
	{id: "ua-blocker-open", status: "open", stamp: historicalStamp(1)},
	{id: "ua-blocker-closed", status: "closed", stamp: historicalStamp(2)},
	{id: "ua-blocked", status: "open", stamp: historicalStamp(3), wantBlocked: true},
	{id: "ua-unblocked", status: "open", stamp: historicalStamp(4)},
	{id: "ua-child", status: "open", stamp: historicalStamp(5), wantBlocked: true},
	{id: "ua-closed-dep", status: "closed", stamp: historicalStamp(6)},
	{id: "ua-free", status: "open", stamp: historicalStamp(7)},
}

var blockingEdges = [][3]string{
	{"ua-blocked", "ua-blocker-open", "blocks"},
	{"ua-unblocked", "ua-blocker-closed", "blocks"},
	{"ua-child", "ua-blocked", "parent-child"},
	{"ua-closed-dep", "ua-blocker-open", "blocks"},
}

// insertStampedIssues writes rows straight into table with their historical
// updated_at. withIsBlocked seeds is_blocked to each row's correct value, the
// shape a store already past 0046 holds.
func insertStampedIssues(t *testing.T, ctx context.Context, conn *sql.Conn, table string, rows []stampedIssue, withIsBlocked bool) {
	t.Helper()
	for _, r := range rows {
		issueType := r.issueType
		if issueType == "" {
			issueType = "task"
		}
		var closedAt any
		if r.status == "closed" {
			closedAt = r.stamp
		}
		cols := "id, title, description, design, acceptance_criteria, notes, status, issue_type, ephemeral, created_at, updated_at, closed_at"
		vals := "?, ?, '', '', '', '', ?, ?, ?, ?, ?, ?"
		args := []any{r.id, "fixture " + r.id, r.status, issueType, r.ephemeral, r.stamp, r.stamp, closedAt}
		if withIsBlocked {
			cols += ", is_blocked"
			vals += ", ?"
			args = append(args, r.wantBlocked)
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO "+table+" ("+cols+") VALUES ("+vals+")", args...); err != nil {
			t.Fatalf("seed %s row %s: %v", table, r.id, err)
		}
	}
}

func insertIssueEdges(t *testing.T, ctx context.Context, conn *sql.Conn, edges [][3]string) {
	t.Helper()
	for _, e := range edges {
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES (UUID(), ?, ?, ?, 'fixture')",
			e[0], e[1], e[2]); err != nil {
			t.Fatalf("seed dependency %v: %v", e, err)
		}
	}
}

func commitFixture(t *testing.T, ctx context.Context, conn *sql.Conn, msg string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		t.Fatalf("DOLT_ADD -A: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?)", msg); err != nil {
		t.Fatalf("DOLT_COMMIT fixture: %v", err)
	}
}

// readUpdatedAt reads one row's updated_at from table (which may carry an
// AS OF clause).
func readUpdatedAt(t *testing.T, ctx context.Context, conn *sql.Conn, table, id string) time.Time {
	t.Helper()
	var got time.Time
	if err := conn.QueryRowContext(ctx, "SELECT updated_at FROM "+table+" WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("read %s updated_at of %s: %v", table, id, err)
	}
	return got
}

func readIsBlocked(t *testing.T, ctx context.Context, conn *sql.Conn, table, id string) bool {
	t.Helper()
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT is_blocked FROM "+table+" WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("read %s is_blocked of %s: %v", table, id, err)
	}
	return got
}

// readEphemeral reads one row's ephemeral flag from table.
func readEphemeral(t *testing.T, ctx context.Context, conn *sql.Conn, table, id string) bool {
	t.Helper()
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT ephemeral FROM "+table+" WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("read %s ephemeral of %s: %v", table, id, err)
	}
	return got
}

// requireGone fails if table still holds a row with id.
func requireGone(t *testing.T, ctx context.Context, conn *sql.Conn, table, id string) {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id = ?", id).Scan(&n); err != nil {
		t.Fatalf("count %s rows for %s: %v", table, id, err)
	}
	if n != 0 {
		t.Errorf("%s still holds %s after the move (%d row(s))", table, id, n)
	}
}

func requireStamp(t *testing.T, ctx context.Context, conn *sql.Conn, table, id string, want time.Time) {
	t.Helper()
	if got := readUpdatedAt(t, ctx, conn, table, id); !got.Equal(want) {
		t.Errorf("%s %s: updated_at = %s, want its historical %s (the migration overwrote it)",
			table, id, got.UTC().Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}

// TestEmbeddedMigrate0046And0047KeepIssueUpdatedAt is AC-1: a 1.0.x-shaped
// store (v45, no is_blocked column) migrated across 0046 and 0047 keeps every
// issue's historical updated_at while the backfilled is_blocked values follow
// the dependency graph.
func TestEmbeddedMigrate0046And0047KeepIssueUpdatedAt(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 45)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	insertStampedIssues(t, ctx, conn, "issues", blockingGraph, false)
	insertIssueEdges(t, ctx, conn, blockingEdges)
	commitFixture(t, ctx, conn, "test: v45 issues with historical updated_at")

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	for _, r := range blockingGraph {
		requireStamp(t, ctx, conn, "issues", r.id, r.stamp)
		if got := readIsBlocked(t, ctx, conn, "issues", r.id); got != r.wantBlocked {
			t.Errorf("issues %s: is_blocked = %v, want %v", r.id, got, r.wantBlocked)
		}
	}
}

// TestEmbeddedMigrate0047StepCommitKeepsIssueUpdatedAt is AC-2: the timestamp
// is restored before 0047's per-step commit, not by some later pass. The pass
// is stopped right after step 0047 commits; both the working row and the row
// in that step's commit must hold the historical value. The blocked issue is
// seeded already blocked, so only 0047's intermediate is_blocked = 0 (and its
// recompute back to 1) touch it.
func TestEmbeddedMigrate0047StepCommitKeepsIssueUpdatedAt(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 46)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	insertStampedIssues(t, ctx, conn, "issues", blockingGraph, true)
	insertIssueEdges(t, ctx, conn, blockingEdges)
	commitFixture(t, ctx, conn, "test: v46 issues with historical updated_at")

	restore := schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, version int) error {
		if version == 47 {
			return fmt.Errorf("injected fault: stop after migration %d", version)
		}
		return nil
	})
	_, err := schema.MigrateUp(ctx, conn)
	restore()
	if err == nil || !strings.Contains(err.Error(), "injected fault") {
		t.Fatalf("MigrateUp err = %v, want the injected fault after 0047", err)
	}
	if v := currentMainVersion(t, ctx, conn); v != 47 {
		t.Fatalf("main schema version = %d, want 47 (the pass must stop right after 0047)", v)
	}
	for _, r := range blockingGraph {
		requireStamp(t, ctx, conn, "issues", r.id, r.stamp)
		requireStamp(t, ctx, conn, "issues AS OF 'HEAD'", r.id, r.stamp)
	}
	if !readIsBlocked(t, ctx, conn, "issues AS OF 'HEAD'", "ua-blocked") {
		t.Errorf("issues AS OF HEAD ua-blocked: is_blocked = false, want true after 0047's recompute")
	}
}

// migrateUpStoppingAfter runs MigrateUp and stops the pass right after main
// step version commits, so assertions see that step's own result rather than
// anything a later migration did to the same rows.
func migrateUpStoppingAfter(t *testing.T, ctx context.Context, conn *sql.Conn, version int) {
	t.Helper()
	restore := schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, v int) error {
		if v == version {
			return fmt.Errorf("injected fault: stop after migration %d", v)
		}
		return nil
	})
	defer restore()
	if _, err := schema.MigrateUp(ctx, conn); err == nil || !strings.Contains(err.Error(), "injected fault") {
		t.Fatalf("MigrateUp err = %v, want the injected fault after %04d", err, version)
	}
	if v := currentMainVersion(t, ctx, conn); v != version {
		t.Fatalf("main schema version = %d, want %d", v, version)
	}
}

// TestEmbeddedMigrateRowMovesKeepUpdatedAt is AC-3: 0035 moves agent-class
// issues into wisps and 0053 moves rig wisps back into issues, each then
// flipping the moved rows' ephemeral flag. A moved row keeps its source's
// timestamp; a destination row that already held the ID (INSERT IGNORE kept
// it) keeps its own.
func TestEmbeddedMigrateRowMovesKeepUpdatedAt(t *testing.T) {
	requireEmbedded(t)

	t.Run("0035 issues to wisps", func(t *testing.T) {
		ctx := t.Context()
		dataDir := seedMainSchemaAt(t, ctx, 34)
		conn, closeConn := openPinnedConn(t, ctx, dataDir)
		defer closeConn()

		moved := stampedIssue{id: "ua-agent", status: "open", issueType: "agent", stamp: historicalStamp(11)}
		collidingSource := stampedIssue{id: "ua-agent-dup", status: "open", issueType: "agent", stamp: historicalStamp(12)}
		collidingDest := stampedIssue{id: "ua-agent-dup", status: "open", issueType: "agent", stamp: historicalStamp(13)}
		insertStampedIssues(t, ctx, conn, "issues", []stampedIssue{moved, collidingSource}, false)
		insertStampedIssues(t, ctx, conn, "wisps", []stampedIssue{collidingDest}, false)
		commitFixture(t, ctx, conn, "test: v34 agent issues with historical updated_at")

		migrateUpStoppingAfter(t, ctx, conn, 35)
		requireStamp(t, ctx, conn, "wisps", moved.id, moved.stamp)
		requireStamp(t, ctx, conn, "wisps", collidingDest.id, collidingDest.stamp)
		// The migration's own work still lands: the moved wisp is ephemeral
		// and both sources left issues.
		if !readEphemeral(t, ctx, conn, "wisps", moved.id) {
			t.Errorf("wisps %s: ephemeral = false, want true after 0035", moved.id)
		}
		requireGone(t, ctx, conn, "issues", moved.id)
		requireGone(t, ctx, conn, "issues", collidingSource.id)
	})

	t.Run("0053 wisps to issues", func(t *testing.T) {
		ctx := t.Context()
		dataDir := seedMainSchemaAt(t, ctx, 52)
		conn, closeConn := openPinnedConn(t, ctx, dataDir)
		defer closeConn()

		moved := stampedIssue{id: "ua-rig", status: "open", issueType: "rig", ephemeral: true, stamp: historicalStamp(21)}
		collidingSource := stampedIssue{id: "ua-rig-dup", status: "open", issueType: "rig", ephemeral: true, stamp: historicalStamp(22)}
		collidingDest := stampedIssue{id: "ua-rig-dup", status: "open", issueType: "rig", ephemeral: true, stamp: historicalStamp(23)}
		insertStampedIssues(t, ctx, conn, "wisps", []stampedIssue{moved, collidingSource}, false)
		insertStampedIssues(t, ctx, conn, "issues", []stampedIssue{collidingDest}, false)
		commitFixture(t, ctx, conn, "test: v52 rig wisps with historical updated_at")

		migrateUpStoppingAfter(t, ctx, conn, 53)
		requireStamp(t, ctx, conn, "issues", moved.id, moved.stamp)
		requireStamp(t, ctx, conn, "issues", collidingDest.id, collidingDest.stamp)
		requireStamp(t, ctx, conn, "issues AS OF 'HEAD'", moved.id, moved.stamp)
		// The migration's own work still lands: both rows in issues are no
		// longer ephemeral and the rig wisps are gone.
		for _, id := range []string{moved.id, collidingDest.id} {
			if readEphemeral(t, ctx, conn, "issues", id) {
				t.Errorf("issues %s: ephemeral = true, want false after 0053", id)
			}
		}
		requireGone(t, ctx, conn, "wisps", moved.id)
		requireGone(t, ctx, conn, "wisps", collidingSource.id)
	})
}

// TestEmbeddedMigrate0041KeepsDependencyTargetsUpdatedAt pins the join rule
// on a real upgrade. 0041 splits dependencies.depends_on_id with UPDATEs that
// join issues and wisps without assigning any of their columns, and the
// engine still stamps every row those joins match: every issue and wisp a
// dependency points at, blocking or not. A v40 store keeps every issue's and
// the wisp target's updated_at through 0041 (working set and step commit)
// and through the rest of the upgrade, while the split lands and the blocked
// issue is flagged.
func TestEmbeddedMigrate0041KeepsDependencyTargetsUpdatedAt(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 40)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	issues := []stampedIssue{
		{id: "ua-dep-src", status: "open", stamp: historicalStamp(41), wantBlocked: true},
		{id: "ua-dep-target", status: "open", stamp: historicalStamp(42)},
		{id: "ua-dep-parent", status: "open", issueType: "epic", stamp: historicalStamp(43)},
		{id: "ua-dep-child", status: "open", stamp: historicalStamp(44)},
		{id: "ua-dep-free", status: "open", stamp: historicalStamp(45)},
	}
	wispTarget := stampedIssue{id: "ua-dep-wisp-target", status: "open", ephemeral: true, stamp: historicalStamp(46)}
	insertStampedIssues(t, ctx, conn, "issues", issues, false)
	insertStampedIssues(t, ctx, conn, "wisps", []stampedIssue{wispTarget}, false)
	// A large TEXT value is stored out of band and reaches SQL functions as
	// *val.TextStorage, which stores written by historical releases hold;
	// the join rule's fingerprint must read it (JSON_ARRAY could not).
	large := strings.Repeat("historical design notes ", 2500) // ~60 KiB, under TEXT's limit at v40
	for _, row := range []struct{ table, id string }{{"issues", "ua-dep-target"}, {"wisps", wispTarget.id}} {
		var stamp time.Time
		if row.table == "wisps" {
			stamp = wispTarget.stamp
		} else {
			stamp = issues[1].stamp
		}
		if _, err := conn.ExecContext(ctx,
			"UPDATE "+row.table+" SET description = ?, design = ?, updated_at = ? WHERE id = ?", large, large, stamp, row.id); err != nil {
			t.Fatalf("give %s %s out-of-band text: %v", row.table, row.id, err)
		}
	}
	for _, e := range [][3]string{
		{"ua-dep-src", "ua-dep-target", "blocks"},
		{"ua-dep-child", "ua-dep-parent", "parent-child"},
		{"ua-dep-src", wispTarget.id, "related"},
		{"ua-dep-src", "external:gh-41", "related"},
	} {
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by) VALUES (?, ?, ?, '2025-01-01 00:00:00', 'fixture')",
			e[0], e[1], e[2]); err != nil {
			t.Fatalf("seed v40 dependency %v: %v", e, err)
		}
	}
	commitFixture(t, ctx, conn, "test: v40 dependency targets with historical updated_at")

	migrateUpStoppingAfter(t, ctx, conn, 41)
	for _, r := range issues {
		requireStamp(t, ctx, conn, "issues", r.id, r.stamp)
		requireStamp(t, ctx, conn, "issues AS OF 'HEAD'", r.id, r.stamp)
	}
	requireStamp(t, ctx, conn, "wisps", wispTarget.id, wispTarget.stamp)
	// The split itself still lands: each target in its own column.
	for _, q := range []string{
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = 'ua-dep-src' AND depends_on_issue_id = 'ua-dep-target'",
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = 'ua-dep-child' AND depends_on_issue_id = 'ua-dep-parent'",
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = 'ua-dep-src' AND depends_on_wisp_id = 'ua-dep-wisp-target'",
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = 'ua-dep-src' AND depends_on_external = 'external:gh-41'",
	} {
		var n int
		if err := conn.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if n != 1 {
			t.Errorf("%s = %d, want 1 after 0041's split", q, n)
		}
	}

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp to latest: %v", err)
	}
	for _, r := range issues {
		requireStamp(t, ctx, conn, "issues", r.id, r.stamp)
		if got := readIsBlocked(t, ctx, conn, "issues", r.id); got != r.wantBlocked {
			t.Errorf("issues %s: is_blocked = %v, want %v", r.id, got, r.wantBlocked)
		}
	}
	requireStamp(t, ctx, conn, "wisps", wispTarget.id, wispTarget.stamp)
}

// TestEmbeddedMigrateIgnored0011KeepsUpdatedAt pins the join rule on the
// ignored series: ignored 0011 folds a wisp's leftover child_counters row
// into wisp_child_counters with an UPDATE that joins wisps without assigning
// them, which stamps the wisp. The first pass on a fresh store stops right
// after ignored 0010 (the second step numbered 10), so the second pass
// genuinely executes ignored 0011, and is stopped right after it.
func TestEmbeddedMigrateIgnored0011KeepsUpdatedAt(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), ".beads", "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	boot, bootCleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "", "")
	if err != nil {
		t.Fatalf("OpenSQL boot: %v", err)
	}
	if _, err := boot.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS testdb"); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	_ = bootCleanup()
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	seen10 := 0
	restore := schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, v int) error {
		if v == 10 {
			if seen10++; seen10 == 2 {
				return fmt.Errorf("injected fault: stop after ignored migration %d", v)
			}
		}
		return nil
	})
	_, err = schema.MigrateUp(ctx, conn)
	restore()
	if err == nil || !strings.Contains(err.Error(), "injected fault") {
		t.Fatalf("first MigrateUp err = %v, want the injected fault after ignored 0010", err)
	}

	parent := stampedIssue{id: "ua-wisp-counter-parent", status: "open", ephemeral: true, stamp: historicalStamp(51)}
	bystander := stampedIssue{id: "ua-wisp-counter-bystander", status: "open", ephemeral: true, stamp: historicalStamp(52)}
	insertStampedIssues(t, ctx, conn, "wisps", []stampedIssue{parent, bystander}, false)
	for _, q := range []string{
		"SET FOREIGN_KEY_CHECKS = 0",
		"INSERT INTO child_counters (parent_id, last_child) VALUES ('ua-wisp-counter-parent', 5)",
		"SET FOREIGN_KEY_CHECKS = 1",
		"INSERT INTO wisp_child_counters (parent_id, last_child) VALUES ('ua-wisp-counter-parent', 3)",
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	commitFixture(t, ctx, conn, "test: leftover child counter for a wisp at ignored v10")

	restore = schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, v int) error {
		if v == 11 {
			return fmt.Errorf("injected fault: stop after ignored migration %d", v)
		}
		return nil
	})
	_, err = schema.MigrateUp(ctx, conn)
	restore()
	if err == nil || !strings.Contains(err.Error(), "injected fault") {
		t.Fatalf("second MigrateUp err = %v, want the injected fault after ignored 0011", err)
	}
	var cursor int
	if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations").Scan(&cursor); err != nil {
		t.Fatalf("read ignored cursor: %v", err)
	}
	if cursor != 11 {
		t.Fatalf("ignored cursor = %d, want 11 (ignored 0011 must have executed)", cursor)
	}
	requireStamp(t, ctx, conn, "wisps", parent.id, parent.stamp)
	requireStamp(t, ctx, conn, "wisps", bystander.id, bystander.stamp)
	// The fold itself still lands.
	var last int
	if err := conn.QueryRowContext(ctx, "SELECT last_child FROM wisp_child_counters WHERE parent_id = ?", parent.id).Scan(&last); err != nil {
		t.Fatalf("read wisp_child_counters: %v", err)
	}
	if last != 5 {
		t.Errorf("wisp_child_counters.last_child = %d, want 5 (GREATEST of both counters)", last)
	}
	var leftover int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM child_counters WHERE parent_id = ?", parent.id).Scan(&leftover); err != nil {
		t.Fatalf("count child_counters: %v", err)
	}
	if leftover != 0 {
		t.Errorf("child_counters still holds %s's counter after ignored 0011 (%d row(s))", parent.id, leftover)
	}
}

// TestEmbeddedMigrateIgnored0007KeepsUpdatedAt is AC-4: ignored migration
// 0007 clears and recomputes wisps.is_blocked. A fresh store's first pass is
// stopped right after ignored 0006 (the second step numbered 6: main 0006
// runs first), so the follow-up pass genuinely executes ignored 0007 against
// the seeded wisps rather than reconciling its cursor. That pass is stopped
// right after ignored 0007, so the assertions see 0007's own result.
func TestEmbeddedMigrateIgnored0007KeepsUpdatedAt(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), ".beads", "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	boot, bootCleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "", "")
	if err != nil {
		t.Fatalf("OpenSQL boot: %v", err)
	}
	if _, err := boot.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS testdb"); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	_ = bootCleanup()
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	seen6 := 0
	restore := schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, v int) error {
		if v == 6 {
			if seen6++; seen6 == 2 {
				return fmt.Errorf("injected fault: stop after ignored migration %d", v)
			}
		}
		return nil
	})
	_, err = schema.MigrateUp(ctx, conn)
	restore()
	if err == nil || !strings.Contains(err.Error(), "injected fault") {
		t.Fatalf("first MigrateUp err = %v, want the injected fault after ignored 0006", err)
	}
	ignoredVersion := func() int {
		var v int
		if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations").Scan(&v); err != nil {
			t.Fatalf("read ignored cursor: %v", err)
		}
		return v
	}
	if v := ignoredVersion(); v != 6 {
		t.Fatalf("ignored cursor = %d, want 6", v)
	}

	// A wisp blocked by an issue, a wisp blocked by another wisp, that wisp
	// blocker itself, and a free bystander wisp. The issue-blocked wisp is
	// already flagged blocked, so 0007 clears its flag and sets it again
	// (stamping it twice while its flag ends where it started); the
	// wisp-blocked one is flagged for the first time.
	blocker := stampedIssue{id: "ua-w-blocker", status: "open", stamp: historicalStamp(31)}
	blocked := stampedIssue{id: "ua-wisp-blocked", status: "open", ephemeral: true, stamp: historicalStamp(32), wantBlocked: true}
	free := stampedIssue{id: "ua-wisp-free", status: "open", ephemeral: true, stamp: historicalStamp(33)}
	wispBlocker := stampedIssue{id: "ua-wisp-blocker", status: "open", ephemeral: true, stamp: historicalStamp(34)}
	wispBlocked := stampedIssue{id: "ua-wisp-blocked-by-wisp", status: "open", ephemeral: true, stamp: historicalStamp(35), wantBlocked: true}
	insertStampedIssues(t, ctx, conn, "issues", []stampedIssue{blocker}, false)
	insertStampedIssues(t, ctx, conn, "wisps", []stampedIssue{blocked}, true)
	insertStampedIssues(t, ctx, conn, "wisps", []stampedIssue{free, wispBlocker, wispBlocked}, false)
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (issue_id, depends_on_issue_id, type, created_by) VALUES (?, ?, 'blocks', 'fixture')",
		blocked.id, blocker.id); err != nil {
		t.Fatalf("seed wisp dependency: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (issue_id, depends_on_wisp_id, type, created_by) VALUES (?, ?, 'blocks', 'fixture')",
		wispBlocked.id, wispBlocker.id); err != nil {
		t.Fatalf("seed wisp-to-wisp dependency: %v", err)
	}
	commitFixture(t, ctx, conn, "test: blocker issue for wisps at ignored v6")

	restore = schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, v int) error {
		if v == 7 {
			return fmt.Errorf("injected fault: stop after ignored migration %d", v)
		}
		return nil
	})
	_, err = schema.MigrateUp(ctx, conn)
	restore()
	if err == nil || !strings.Contains(err.Error(), "injected fault") {
		t.Fatalf("second MigrateUp err = %v, want the injected fault after ignored 0007", err)
	}
	if v := ignoredVersion(); v != 7 {
		t.Fatalf("ignored cursor = %d, want 7 (ignored 0007 must have executed)", v)
	}
	for _, w := range []stampedIssue{blocked, free, wispBlocker, wispBlocked} {
		requireStamp(t, ctx, conn, "wisps", w.id, w.stamp)
		if got := readIsBlocked(t, ctx, conn, "wisps", w.id); got != w.wantBlocked {
			t.Errorf("wisps %s: is_blocked = %v, want %v", w.id, got, w.wantBlocked)
		}
	}
}

// TestEmbeddedMigrateGuardRestoresAcrossBatches runs the restore through the
// real engine with more stamped rows than one restoring UPDATE carries
// (restoreBatchSize, 500): 1001 issues blocked by one open blocker, two thirds
// of them already flagged blocked (0047 clears and sets them again) and the
// rest flagged for the first time. The sqlmock runner test pins how the rows
// split into statements; this pins that the engine accepts and applies
// full-size batches, in the working set and in 0047's step commit.
func TestEmbeddedMigrateGuardRestoresAcrossBatches(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 46)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	const blockedCount = 2*500 + 1
	blocker := stampedIssue{id: "ua-batch-blocker", status: "open", stamp: historicalStamp(0)}
	insertStampedIssues(t, ctx, conn, "issues", []stampedIssue{blocker}, true)
	batchID := func(i int) string { return fmt.Sprintf("ua-batch-%04d", i) }
	batchStamp := func(i int) time.Time {
		return time.Date(2024, time.June, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute)
	}
	const chunk = 200
	for start := 0; start < blockedCount; start += chunk {
		end := min(start+chunk, blockedCount)
		var issueRows, depRows []string
		var issueArgs, depArgs []any
		for i := start; i < end; i++ {
			issueRows = append(issueRows, "(?, 'fixture', '', '', '', '', 'open', 'task', ?, ?, ?)")
			issueArgs = append(issueArgs, batchID(i), batchStamp(i), batchStamp(i), i%3 != 0)
			depRows = append(depRows, "(UUID(), ?, ?, 'blocks', 'fixture')")
			depArgs = append(depArgs, batchID(i), blocker.id)
		}
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, issue_type, created_at, updated_at, is_blocked) VALUES "+
				strings.Join(issueRows, ", "), issueArgs...); err != nil {
			t.Fatalf("seed issues %d..%d: %v", start, end-1, err)
		}
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES "+
				strings.Join(depRows, ", "), depArgs...); err != nil {
			t.Fatalf("seed dependencies %d..%d: %v", start, end-1, err)
		}
	}
	commitFixture(t, ctx, conn, "test: v46 issues past one restore batch")

	migrateUpStoppingAfter(t, ctx, conn, 47)

	for _, table := range []string{"issues", "issues AS OF 'HEAD'"} {
		rows, err := conn.QueryContext(ctx, "SELECT id, updated_at, is_blocked FROM "+table+" WHERE id LIKE 'ua-batch-%' ORDER BY id")
		if err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		seen, stamped := 0, 0
		for rows.Next() {
			var id string
			var got time.Time
			var blocked bool
			if err := rows.Scan(&id, &got, &blocked); err != nil {
				t.Fatalf("scan %s: %v", table, err)
			}
			want, wantBlocked := blocker.stamp, false
			if id != blocker.id {
				var i int
				if _, err := fmt.Sscanf(id, "ua-batch-%04d", &i); err != nil {
					t.Fatalf("parse fixture id %q: %v", id, err)
				}
				want, wantBlocked = batchStamp(i), true
			}
			if !got.Equal(want) {
				if stamped++; stamped <= 3 {
					t.Errorf("%s %s: updated_at = %s, want its historical %s (the migration overwrote it)",
						table, id, got.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
				}
			}
			if blocked != wantBlocked {
				t.Errorf("%s %s: is_blocked = %v, want %v", table, id, blocked, wantBlocked)
			}
			seen++
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		_ = rows.Close()
		if seen != blockedCount+1 || stamped != 0 {
			t.Errorf("%s: %d fixture rows read (want %d), %d with a migration-time updated_at (want 0)", table, seen, blockedCount+1, stamped)
		}
	}
}

// TestEmbeddedMigrateGuardKeepsAnotherSessionsUpdatedAt pins that the guard
// undoes only the frozen body's own rewrites. While main 0047 runs, a second
// session edits an issue the recompute leaves alone, the way an older bd
// sharing the server could: that edit keeps its own updated_at, and the
// issues the body rewrote still get their historical values back.
func TestEmbeddedMigrateGuardKeepsAnotherSessionsUpdatedAt(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 46)

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("migrating session: %v", err)
	}
	defer conn.Close()
	writer, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("writing session: %v", err)
	}
	defer writer.Close()

	insertStampedIssues(t, ctx, conn, "issues", blockingGraph, true)
	insertIssueEdges(t, ctx, conn, blockingEdges)
	commitFixture(t, ctx, conn, "test: v46 issues with historical updated_at")

	edited := blockingGraph[len(blockingGraph)-1] // ua-free: never blocked
	editedAt := time.Date(2025, time.September, 9, 9, 9, 9, 0, time.UTC)
	edits := 0
	restore := schema.SetUpdatedAtSnapshotHookForTest(func(ctx context.Context, _ schema.DBConn, version int) error {
		if version != 47 {
			return nil
		}
		edits++
		_, err := writer.ExecContext(ctx, "UPDATE issues SET title = 'edited during 0047', updated_at = ? WHERE id = ?", editedAt, edited.id)
		return err
	})
	defer restore()
	migrateUpStoppingAfter(t, ctx, conn, 47)
	if edits != 1 {
		t.Fatalf("the second session edited %d time(s) during 0047, want 1", edits)
	}

	if got := readUpdatedAt(t, ctx, conn, "issues", edited.id); !got.Equal(editedAt) {
		t.Errorf("issues %s: updated_at = %s, want the second session's %s (the guard reverted another writer's edit)",
			edited.id, got.UTC().Format(time.RFC3339Nano), editedAt.Format(time.RFC3339Nano))
	}
	for _, r := range blockingGraph {
		if r.id != edited.id {
			requireStamp(t, ctx, conn, "issues", r.id, r.stamp)
		}
	}
}

// TestEmbeddedMigrateGuardKeepsUpdatedAtAutoStampDefinitions is AC-8's
// runtime half: after a full upgrade through every guarded step, each
// auto-stamped table's updated_at still declares ON UPDATE CURRENT_TIMESTAMP,
// and the clause still fires on an ordinary write. The restore's explicit
// assignments suppress it per statement only. (The migration-freeze tests
// pin the SQL hashes; the CLI parity oracle compares the main tables'
// definitions but excludes wisps.)
func TestEmbeddedMigrateGuardKeepsUpdatedAtAutoStampDefinitions(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 45)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	insertStampedIssues(t, ctx, conn, "issues", blockingGraph, false)
	insertIssueEdges(t, ctx, conn, blockingEdges)
	commitFixture(t, ctx, conn, "test: v45 issues with historical updated_at")

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	for _, table := range []string{"issues", "wisps", "routes", "federation_peers"} {
		var colType, nullable, extra string
		var colDefault sql.NullString
		err := conn.QueryRowContext(ctx, `SELECT COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, EXTRA
			FROM INFORMATION_SCHEMA.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'updated_at'`, table).
			Scan(&colType, &nullable, &colDefault, &extra)
		if err != nil {
			t.Fatalf("reading %s.updated_at definition: %v", table, err)
		}
		if !strings.Contains(strings.ToLower(extra), "on update current_timestamp") {
			t.Errorf("%s.updated_at EXTRA = %q, want ON UPDATE CURRENT_TIMESTAMP", table, extra)
		}
		if nullable != "NO" || !strings.Contains(strings.ToLower(colDefault.String), "current_timestamp") {
			t.Errorf("%s.updated_at = %s nullable=%s default=%v, want NOT NULL DEFAULT CURRENT_TIMESTAMP", table, colType, nullable, colDefault)
		}
	}

	free := blockingGraph[len(blockingGraph)-1]
	if _, err := conn.ExecContext(ctx, "UPDATE issues SET title = 'retitled' WHERE id = ?", free.id); err != nil {
		t.Fatalf("retitling %s: %v", free.id, err)
	}
	if got := readUpdatedAt(t, ctx, conn, "issues", free.id); got.Equal(free.stamp) {
		t.Errorf("issues %s: updated_at stayed %v after an ordinary write; ON UPDATE CURRENT_TIMESTAMP no longer fires", free.id, got)
	}
}
