package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// Issue #6092: a shared-server store upgrading bd 1.0.0 → 1.2.2 bricks in the
// ignored migration chain over a clone-local legacy wisp_dependencies table.
//
// ignored/0005 guards its entire frozen body on one upfront flag — @needs_drop,
// read once from depends_on_id's presence — and, while that flag is 1, re-adds
// idx_wisp_dep_type_issue and its two siblings. Main 0022 creates exactly
// those three indexes over a split-target table (its own guard is
// @has_split_targets), so a legacy generated-column table that already carries
// them collides: "Duplicate key name 'idx_wisp_dep_type_issue'". A partially
// applied 0005 (the drop half ran, the add-index half failed) leaves
// depends_on_id gone, so a retry reads @needs_drop = 0, no-ops the whole body,
// advances the cursor, and strands the table without its target foreign keys
// and without ck_wisp_dep_one_target, which the file never adds.
//
// A clone whose main cursor is already past 58 — main at latest, ignored still
// at 4 — has no pending main migration to host the existing forward-shape
// repair ({"schema_migrations", 58}), so nothing heals the table before 0005
// runs. The fix registers repairWispDependenciesForwardShape at
// {"ignored_schema_migrations", 5} as well: it marches the table to the final
// shape immediately before the frozen body, which selects 0005's no-op branch
// and restores the constraints a partial run dropped.
//
// These tests run the real migration pass (schema.MigrateUpWithLock on a
// pinned session) against a live dolt sql-server, the reporter's mode. Each
// test owns its database — a single-branch database where the fixture, the
// pass and every assertion share one pinned connection, so no fresh pooled
// session can land on a different branch's working set and mask the outcome.
// PRE-fix the primary regression dies at ignored/0005 with the duplicate key
// name, and the partial-shape regression silently converges the cursor while
// leaving the constraints missing.

// issue6092LegacyDelegateWispDepDDL is the legacy generated-column shape with
// the split target columns already present: depends_on_id still a STORED
// COALESCE over them, the composite primary key, and neither target foreign
// key. Ported from internal/storage/embeddeddolt's
// migrate_wisp_dep_forward_repair_test.go (legacyDelegateWispDepDDL), which
// took it verbatim from a production SHOW CREATE TABLE. The source foreign
// key fk_wisp_dep_issue is added separately by the fixture, matching the
// 0021-lineage tables the reporter upgraded.
const issue6092LegacyDelegateWispDepDDL = `
CREATE TABLE wisp_dependencies (
    issue_id VARCHAR(255) NOT NULL,
    type VARCHAR(32) NOT NULL DEFAULT 'blocks',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255) DEFAULT '',
    metadata JSON DEFAULT (JSON_OBJECT()),
    thread_id VARCHAR(255) DEFAULT '',
    depends_on_issue_id VARCHAR(255) NULL,
    depends_on_wisp_id VARCHAR(255) NULL,
    depends_on_external VARCHAR(255) NULL,
    depends_on_id VARCHAR(255) NOT NULL GENERATED ALWAYS AS (COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)) STORED,
    PRIMARY KEY (issue_id, depends_on_id),
    KEY idx_wisp_dep_external_target (depends_on_external),
    KEY idx_wisp_dep_issue_target (depends_on_issue_id),
    KEY idx_wisp_dep_type (type),
    KEY idx_wisp_dep_type_target (type, depends_on_id),
    KEY idx_wisp_dep_wisp_target (depends_on_wisp_id)
)`

// issue6092Conn is the subset of *sql.Conn (and *sql.DB) the helpers below
// need, so the fixture helpers serve both a pinned session and a pool.
type issue6092Conn interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// issue6092Setup opens a private single-branch database on the test dolt
// sql-server, brings its whole schema to this binary's latest, and returns the
// pinned connection everything else runs on. No DROP DATABASE on cleanup:
// rapid CREATE/DROP cycles crash the Dolt server (see events_preflip_restore_
// test.go).
func issue6092Setup(t *testing.T) (string, *sql.Conn, context.Context) {
	t.Helper()
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)

	dbName := uniqueTestDBName(t)
	admin, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { admin.Close() })
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	db, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName, Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	if _, err := schema.MigrateUpWithLock(ctx, conn, dbName); err != nil {
		t.Fatalf("initial migration pass: %v", err)
	}
	return dbName, conn, ctx
}

