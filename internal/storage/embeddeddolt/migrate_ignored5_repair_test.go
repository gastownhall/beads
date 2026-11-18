//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// Real-Dolt mirrors of the #6092 ignored-5 entry point for
// repairWispDependenciesForwardShape.
//
// seedMainSchemaAt brings main to latest WITHOUT ever running the ignored
// chain (MigrateUpTo is main-only), which reproduces the skew at the heart of
// #6092: main's cursor is past 58, so no pending main migration hosts the
// repair, and the ignored replay runs first over whatever the store carries.
// ignored/0001's create dance and 0003's split-column body are guarded against
// already-present objects, so the replay walks 0001..0004 as no-ops over the
// fixture shapes and reaches the repair at 5 immediately before the frozen
// 0005 body.
//
// The fixtures rebuild wisp_dependencies the way the affected clones got it:
// the legacy generated-column delegate shape (legacyDelegateWispDepDDL, in
// migrate_wisp_dep_forward_repair_test.go) or the partially applied 0005
// shape, plus main 0022's three type indexes executed from the shipped file.

// partial0005WispDepDDL is the shape
// TestIssue6092IgnoredWispDepRepairRestoresConstraintsAfterFailedRun in
// internal/storage/dolt builds: the id-keyed table a partially applied
// ignored/0005 strands — unique keys and type indexes in place, not one of the
// four final constraints.
const partial0005WispDepDDL = `
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
    KEY idx_wisp_dep_type (type),
    KEY idx_wisp_dep_type_issue (type, depends_on_issue_id),
    KEY idx_wisp_dep_type_wisp (type, depends_on_wisp_id),
    KEY idx_wisp_dep_type_external (type, depends_on_external)
)`

func TestEmbeddedIgnored5RepairHealsSkewedClone(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	fresh := readWispDepShape(t, ctx, referenceStoreConn(t, ctx))

	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dataDir)

	// The reporter's population: a clone-local legacy delegate table that
	// already carries main 0022's three type indexes.
	mustExecConn(t, ctx, conn, "DROP TABLE wisp_dependencies")
	mustExecConn(t, ctx, conn, legacyDelegateWispDepDDL)
	mustExecConn(t, ctx, conn, "ALTER TABLE wisp_dependencies ADD CONSTRAINT fk_wisp_dep_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE")
	mustRunMainMigration(t, ctx, conn, "0022_wisp_dep_type_index.up.sql")

	for _, id := range []string{"w1", "w2", "w3"} {
		seedWisp(t, ctx, conn, id)
	}
	seedIssue(t, ctx, conn, "i1")
	seedWispDep(t, ctx, conn, "w1", nil, strptr("w2"), nil)
	seedWispDep(t, ctx, conn, "w3", strptr("i1"), nil, nil)
	seedWispDep(t, ctx, conn, "w1", nil, nil, strptr("external:e1"))
	commitSeed(t, ctx, conn, "test: #6092 legacy wisp_dependencies with 0022's type indexes")
	closeConn()

	conn2, closeConn2 := openPinnedConn(t, ctx, dataDir)
	defer closeConn2()

	// PRE-fix the ignored replay dies at 0005 re-adding 0022's indexes.
	if _, err := schema.MigrateUp(ctx, conn2); err != nil {
		t.Fatalf("MigrateUp over a skewed clone's legacy wisp_dependencies (#6092): %v", err)
	}

	if v := currentMainVersion(t, ctx, conn2); v != schema.LatestVersion() {
		t.Fatalf("main schema version = %d, want latest %d", v, schema.LatestVersion())
	}
	var ignored int64
	if err := conn2.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM ignored_schema_migrations").Scan(&ignored); err != nil {
		t.Fatalf("read ignored cursor: %v", err)
	}
	if ignored != int64(schema.LatestIgnoredVersion()) {
		t.Fatalf("ignored schema version = %d, want latest %d", ignored, schema.LatestIgnoredVersion())
	}
	requireSameWispDepShape(t, readWispDepShape(t, ctx, conn2), fresh)

	want := []wispDepRow{
		{"w1", "", "", "external:e1"},
		{"w1", "", "w2", ""},
		{"w3", "i1", "", ""},
	}
	if got := readWispDeps(t, ctx, conn2); !sameWispDepRows(got, want) {
		t.Fatalf("surviving rows = %v, want %v", got, want)
	}
}

