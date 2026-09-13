//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

const migration0059FrozenHash = "f0186029a1c87d579b67e0540ec22ad0bd0fc59793b07f7d24a24e122b77c394"

// TestEmbeddedMigration0059IndexedExecutionMatchesFrozenSemantics is a real
// Dolt differential: equivalent version-58 stores execute the byte-exact
// frozen body and the runtime-adapted body, then every issue's derived blocked
// state is compared. The fixture covers committed dependencies, clone-local
// wisp dependencies, the NULL gate defect 0059 repairs, and nearby clear rows.
func TestEmbeddedMigration0059IndexedExecutionMatchesFrozenSemantics(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	controlDir := seedMainSchemaAt(t, ctx, 58)
	control, closeControl := openPinnedConn(t, ctx, controlDir)
	seedMigration0059Graph(t, ctx, control)
	frozen, err := schema.MigrationSQL("0059_recompute_null_gate_is_blocked.up.sql")
	if err != nil {
		t.Fatalf("MigrationSQL(0059): %v", err)
	}
	if err := schema.DrainCall(ctx, control, frozen); err != nil {
		t.Fatalf("execute frozen migration 0059: %v", err)
	}
	want := migration0059Outcomes(t, ctx, control)
	assertMigration0059TemporaryTablesGone(t, ctx, control)
	closeControl()

	candidateDir := seedMainSchemaAt(t, ctx, 58)
	candidate, closeCandidate := openPinnedConn(t, ctx, candidateDir)
	defer closeCandidate()
	seedMigration0059Graph(t, ctx, candidate)
	if _, err := schema.MigrateUpTo(ctx, candidate, 59); err != nil {
		t.Fatalf("MigrateUpTo(59): %v", err)
	}
	got := migration0059Outcomes(t, ctx, candidate)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("adapted migration outcomes = %#v, want frozen outcomes %#v", got, want)
	}
	if got["m59-waiter"] != 1 || got["m59-wisp-waiter"] != 1 || got["m59-free"] != 0 {
		t.Fatalf("representative outcomes = %#v, want both waiters blocked and free issue clear", got)
	}
	for _, id := range []string{
		"m59-root-issue", "m59-child-from-issue", "m59-child-from-wisp",
		"m59-wisp-child-target-issue", "m59-all-waiter", "m59-grandchild",
		"m59-cycle-a", "m59-cycle-b",
	} {
		if got[id] != 1 {
			t.Errorf("%s is_blocked = %d, want 1", id, got[id])
		}
	}
	if got["m59-any-waiter"] != 0 {
		t.Errorf("m59-any-waiter is_blocked = %d, want 0 after one child closed", got["m59-any-waiter"])
	}
	if hash := scalarString(t, ctx, candidate, "SELECT content_hash FROM schema_migrations WHERE version = 59"); hash != migration0059FrozenHash {
		t.Fatalf("migration 59 content_hash = %q, want frozen migration hash", hash)
	}
	assertMigration0059TemporaryTablesGone(t, ctx, candidate)
}