// TestIssue6092IgnoredWispDepDropOverPreexistingTypeIndexes is the primary
// regression: main recorded through latest (0022 ran over a split-target
// table), ignored still at 4, and the legacy generated-column
// wisp_dependencies carrying main 0022's three type indexes. The upgrade
// pass must succeed, both cursors must reach latest, and the table must end
// in the final shape — without the duplicate-index abort at ignored/0005 —
// with every seeded dependency row preserved under its own surrogate id and
// the referenced wisp/issue rows untouched.
func TestIssue6092IgnoredWispDepDropOverPreexistingTypeIndexes(t *testing.T) {
	dbName, conn, ctx := issue6092Setup(t)

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("exec %.80q: %v", query, err)
		}
	}
	scalar := func(query string) int64 {
		t.Helper()
		var v int64
		if err := conn.QueryRowContext(ctx, query).Scan(&v); err != nil {
			t.Fatalf("query %.80q: %v", query, err)
		}
		return v
	}

	// A clone whose main sequence ran on a newer binary than its ignored
	// sequence: main at latest, ignored cursor rewound to 4.
	exec("DELETE FROM ignored_schema_migrations WHERE version > 4")

	exec("DROP TABLE wisp_dependencies")
	exec(issue6092LegacyDelegateWispDepDDL)
	exec("ALTER TABLE wisp_dependencies ADD CONSTRAINT fk_wisp_dep_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE")
	// Main 0022 over a split-target table is what created the reporter's
	// colliding indexes; run the frozen file, not a hand-made copy, so the
	// fixture carries the real objects (and their real names).
	issue6092RunMigrationFile(t, ctx, conn, "../schema/migrations/0022_wisp_dep_type_index.up.sql")

	issue6092SeedReferencedRows(t, ctx, conn)
	issue6092SeedValidWispDeps(t, ctx, conn)
	updatedBefore := issue6092SnapshotUpdatedAt(t, ctx, conn)
	issue6092CommitFixture(t, ctx, conn, "test: #6092 legacy wisp_dependencies with 0022's type indexes")

	if got := scalar("SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations"); got != 4 {
		t.Fatalf("fixture ignored cursor = %d, want 4", got)
	}
	var branches int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_branches").Scan(&branches); err != nil {
		t.Fatalf("count branches: %v", err)
	}
	if branches != 1 {
		t.Fatalf("fixture database has %d branches, want 1", branches)
	}

	// PRE-fix this dies at ignored/0005 with
	// "Error 1105 (HY000): Duplicate key name 'idx_wisp_dep_type_issue'".
	if _, err := schema.MigrateUpWithLock(ctx, conn, dbName); err != nil {
		t.Fatalf("upgrade over a legacy wisp_dependencies carrying main 0022's type indexes (#6092): %v", err)
	}

	if got := scalar("SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != int64(schema.LatestVersion()) {
		t.Fatalf("main cursor = %d after the upgrade, want latest %d", got, schema.LatestVersion())
	}
	if got := scalar("SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations"); got != int64(schema.LatestIgnoredVersion()) {
		t.Fatalf("ignored cursor = %d after the upgrade, want latest %d", got, schema.LatestIgnoredVersion())
	}
	issue6092RequireFinalWispDepShape(t, ctx, conn)
	issue6092RequireSeededRowsPreserved(t, ctx, conn, updatedBefore)
}

