//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// updated_at preservation across value-rewriting migrations (gastownhall/beads
// #5981, work item bd-chqckf).
//
// issues.updated_at and wisps.updated_at carry ON UPDATE CURRENT_TIMESTAMP
// (0001_create_issues, 0020_create_wisps). Migrations 0046/0047 recompute
// issues.is_blocked and 0035/0053 flip `ephemeral` while moving rows between
// the tables (ignored/0007 does the wisp-plane recompute) — none of them
// assigns updated_at, so on a pre-1.2 → 1.2.x upgrade every row the migration
// actually changes is silently re-stamped with the migration's wall clock,
// destroying real last-modified dates (56 of 175 issues in the report). The
// shipped SQL is frozen (migration_freeze_test.go pins its hashes), so the
// fix snapshots (id, updated_at) before such a step and restores the
// historical timestamps after its body, before the cursor row and the step
// commit record it.
//
// These tests exercise real migrations on the real embedded engine, comparing
// timestamps by SQL equality (COUNT(*) WHERE id = ? AND updated_at = ?) rather
// than Go-side parsing: the round trip through the driver's DATETIME
// representation stays out of the assertion entirely.

// updatedGuardExec runs one statement, failing the test on error.
func updatedGuardExec(t *testing.T, ctx context.Context, conn *sql.Conn, query string, args ...any) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// updatedGuardRowKeepsTimestamp asserts row id of table still carries exactly
// the `want` updated_at literal it was seeded with. On mismatch it reports the
// value it actually holds, read back as CHAR so the failure message shows what
// the migration stamped over the history.
func updatedGuardRowKeepsTimestamp(t *testing.T, ctx context.Context, conn *sql.Conn, table, id, want string) {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+table+" WHERE id = ? AND updated_at = ?", id, want).Scan(&n); err != nil {
		t.Fatalf("read %s.%s updated_at: %v", table, id, err)
	}
	if n == 1 {
		return
	}
	var got sql.NullString
	_ = conn.QueryRowContext(ctx,
		"SELECT CAST(updated_at AS CHAR) FROM "+table+" WHERE id = ?", id).Scan(&got)
	t.Errorf("%s.%s updated_at = %q after migration, want %q (historical last-modified date overwritten)",
		table, id, got.String, want)
}

// updatedGuardRowIsBlocked asserts the row's derived is_blocked value.
func updatedGuardRowIsBlocked(t *testing.T, ctx context.Context, conn *sql.Conn, table, id string, want bool) {
	t.Helper()
	var b int
	if err := conn.QueryRowContext(ctx,
		"SELECT is_blocked FROM "+table+" WHERE id = ?", id).Scan(&b); err != nil {
		t.Fatalf("read %s.%s is_blocked: %v", table, id, err)
	}
	if (b != 0) != want {
		t.Errorf("%s.%s is_blocked = %d, want %v (recompute must still run)", table, id, b, want)
	}
}

// updatedGuardSeedIssue inserts one issue with an explicit historical
// updated_at, the pre-upgrade shape every test here starts from. Every NOT
// NULL column is listed explicitly: the embedded engine does not honor the
// TEXT DEFAULT '' / JSON DEFAULT (JSON_OBJECT()) clauses the bootstrap DDL
// carries, so relying on them fails with "Field ... doesn't have a default
// value".
func updatedGuardSeedIssue(t *testing.T, ctx context.Context, conn *sql.Conn, id, status, issueType, updated string, ephemeral int) {
	t.Helper()
	updatedGuardExec(t, ctx, conn,
		`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes,
		                   status, priority, issue_type, metadata, ephemeral, created_at, updated_at)
		 VALUES (?, ?, '', '', '', '', ?, 2, ?, '{}', ?, '2025-12-01 08:00:00', ?)`,
		id, "seed "+id, status, issueType, ephemeral, updated)
}