func TestEmbeddedIgnored5RepairRestoresConstraintsAfterFailedRun(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	fresh := readWispDepShape(t, ctx, referenceStoreConn(t, ctx))

	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dataDir)

	// The stranded state: cursor still at 4 (the ignored chain never ran), the
	// table already id-keyed with its unique keys and type indexes but none of
	// the four final constraints, and rows carrying explicit surrogate ids the
	// way a partially applied 0005 minted them.
	mustExecConn(t, ctx, conn, "DROP TABLE wisp_dependencies")
	mustExecConn(t, ctx, conn, partial0005WispDepDDL)
	for _, id := range []string{"w1", "w2", "w3"} {
		seedWisp(t, ctx, conn, id)
	}
	seedIssue(t, ctx, conn, "i1")
	wantIDs := map[string]string{
		"w1->w2":          depid.New("w1", "w2"),
		"w3->i1":          depid.New("w3", "i1"),
		"w1->external:e1": depid.New("w1", "external:e1"),
	}
	// depid.New ids, not arbitrary UUIDs: rekeyDependencyIDs (#4259)
	// rewrites every wisp_dependencies id that does not equal depid.New on
	// any pass with ignored 26 pending, so the ids the repair must preserve
	// have to already be the deterministic values.
	seedWispDepWithID(t, ctx, conn, wantIDs["w1->w2"], "w1", nil, strptr("w2"), nil)
	seedWispDepWithID(t, ctx, conn, wantIDs["w3->i1"], "w3", strptr("i1"), nil, nil)
	seedWispDepWithID(t, ctx, conn, wantIDs["w1->external:e1"], "w1", nil, nil, strptr("external:e1"))
	commitSeed(t, ctx, conn, "test: #6092 partial 0005 shape without target constraints")
	closeConn()

	conn2, closeConn2 := openPinnedConn(t, ctx, dataDir)
	defer closeConn2()

	// PRE-fix this pass succeeds and advances the ignored cursor while the
	// frozen body no-ops on @needs_drop = 0, leaving every constraint missing.
	if _, err := schema.MigrateUp(ctx, conn2); err != nil {
		t.Fatalf("MigrateUp over a partially applied 0005 (#6092): %v", err)
	}

	if v := currentMainVersion(t, ctx, conn2); v != schema.LatestVersion() {
		t.Fatalf("main schema version = %d, want latest %d", v, schema.LatestVersion())
	}
	var ignored int64
	if err := conn2.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM ignored_schema_migrations").Scan(&ignored); err != nil {
		t.Fatalf("read ignored cursor: %v", err)
	}
	if ignored != int64(schema.LatestIgnoredVersion()) {
		t.Fatalf("ignored schema version = %d, want latest %d", ignored, schema.LatestIgnoredVersion())
	}
	requireSameWispDepShape(t, readWispDepShape(t, ctx, conn2), fresh)

	for natural, wantID := range wantIDs {
		issue, target, ok := strings.Cut(natural, "->")
		if !ok {
			t.Fatalf("bad natural key %q", natural)
		}
		var id string
		if err := conn2.QueryRowContext(ctx, `
SELECT id FROM wisp_dependencies
WHERE issue_id = ?
  AND (depends_on_issue_id = ? OR depends_on_wisp_id = ? OR depends_on_external = ?)`,
			issue, target, target, target).Scan(&id); err != nil {
			t.Fatalf("read id for %s: %v", natural, err)
		}
		if id != wantID {
			t.Errorf("row %s id = %q after the repair, want the pre-existing %q", natural, id, wantID)
		}
	}
}

// seedWispDepWithID is seedWispDep for a table whose surrogate id the fixture
// controls.
func seedWispDepWithID(t *testing.T, ctx context.Context, conn *sql.Conn, id, issueID string, issueTarget, wispTarget, externalTarget *string) {
	t.Helper()
	mustExecConn(t, ctx, conn, `
INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata)
VALUES (?, ?, ?, ?, ?, 'blocks', NOW(), 'tester', JSON_OBJECT())`,
		id, issueID, nullable(issueTarget), nullable(wispTarget), nullable(externalTarget))
}

// mustRunMainMigration executes one shipped main migration file's whole body
// as-is, the way the migration runner does. It reads the embedded copy — a
// source-tree relative path does not survive this target's sharded runfiles.
func mustRunMainMigration(t *testing.T, ctx context.Context, conn *sql.Conn, name string) {
	t.Helper()
	body, err := schema.MigrationSQL(name)
	if err != nil {
		t.Fatalf("read main migration %s: %v", name, err)
	}
	mustExecConn(t, ctx, conn, body)
}