// issue6092SnapshotUpdatedAt captures the updated_at of every referenced wisp
// and issue row (both columns are ON UPDATE CURRENT_TIMESTAMP, so any rewrite
// of a referenced row moves its value) for the post-upgrade comparison.
func issue6092SnapshotUpdatedAt(t *testing.T, ctx context.Context, db issue6092Conn) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	for _, ref := range []struct{ table, id string }{
		{"wisps", "w1"}, {"wisps", "w2"}, {"wisps", "w3"}, {"issues", "i1"},
	} {
		var ts string
		if err := db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT updated_at FROM %s WHERE id = ?", ref.table), ref.id).Scan(&ts); err != nil {
			t.Fatalf("snapshot %s.%s updated_at: %v", ref.table, ref.id, err)
		}
		snapshot[ref.table+"/"+ref.id] = ts
	}
	return snapshot
}

// issue6092RequireSeededRowsPreserved asserts every seeded dependency row
// survived the upgrade with its targets, type and created_by intact under a
// distinct nonempty id, and that the referenced wisp and issue rows still
// carry the updated_at captured before it.
func issue6092RequireSeededRowsPreserved(t *testing.T, ctx context.Context, db issue6092Conn, before map[string]string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
SELECT id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_by
FROM wisp_dependencies`)
	if err != nil {
		t.Fatalf("read wisp_dependencies: %v", err)
	}
	var got []issue6092DepRow
	for rows.Next() {
		var r issue6092DepRow
		if err := rows.Scan(&r.id, &r.issue, &r.issueTarget, &r.wispTarget, &r.ext, &r.depType, &r.createdBy); err != nil {
			rows.Close()
			t.Fatalf("scan wisp dep: %v", err)
		}
		got = append(got, r)
	}
	_ = rows.Close()

	want := []struct{ issue, target, depType, createdBy string }{
		{"w1", "w2", "blocks", "alice"},
		{"w3", "i1", "relates", "bob"},
		{"w1", "external:e1", "mentions", "carol"},
	}
	if len(got) != len(want) {
		t.Fatalf("wisp_dependencies has %d rows after the upgrade, want %d", len(got), len(want))
	}

	ids := map[string]bool{}
	for _, r := range got {
		if r.id == "" || len(r.id) != 36 {
			t.Errorf("row %s->%s has id %q, want a distinct nonempty 36-char id", r.issue, r.target(), r.id)
		}
		if ids[r.id] {
			t.Errorf("duplicate id %s across preserved rows", r.id)
		}
		ids[r.id] = true

		matched := false
		for _, w := range want {
			if r.issue == w.issue && r.target() == w.target {
				matched = true
				if r.depType != w.depType {
					t.Errorf("row %s->%s type = %q, want %q", r.issue, w.target, r.depType, w.depType)
				}
				if r.createdBy != w.createdBy {
					t.Errorf("row %s->%s created_by = %q, want %q", r.issue, w.target, r.createdBy, w.createdBy)
				}
				break
			}
		}
		if !matched {
			t.Errorf("unexpected dep row %s->%s (%s/%s) survived", r.issue, r.target(), r.depType, r.createdBy)
		}
	}

	for _, ref := range []struct{ table, id string }{
		{"wisps", "w1"}, {"wisps", "w2"}, {"wisps", "w3"}, {"issues", "i1"},
	} {
		var ts string
		if err := db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT updated_at FROM %s WHERE id = ?", ref.table), ref.id).Scan(&ts); err != nil {
			t.Fatalf("re-read %s.%s updated_at: %v", ref.table, ref.id, err)
		}
		if ts != before[ref.table+"/"+ref.id] {
			t.Errorf("%s.%s updated_at changed across the upgrade: %q -> %q", ref.table, ref.id, before[ref.table+"/"+ref.id], ts)
		}
	}
}

// issue6092DepRow is one wisp_dependencies row as the preservation check
// reads it.
type issue6092DepRow struct {
	id, issue, createdBy, depType string
	issueTarget, wispTarget, ext  sql.NullString
}

// target renders whichever single target column a preserved row carries.
func (r issue6092DepRow) target() string {
	switch {
	case r.issueTarget.Valid:
		return r.issueTarget.String
	case r.wispTarget.Valid:
		return r.wispTarget.String
	case r.ext.Valid:
		return r.ext.String
	}
	return "<no target>"
}

// issue6092Partial0005WispDepDDL is the state a partially applied ignored/0005
// strands: the drop half ran (depends_on_id gone, legacy indexes and foreign
// keys dropped), the id/unique-key/index adds ran, and the failure hit before
// the foreign-key restore — so the retry reads @needs_drop = 0, no-ops the
// whole frozen body and advances the cursor past a table with no foreign keys
// and no one-target check. The legacy plain KEYs survive exactly as they would
// on a real stranded table; 0005 never drops them.
const issue6092Partial0005WispDepDDL = `
CREATE TABLE wisp_dependencies (
    id CHAR(36) NOT NULL DEFAULT (UUID()),
    issue_id VARCHAR(255) NOT NULL,
    depends_on_issue_id VARCHAR(255) NULL,
    depends_on_wisp_id VARCHAR(255) NULL,
    depends_on_external VARCHAR(255) NULL,
    type VARCHAR(32) NOT NULL DEFAULT 'blocks',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255) DEFAULT '',
    metadata JSON DEFAULT (JSON_OBJECT()),
    thread_id VARCHAR(255) DEFAULT '',
    PRIMARY KEY (id),
    UNIQUE KEY uk_wisp_dep_issue_target (issue_id, depends_on_issue_id),
    UNIQUE KEY uk_wisp_dep_wisp_target (issue_id, depends_on_wisp_id),
    UNIQUE KEY uk_wisp_dep_external_target (issue_id, depends_on_external),
    KEY idx_wisp_dep_external_target (depends_on_external),
    KEY idx_wisp_dep_issue_target (depends_on_issue_id),
    KEY idx_wisp_dep_type (type),
    KEY idx_wisp_dep_type_issue (type, depends_on_issue_id),
    KEY idx_wisp_dep_type_wisp (type, depends_on_wisp_id),
    KEY idx_wisp_dep_type_external (type, depends_on_external),
    KEY idx_wisp_dep_wisp_target (depends_on_wisp_id)
)`

// TestIssue6092IgnoredWispDepRepairRestoresConstraintsAfterFailedRun covers the
// stranded population of #6092: a partially applied ignored/0005 left the
// id-keyed table without its target foreign keys and without
// ck_wisp_dep_one_target, and the ignored cursor still at 4. A plain retry
// cannot heal it — the frozen body no-ops on @needs_drop = 0 — so the repair
// registered at ignored 5 must complete the shape in place, keeping the
// existing surrogate ids.
func TestIssue6092IgnoredWispDepRepairRestoresConstraintsAfterFailedRun(t *testing.T) {
	dbName, conn, ctx := issue6092Setup(t)

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("exec %.80q: %v", query, err)
		}
	}
	scalar := func(query string) int64 {
		t.Helper()
		var v int64
		if err := conn.QueryRowContext(ctx, query).Scan(&v); err != nil {
			t.Fatalf("query %.80q: %v", query, err)
		}
		return v
	}

	// The stranded state: cursor still at 4 (0005 never stamped), table
	// already id-keyed with its unique keys but none of the four constraints.
	exec("DELETE FROM ignored_schema_migrations WHERE version > 4")
	exec("DROP TABLE wisp_dependencies")
	exec(issue6092Partial0005WispDepDDL)

	issue6092SeedReferencedRows(t, ctx, conn)
	wantIDs := issue6092SeedWispDepsWithDeterministicIDs(t, ctx, conn)
	updatedBefore := issue6092SnapshotUpdatedAt(t, ctx, conn)
	issue6092CommitFixture(t, ctx, conn, "test: #6092 partial 0005 shape without target constraints")

	if got := scalar("SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations"); got != 4 {
		t.Fatalf("fixture ignored cursor = %d, want 4", got)
	}

	// PRE-fix this pass succeeds and advances the cursor while the frozen
	// body no-ops, leaving every constraint missing — the stranded state the
	// reporter had to recover from by hand.
	if _, err := schema.MigrateUpWithLock(ctx, conn, dbName); err != nil {
		t.Fatalf("upgrade over a partially applied 0005 (#6092): %v", err)
	}

	if got := scalar("SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != int64(schema.LatestVersion()) {
		t.Fatalf("main cursor = %d after the upgrade, want latest %d", got, schema.LatestVersion())
	}
	if got := scalar("SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations"); got != int64(schema.LatestIgnoredVersion()) {
		t.Fatalf("ignored cursor = %d after the upgrade, want latest %d", got, schema.LatestIgnoredVersion())
	}
	issue6092RequireFinalWispDepShape(t, ctx, conn)
	issue6092RequireSeededRowsPreserved(t, ctx, conn, updatedBefore)

	for natural, wantID := range wantIDs {
		if got := issue6092WispDepID(t, ctx, conn, natural); got != wantID {
			t.Errorf("row %s id = %q after the repair, want the pre-existing %q", natural, got, wantID)
		}
	}
}

// issue6092SeedWispDepsWithDeterministicIDs seeds one valid row per target
// kind carrying explicit surrogate ids and returns them keyed by natural
// identity. The ids are depid.New(issue, target) values rather than arbitrary
// UUIDs: rekeyDependencyIDs (#4259) rewrites every wisp_dependencies id that
// does not equal depid.New on any pass with ignored 26 pending — and a
// cursor-at-4 replay makes 26 pending — so the ids the repair must preserve
// have to already be the deterministic values for the retention claim to be
// about the repair, not about the rekey rewriting them underneath it.
func issue6092SeedWispDepsWithDeterministicIDs(t *testing.T, ctx context.Context, db issue6092Conn) map[string]string {
	t.Helper()
	rows := []struct {
		issue, issueTarget, wispTarget, externalTarget, depType, createdBy string
	}{
		{"w1", "", "w2", "", "blocks", "alice"},
		{"w3", "i1", "", "", "relates", "bob"},
		{"w1", "", "", "external:e1", "mentions", "carol"},
	}
	ids := map[string]string{}
	for _, r := range rows {
		target := r.issueTarget
		if target == "" {
			target = r.wispTarget
		}
		if target == "" {
			target = r.externalTarget
		}
		id := depid.New(r.issue, target)
		var issueTarget, wispTarget, externalTarget any
		if r.issueTarget != "" {
			issueTarget = r.issueTarget
		}
		if r.wispTarget != "" {
			wispTarget = r.wispTarget
		}
		if r.externalTarget != "" {
			externalTarget = r.externalTarget
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata)
VALUES (?, ?, ?, ?, ?, ?, '2026-11-01 10:00:00', ?, JSON_OBJECT())`,
			id, r.issue, issueTarget, wispTarget, externalTarget, r.depType, r.createdBy); err != nil {
			t.Fatalf("seed wisp dep %s: %v", id, err)
		}
		ids[r.issue+"->"+target] = id
	}
	return ids
}