// updatedGuardCommitFixture commits everything currently stageable in the
// working set, the state MigrateUp's pre-migration dirty-table guard requires.
// wisps and every wisp_* table match dolt_ignore patterns, so a fixture that
// seeded only wisp-plane rows has nothing stageable; "nothing to commit" is
// then the already-clean state the commit exists to guarantee.
func updatedGuardCommitFixture(t *testing.T, ctx context.Context, conn *sql.Conn, message string) {
	t.Helper()
	updatedGuardExec(t, ctx, conn, "CALL DOLT_ADD('-A')")
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?)", message); err != nil {
		if !strings.Contains(err.Error(), "nothing to commit") {
			t.Fatalf("CALL DOLT_COMMIT(%q): %v", message, err)
		}
	}
}

// TestEmbeddedMigrate0046And0047KeepIssueUpdatedAt is the primary regression
// for #5981: a genuine v45 store (the last version before the is_blocked
// projection) with distinct historical updated_at values and both an open and
// a closed blocker upgrades across 0046 and 0047. Every existing issue must
// retain its timestamp, and is_blocked must still reflect the dependency
// graph — the migration's job does not disappear, only its collateral damage.
func TestEmbeddedMigrate0046And0047KeepIssueUpdatedAt(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	const (
		blocker      = "gh5981-blocker"
		blocked      = "gh5981-blocked"
		closedBlock  = "gh5981-closed-blocker"
		closedBlkd   = "gh5981-closed-blocked"
		free         = "gh5981-free"
		blockerTS    = "2026-01-10 09:00:00"
		blockedTS    = "2026-01-15 10:00:00"
		closedBlockT = "2026-01-20 11:00:00"
		closedBlkdTS = "2026-01-25 12:00:00"
		freeTS       = "2026-02-01 13:00:00"
	)

	dataDir := seedMainSchemaAt(t, ctx, 45)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	updatedGuardSeedIssue(t, ctx, conn, blocker, "open", "task", blockerTS, 0)
	updatedGuardSeedIssue(t, ctx, conn, blocked, "open", "task", blockedTS, 0)
	updatedGuardSeedIssue(t, ctx, conn, closedBlock, "closed", "task", closedBlockT, 0)
	updatedGuardSeedIssue(t, ctx, conn, closedBlkd, "open", "task", closedBlkdTS, 0)
	updatedGuardSeedIssue(t, ctx, conn, free, "open", "task", freeTS, 0)
	updatedGuardExec(t, ctx, conn,
		`INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by, metadata, created_at)
		 VALUES ('gh5981-dep-open', ?, ?, 'blocks', '', '{}', '2026-01-05 08:00:00')`, blocked, blocker)
	updatedGuardExec(t, ctx, conn,
		`INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by, metadata, created_at)
		 VALUES ('gh5981-dep-closed', ?, ?, 'blocks', '', '{}', '2026-01-05 08:00:00')`, closedBlkd, closedBlock)
	updatedGuardCommitFixture(t, ctx, conn, "test: seed v45 fixture for gh 5981")

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp from v45: %v", err)
	}
	if v := currentMainVersion(t, ctx, conn); v != schema.LatestVersion() {
		t.Fatalf("schema version after MigrateUp = %d, want latest %d", v, schema.LatestVersion())
	}

	// The recompute itself must still work: blocked-by-open is blocked,
	// blocked-by-closed and everything else are not.
	updatedGuardRowIsBlocked(t, ctx, conn, "issues", blocked, true)
	updatedGuardRowIsBlocked(t, ctx, conn, "issues", closedBlkd, false)
	updatedGuardRowIsBlocked(t, ctx, conn, "issues", blocker, false)
	updatedGuardRowIsBlocked(t, ctx, conn, "issues", free, false)

	// And none of the pre-existing rows paid for it with its timestamp.
	updatedGuardRowKeepsTimestamp(t, ctx, conn, "issues", blocker, blockerTS)
	updatedGuardRowKeepsTimestamp(t, ctx, conn, "issues", blocked, blockedTS)
	updatedGuardRowKeepsTimestamp(t, ctx, conn, "issues", closedBlock, closedBlockT)
	updatedGuardRowKeepsTimestamp(t, ctx, conn, "issues", closedBlkd, closedBlkdTS)
	updatedGuardRowKeepsTimestamp(t, ctx, conn, "issues", free, freeTS)
}