// TestEmbeddedMigration0059ProductionRetryConverges proves the production
// MigrateUp path commits 0059 atomically before a simulated interruption and a
// plain retry completes both the main and ignored migration chains. This is the
// crash boundary relevant to an operator restarting a failed schema upgrade.
func TestEmbeddedMigration0059ProductionRetryConverges(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 58)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	seedMigration0059Graph(t, ctx, conn)

	injected := errors.New("injected interruption after migration 0059")
	restore := schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, version int) error {
		if version == 59 {
			return injected
		}
		return nil
	})
	if _, err := schema.MigrateUp(ctx, conn); !errors.Is(err, injected) {
		restore()
		closeConn()
		t.Fatalf("first MigrateUp error = %v, want injected interruption", err)
	}
	restore()
	closeConn()

	retry, closeRetry := openPinnedConn(t, ctx, dataDir)
	defer closeRetry()
	if got := scalarInt(t, ctx, retry, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != 59 {
		t.Fatalf("cursor after interruption = %d, want committed migration 59", got)
	}
	assertMigration0059TemporaryTablesGone(t, ctx, retry)

	if _, err := schema.MigrateUp(ctx, retry); err != nil {
		t.Fatalf("plain MigrateUp retry: %v", err)
	}
	if got := scalarInt(t, ctx, retry, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != schema.LatestVersion() {
		t.Fatalf("main cursor after retry = %d, want %d", got, schema.LatestVersion())
	}
	if got := scalarInt(t, ctx, retry, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations"); got != schema.LatestIgnoredVersion() {
		t.Fatalf("ignored cursor after retry = %d, want %d", got, schema.LatestIgnoredVersion())
	}
	outcomes := migration0059Outcomes(t, ctx, retry)
	if outcomes["m59-waiter"] != 1 || outcomes["m59-wisp-waiter"] != 1 || outcomes["m59-free"] != 0 {
		t.Fatalf("post-retry outcomes = %#v", outcomes)
	}
	wispOutcomes := migration0059WispOutcomes(t, ctx, retry)
	for _, id := range []string{"m59-root-wisp", "m59-wisp-child-from-issue", "m59-wisp-child-from-wisp"} {
		if wispOutcomes[id] != 1 {
			t.Errorf("post-ignored-retry %s is_blocked = %d, want 1", id, wispOutcomes[id])
		}
	}
	assertMigration0059TemporaryTablesGone(t, ctx, retry)
}

// TestEmbeddedMigration0059MidStepFailureRollsBackAndRetries exercises the
// actual executor failure window, before the cursor row and Dolt migration
// commit exist. The custom execution transaction must remove ordinary helper
// tables and leave the versioned graph clean enough for an unassisted retry.
func TestEmbeddedMigration0059MidStepFailureRollsBackAndRetries(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 58)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	seedMigration0059Graph(t, ctx, conn)

	injected := errors.New("injected migration 0059 failure after apply")
	restore := schema.SetMigration0059FaultHookForTest(func(stage string) error {
		if stage == "after_apply" {
			return injected
		}
		return nil
	})
	if _, err := schema.MigrateUp(ctx, conn); !errors.Is(err, injected) {
		restore()
		closeConn()
		t.Fatalf("first MigrateUp error = %v, want injected mid-step failure", err)
	}
	restore()
	closeConn()

	retry, closeRetry := openPinnedConn(t, ctx, dataDir)
	defer closeRetry()
	if got := scalarInt(t, ctx, retry, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != 58 {
		t.Fatalf("cursor after rolled-back mid-step failure = %d, want 58", got)
	}
	assertMigration0059TemporaryTablesGone(t, ctx, retry)
	if _, err := schema.MigrateUp(ctx, retry); err != nil {
		t.Fatalf("plain MigrateUp retry after mid-step failure: %v", err)
	}
	if got := scalarInt(t, ctx, retry, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != schema.LatestVersion() {
		t.Fatalf("main cursor after retry = %d, want %d", got, schema.LatestVersion())
	}
}

func seedMigration0059Graph(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	for _, id := range []string{
		"m59-parent", "m59-child", "m59-waiter", "m59-wisp-waiter", "m59-free",
		"m59-target-issue", "m59-root-issue", "m59-child-from-issue", "m59-child-from-wisp",
		"m59-wisp-child-target-issue", "m59-any-parent", "m59-any-open", "m59-any-closed",
		"m59-any-waiter", "m59-all-waiter", "m59-grandchild", "m59-cycle-a", "m59-cycle-b",
	} {
		seedIssue(t, ctx, conn, id)
	}
	for _, id := range []string{
		"m59-wisp-parent", "m59-wisp-child", "m59-target-wisp", "m59-root-wisp",
		"m59-wisp-child-from-issue", "m59-wisp-child-from-wisp",
	} {
		seedWisp(t, ctx, conn, id)
	}
	mustExecConn(t, ctx, conn, `
INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata)
VALUES
  ('00000000-0000-0000-0000-000000000591', 'm59-child', 'm59-parent', 'parent-child', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000592', 'm59-waiter', 'm59-parent', 'waits-for', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000595', 'm59-root-issue', 'm59-target-issue', 'blocks', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000596', 'm59-child-from-issue', 'm59-root-issue', 'parent-child', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000597', 'm59-wisp-child-target-issue', 'm59-target-issue', 'conditional-blocks', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000599', 'm59-any-open', 'm59-any-parent', 'parent-child', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000600', 'm59-any-closed', 'm59-any-parent', 'parent-child', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000601', 'm59-any-waiter', 'm59-any-parent', 'waits-for', NOW(), 'tester', JSON_OBJECT('gate', 'any-children')),
	('00000000-0000-0000-0000-000000000602', 'm59-all-waiter', 'm59-any-parent', 'waits-for', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000607', 'm59-grandchild', 'm59-child-from-wisp', 'parent-child', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000608', 'm59-cycle-a', 'm59-target-issue', 'blocks', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000609', 'm59-cycle-b', 'm59-cycle-a', 'parent-child', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000610', 'm59-cycle-a', 'm59-cycle-b', 'parent-child', NOW(), 'tester', JSON_OBJECT())`)
	mustExecConn(t, ctx, conn, `
INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by, metadata)
VALUES
	('00000000-0000-0000-0000-000000000593', 'm59-wisp-child', 'm59-wisp-parent', 'parent-child', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000603', 'm59-root-wisp', 'm59-target-wisp', 'blocks', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000604', 'm59-wisp-child-from-wisp', 'm59-root-wisp', 'parent-child', NOW(), 'tester', JSON_OBJECT())`)
	mustExecConn(t, ctx, conn, `
INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata)
VALUES
	('00000000-0000-0000-0000-000000000605', 'm59-wisp-child-from-issue', 'm59-root-issue', 'parent-child', NOW(), 'tester', JSON_OBJECT())`)
	mustExecConn(t, ctx, conn, `
INSERT INTO dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by, metadata)
VALUES
	('00000000-0000-0000-0000-000000000594', 'm59-wisp-waiter', 'm59-wisp-parent', 'waits-for', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000598', 'm59-child-from-wisp', 'm59-root-wisp', 'parent-child', NOW(), 'tester', JSON_OBJECT()),
	('00000000-0000-0000-0000-000000000606', 'm59-wisp-child-target-issue', 'm59-target-wisp', 'blocks', NOW(), 'tester', JSON_OBJECT())`)
	mustExecConn(t, ctx, conn, "UPDATE issues SET status = 'closed' WHERE id = 'm59-any-closed'")
	mustExecConn(t, ctx, conn, "UPDATE issues SET is_blocked = 0, updated_at = updated_at WHERE id LIKE 'm59-%'")
	// TINYINT(1) accepts noncanonical integers. The frozen migration first
	// resets every row to literal zero, so the adapter must normalize a blocked
	// row from 2 to exactly 1 rather than treating it as an existing true value.
	mustExecConn(t, ctx, conn, "UPDATE issues SET is_blocked = 2, updated_at = updated_at WHERE id = 'm59-root-issue'")
	commitSeed(t, ctx, conn, "test: seed migration 0059 null-gate graph")
}

func migration0059Outcomes(t *testing.T, ctx context.Context, conn *sql.Conn) map[string]int {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT id, is_blocked FROM issues WHERE id LIKE 'm59-%' ORDER BY id")
	if err != nil {
		t.Fatalf("query migration 0059 outcomes: %v", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var id string
		var blocked int
		if err := rows.Scan(&id, &blocked); err != nil {
			t.Fatalf("scan migration 0059 outcome: %v", err)
		}
		out[id] = blocked
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migration 0059 outcomes: %v", err)
	}
	return out
}

func migration0059WispOutcomes(t *testing.T, ctx context.Context, conn *sql.Conn) map[string]int {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT id, is_blocked FROM wisps WHERE id LIKE 'm59-%' ORDER BY id")
	if err != nil {
		t.Fatalf("query migration 0059 wisp outcomes: %v", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var id string
		var blocked int
		if err := rows.Scan(&id, &blocked); err != nil {
			t.Fatalf("scan migration 0059 wisp outcome: %v", err)
		}
		out[id] = blocked
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migration 0059 wisp outcomes: %v", err)
	}
	return out
}

func assertMigration0059TemporaryTablesGone(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	for _, table := range []string{"__bd_0059_recompute_wisps", "__bd_0059_recompute_wisp_deps", "__bd_0059_blocked_nodes"} {
		var got int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&got); err != nil {
			t.Fatalf("inspect temporary table %s: %v", table, err)
		}
		if got != 0 {
			t.Errorf("temporary table %s remains after migration (%d rows in INFORMATION_SCHEMA)", table, got)
		}
	}
}