// issue6092WispDepID reads one dependency row's surrogate id by its natural
// identity ("issue->target").
func issue6092WispDepID(t *testing.T, ctx context.Context, db issue6092Conn, natural string) string {
	t.Helper()
	issue, target, ok := strings.Cut(natural, "->")
	if !ok {
		t.Fatalf("bad natural key %q", natural)
	}
	var id string
	if err := db.QueryRowContext(ctx, `
SELECT id FROM wisp_dependencies
WHERE issue_id = ?
  AND (depends_on_issue_id = ? OR depends_on_wisp_id = ? OR depends_on_external = ?)`,
		issue, target, target, target).Scan(&id); err != nil {
		t.Fatalf("read id for %s: %v", natural, err)
	}
	return id
}

// issue6092RunMigrationFile executes one frozen migration file's whole body
// as-is, the way the migration runner does.
func issue6092RunMigrationFile(t *testing.T, ctx context.Context, db issue6092Conn, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if _, err := db.ExecContext(ctx, string(data)); err != nil {
		t.Fatalf("run migration file %s: %v", path, err)
	}
}

// issue6092SeedReferencedRows seeds the wisp and issue rows the dependency
// fixture points at: three wisps and one issue, all referenced.
func issue6092SeedReferencedRows(t *testing.T, ctx context.Context, db issue6092Conn) {
	t.Helper()
	for _, id := range []string{"w1", "w2", "w3"} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral) VALUES (?, 'w', '', '', '', '', 'open', 2, 'task', 1)", id); err != nil {
			t.Fatalf("seed wisp %s: %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('i1', 'i', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("seed issue i1: %v", err)
	}
}

