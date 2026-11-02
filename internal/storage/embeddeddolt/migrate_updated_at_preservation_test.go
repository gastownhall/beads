//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

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
	})
}