// issue6092SeedValidWispDeps seeds one valid row per target kind — wisp,
// issue and external — each single-target, each with a distinct type and
// created_by so preservation is observable, and each with a distinct
// natural identity so no cleanup step has any reason to touch them.
func issue6092SeedValidWispDeps(t *testing.T, ctx context.Context, db issue6092Conn) {
	t.Helper()
	rows := []struct {
		issue, issueTarget, wispTarget, externalTarget, depType, createdBy string
	}{
		{"w1", "", "w2", "", "blocks", "alice"},
		{"w3", "i1", "", "", "relates", "bob"},
		{"w1", "", "", "external:e1", "mentions", "carol"},
	}
	for _, r := range rows {
		var issueTarget, wispTarget, externalTarget any
		if r.issueTarget != "" {
			issueTarget = r.issueTarget
		}
		if r.wispTarget != "" {
			wispTarget = r.wispTarget
		}
		if r.externalTarget != "" {
			externalTarget = r.externalTarget
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO wisp_dependencies (issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata)
VALUES (?, ?, ?, ?, ?, '2026-11-01 10:00:00', ?, JSON_OBJECT())`,
			r.issue, issueTarget, wispTarget, externalTarget, r.depType, r.createdBy); err != nil {
			t.Fatalf("seed wisp dep %s -> %v: %v", r.issue, r, err)
		}
	}
}

// issue6092CommitFixture commits the tracked part of the fixture (the issues
// insert): an uncommitted write to a synced table would trip MigrateUp's
// dirty-table guard (#4566) before the pass ever reaches ignored/0005. The
// wisp-plane tables are dolt_ignore'd and stage nothing, hence --skip-empty.
func issue6092CommitFixture(t *testing.T, ctx context.Context, db issue6092Conn, message string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		t.Fatalf("stage fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--skip-empty')", message); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
}

// issue6092RequireFinalWispDepShape asserts the structural contract of the
// final wisp_dependencies shape the repair must leave behind: the surrogate
// id primary key, the three natural-identity unique keys, the three type
// indexes 0022 created (which 0005 would have collided with), the legacy
// generated column gone, all three foreign keys against their referenced
// tables, and the one-target check. Names are asserted, not just coverage:
// every fixture here builds objects with these exact names.
func issue6092RequireFinalWispDepShape(t *testing.T, ctx context.Context, db issue6092Conn) {
	t.Helper()

	var genCount int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies' AND COLUMN_NAME = 'depends_on_id'`).Scan(&genCount); err != nil {
		t.Fatalf("probe depends_on_id: %v", err)
	}
	if genCount != 0 {
		t.Fatal("depends_on_id survived the repair")
	}

	var idType, idNullable string
	if err := db.QueryRowContext(ctx, `
SELECT COLUMN_TYPE, IS_NULLABLE FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies' AND COLUMN_NAME = 'id'`).Scan(&idType, &idNullable); err != nil {
		t.Fatalf("probe wisp_dependencies.id: %v", err)
	}
	if idType != "char(36)" || idNullable != "NO" {
		t.Fatalf("wisp_dependencies.id = %s/%s, want char(36)/NO", idType, idNullable)
	}

	var pkCols string
	if err := db.QueryRowContext(ctx, `
SELECT GROUP_CONCAT(COLUMN_NAME ORDER BY ORDINAL_POSITION) FROM INFORMATION_SCHEMA.KEY_COLUMN_USAGE
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies' AND CONSTRAINT_NAME = 'PRIMARY'`).Scan(&pkCols); err != nil {
		t.Fatalf("read primary key: %v", err)
	}
	if pkCols != "id" {
		t.Fatalf("primary key = %q, want id", pkCols)
	}

	for _, index := range []string{
		"uk_wisp_dep_issue_target", "uk_wisp_dep_wisp_target", "uk_wisp_dep_external_target",
		"idx_wisp_dep_type_issue", "idx_wisp_dep_type_wisp", "idx_wisp_dep_type_external",
	} {
		// STATISTICS carries one row per COLUMN of the index, so count the
		// first column's row rather than all of them.
		var n int
		if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies' AND INDEX_NAME = ? AND SEQ_IN_INDEX = 1`, index).Scan(&n); err != nil {
			t.Fatalf("probe index %s: %v", index, err)
		}
		if n != 1 {
			t.Errorf("index %s count = %d, want 1", index, n)
		}
	}

	fks := map[string]string{}
	rows, err := db.QueryContext(ctx, `
SELECT rc.CONSTRAINT_NAME, rc.REFERENCED_TABLE_NAME
FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS rc
WHERE rc.CONSTRAINT_SCHEMA = DATABASE() AND rc.TABLE_NAME = 'wisp_dependencies'`)
	if err != nil {
		t.Fatalf("read foreign keys: %v", err)
	}
	for rows.Next() {
		var name, refTable string
		if err := rows.Scan(&name, &refTable); err != nil {
			rows.Close()
			t.Fatalf("scan foreign key: %v", err)
		}
		fks[name] = refTable
	}
	_ = rows.Close()
	wantFKs := map[string]string{
		"fk_wisp_dep_issue":        "wisps",
		"fk_wisp_dep_wisp_target":  "wisps",
		"fk_wisp_dep_issue_target": "issues",
	}
	for name, refTable := range wantFKs {
		if got, ok := fks[name]; !ok || got != refTable {
			t.Errorf("foreign key %s = %q(%v present), want %s", name, got, ok, refTable)
		}
	}

	var checks int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies'
  AND CONSTRAINT_TYPE = 'CHECK' AND CONSTRAINT_NAME = 'ck_wisp_dep_one_target'`).Scan(&checks); err != nil {
		t.Fatalf("probe ck_wisp_dep_one_target: %v", err)
	}
	if checks != 1 {
		t.Fatalf("ck_wisp_dep_one_target count = %d, want 1", checks)
	}
}

// DRAFT for AC-4 + AC-5 — to append to internal/storage/dolt/issue6092_ignored_wisp_dep_test.go
// once no bazel run is in flight.

// issue6092ShowCreateWispDep returns the canonical SHOW CREATE TABLE text for
// wisp_dependencies: any added, dropped or reordered key, constraint or
// column changes it, which makes it the strongest structural fingerprint for
// idempotence assertions.
func issue6092ShowCreateWispDep(t *testing.T, ctx context.Context, db issue6092Conn) string {
	t.Helper()
	var tbl, create string
	if err := db.QueryRowContext(ctx, "SHOW CREATE TABLE wisp_dependencies").Scan(&tbl, &create); err != nil {
		t.Fatalf("show create wisp_dependencies: %v", err)
	}
	return create
}

// TestIssue6092IgnoredWispDepReplayLeavesConvergedTableUntouched covers the
// converged population of #6092: both chains fully applied, the table in its
// final shape with rows under deterministic ids, and only the ignored cursor
// rewound to 4 — the state a re-run or a restore lands in. The replay of
// ignored 5..31 must leave the table exactly as it stands (the repair
// registered at ignored 5 finds the final shape complete and stands down; the
// frozen 0005 body no-ops on @needs_drop = 0), and a second pass must apply
// nothing at all. This is the guard half of the fix: converging the stranded
// populations must not cost convergence of the healthy one.
func TestIssue6092IgnoredWispDepReplayLeavesConvergedTableUntouched(t *testing.T) {
	dbName, conn, ctx := issue6092Setup(t)

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("exec %.80q: %v", query, err)
		}
	}
	scalar := func(query string) int64 {
		t.Helper()
		var v int64
		if err := conn.QueryRowContext(ctx, query).Scan(&v); err != nil {
			t.Fatalf("query %.80q: %v", query, err)
		}
		return v
	}

	// The converged state: final shape from the setup pass itself, plus rows
	// under depid.New ids (see issue6092SeedWispDepsWithDeterministicIDs: the
	// #4259 rekey rewrites any other id while the rewind leaves 26 pending,
	// and this test asserts the replay, not the rekey, leaves them alone).
	issue6092SeedReferencedRows(t, ctx, conn)
	wantIDs := issue6092SeedWispDepsWithDeterministicIDs(t, ctx, conn)
	updatedBefore := issue6092SnapshotUpdatedAt(t, ctx, conn)
	shapeBefore := issue6092ShowCreateWispDep(t, ctx, conn)

	exec("DELETE FROM ignored_schema_migrations WHERE version > 4")
	issue6092CommitFixture(t, ctx, conn, "test: #6092 converged wisp_dependencies before ignored rewind")

	if got := scalar("SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations"); got != 4 {
		t.Fatalf("fixture ignored cursor = %d, want 4", got)
	}

	// Pass 1: replay 5..31 over the converged table.
	if _, err := schema.MigrateUpWithLock(ctx, conn, dbName); err != nil {
		t.Fatalf("replay over a converged wisp_dependencies (#6092): %v", err)
	}
	if got := scalar("SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != int64(schema.LatestVersion()) {
		t.Fatalf("main cursor = %d after the replay, want latest %d", got, schema.LatestVersion())
	}
	if got := scalar("SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations"); got != int64(schema.LatestIgnoredVersion()) {
		t.Fatalf("ignored cursor = %d after the replay, want latest %d", got, schema.LatestIgnoredVersion())
	}
	if shape := issue6092ShowCreateWispDep(t, ctx, conn); shape != shapeBefore {
		t.Fatalf("replay changed the converged shape:\nbefore:\n%s\nafter:\n%s", shapeBefore, shape)
	}
	issue6092RequireFinalWispDepShape(t, ctx, conn)
	issue6092RequireSeededRowsPreserved(t, ctx, conn, updatedBefore)
	for natural, wantID := range wantIDs {
		if got := issue6092WispDepID(t, ctx, conn, natural); got != wantID {
			t.Errorf("row %s id = %q after the replay, want the pre-existing %q", natural, got, wantID)
		}
	}

	// Pass 2: applies nothing.
	logBefore := scalar("SELECT COUNT(*) FROM dolt_log")
	applied, err := schema.MigrateUpWithLock(ctx, conn, dbName)
	if err != nil {
		t.Fatalf("second pass over the converged table (#6092): %v", err)
	}
	if applied != 0 {
		t.Fatalf("second pass applied %d migrations, want 0", applied)
	}
	if got := scalar("SELECT COUNT(*) FROM dolt_log"); got != logBefore {
		t.Fatalf("dolt_log grew across the second pass: %d -> %d", logBefore, got)
	}
	if shape := issue6092ShowCreateWispDep(t, ctx, conn); shape != shapeBefore {
		t.Fatalf("second pass changed the converged shape:\nbefore:\n%s\nafter:\n%s", shapeBefore, shape)
	}
	for natural, wantID := range wantIDs {
		if got := issue6092WispDepID(t, ctx, conn, natural); got != wantID {
			t.Errorf("row %s id = %q after the second pass, want the pre-existing %q", natural, got, wantID)
		}
	}
}
