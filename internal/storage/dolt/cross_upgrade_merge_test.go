package dolt

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// preReshapeVersion is the last schema version before the 0041/0043/0050
// dependencies primary-key reshape: at 0040 the dependencies PK is the
// natural composite (issue_id, depends_on_id); at HEAD it is the
// deterministic id column. A merge whose common ancestor sits at or before
// 0040 therefore crosses a PK boundary — the #4259 incident geometry.
const preReshapeVersion = 40

// crossUpgradeTimeout is generous: each scenario runs the full migration
// chain three times (ancestor bootstrap to 0040, then 0041..HEAD on each of
// two branches).
const crossUpgradeTimeout = 4 * time.Minute

// TestCrossUpgradeBoundaryMerge pins the cross-upgrade merge semantics from
// the #4259 incident audit (bd-6dnrw.16): two clones whose common ancestor
// predates the dependencies PK reshape, each migrated to HEAD independently.
// dependencies_merge_test.go only forks from post-migration ancestors, so
// this exact geometry — the one that corrupted production — was untested.
//
// Scenario (a), clean boundary crossing: the clones synced immediately
// before upgrading, so no row edits straddle the boundary. Both sides run
// the identical migration chain; the #4266/0050 deterministic rekey must
// make the results byte-identical, and the merge must converge with the
// legacy edge present exactly once under its deterministic id.
//
// Scenario (b), stranded deltas: each clone wrote dependency edges in the
// OLD schema before upgrading. The ancestor's composite PK differs from both
// heads' id PK with real row diffs to reconcile, so Dolt refuses the merge
// outright ("cannot merge because table dependencies has different primary
// keys ..."). The refusal must be recognized by dberrors.IsAncestorPKMismatch
// — the classifier behind bd dolt pull's recovery guidance — pinning the
// handler against a real Dolt error rather than hardcoded strings.
//
// (Scenario (c) of the audit — clones already forked by broken-1.0.5 random
// ids — is covered by the negative control in dependencies_merge_test.go.)
func TestCrossUpgradeBoundaryMerge(t *testing.T) {
	t.Run("synced clones converge across the boundary", func(t *testing.T) {
		env := setupPreReshapeAncestor(t)
		ctx := env.ctx

		env.upgradeBranch(t, "clone_a")
		env.upgradeBranch(t, "clone_b")

		env.checkout(t, "clone_a")
		_, mergeErr := env.conn.ExecContext(ctx, "CALL DOLT_MERGE('clone_b')")
		if mergeErr != nil {
			t.Fatalf("clean boundary crossing must converge (4266 claim), got: %v", mergeErr)
		}

		var rows int
		if err := env.conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = 'anc-x' AND depends_on_issue_id = 'anc-y'").Scan(&rows); err != nil {
			t.Fatalf("count legacy edge: %v", err)
		}
		if rows != 1 {
			t.Errorf("legacy edge should survive the merge exactly once, got %d rows", rows)
		}

		// The edge must carry the deterministic id on the merged result: this
		// is what made the merge clean. Same-random-luck is not possible, and
		// a UUID here means the 0050 rekey regressed.
		var gotID string
		if err := env.conn.QueryRowContext(ctx,
			"SELECT id FROM dependencies WHERE issue_id = 'anc-x' AND depends_on_issue_id = 'anc-y'").Scan(&gotID); err != nil {
			t.Fatalf("read merged edge id: %v", err)
		}
		if want := depid.New("anc-x", "anc-y"); gotID != want {
			t.Errorf("merged edge id = %q, want deterministic %q", gotID, want)
		}
	})

	t.Run("stranded pre-upgrade deltas are refused with the ancestor-PK signature", func(t *testing.T) {
		env := setupPreReshapeAncestor(t)
		ctx := env.ctx

		// Divergent edges written in the OLD schema (composite-PK shape),
		// one per clone, committed before either side upgrades.
		env.checkout(t, "clone_a")
		env.exec(t, "INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by) "+
			"VALUES ('anc-x', 'anc-z', 'blocks', '2020-01-02 00:00:00', 'a')")
		env.exec(t, "CALL DOLT_COMMIT('-Am', 'stranded edge on clone_a')")

		env.checkout(t, "clone_b")
		env.exec(t, "INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by) "+
			"VALUES ('anc-y', 'anc-z', 'blocks', '2020-01-03 00:00:00', 'b')")
		env.exec(t, "CALL DOLT_COMMIT('-Am', 'stranded edge on clone_b')")

		env.upgradeBranch(t, "clone_a")
		env.upgradeBranch(t, "clone_b")

		env.checkout(t, "clone_a")
		_, mergeErr := env.conn.ExecContext(ctx, "CALL DOLT_MERGE('clone_b')")
		if mergeErr == nil {
			t.Fatal("merge across the PK boundary with stranded deltas converged cleanly; " +
				"expected Dolt's different-primary-keys refusal (if Dolt learned to merge " +
				"this, the recovery guidance in cmd/bd/dolt.go may be obsolete)")
		}
		if !dberrors.IsAncestorPKMismatch(mergeErr) {
			t.Errorf("dberrors.IsAncestorPKMismatch must recognize the real refusal "+
				"(bd dolt pull guidance depends on it), got unrecognized error: %v", mergeErr)
		}
		if table := dberrors.AncestorPKMismatchTable(mergeErr); table != "dependencies" {
			t.Errorf("AncestorPKMismatchTable = %q, want %q (error: %v)", table, "dependencies", mergeErr)
		}
	})
}

// crossUpgradeEnv is a fresh database on the test server holding a v0040
// ancestor commit with two branches (clone_a, clone_b) forked from it, all
// driven over one pinned connection (DOLT_CHECKOUT is session-scoped).
type crossUpgradeEnv struct {
	ctx  context.Context
	conn *sql.Conn
}

func (e *crossUpgradeEnv) exec(t *testing.T, query string) {
	t.Helper()
	if _, err := e.conn.ExecContext(e.ctx, query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func (e *crossUpgradeEnv) checkout(t *testing.T, branch string) {
	t.Helper()
	if _, err := e.conn.ExecContext(e.ctx, "CALL DOLT_CHECKOUT(?)", branch); err != nil {
		t.Fatalf("checkout %s: %v", branch, err)
	}
}

// upgradeBranch runs the full production upgrade (schema.MigrateUp, including
// the deterministic rekeys) on the given branch, as an independent clone
// would on first contact with the new binary.
func (e *crossUpgradeEnv) upgradeBranch(t *testing.T, branch string) {
	t.Helper()
	e.checkout(t, branch)
	if _, err := schema.MigrateUp(e.ctx, e.conn); err != nil {
		t.Fatalf("MigrateUp on %s: %v", branch, err)
	}
	// Frozen migrations 0046/0047 flip is_blocked on issue rows, and
	// issues.updated_at carries ON UPDATE CURRENT_TIMESTAMP, so each branch
	// stamps its own migration wall clock on blocked rows - branches
	// upgraded across a second boundary then differ in bookkeeping only and
	// the raw DOLT_MERGE below reports a conflict, drowning the 4266 claim
	// this test pins (bd-578h9.19; production avoids the geometry via the
	// remote-migrate gate, and the runtime recompute now preserves
	// updated_at). Pin it to a constant on both branches.
	if _, err := e.conn.ExecContext(e.ctx,
		"UPDATE issues SET updated_at = '2026-01-01 00:00:00'"); err != nil {
		t.Fatalf("pin issues.updated_at on %s: %v", branch, err)
	}
	// MigrateUp commits its own work; sweep anything it intentionally leaves
	// in the working set so the merge below compares committed state only.
	if _, err := e.conn.ExecContext(e.ctx, "CALL DOLT_COMMIT('-Am', 'post-upgrade sweep')"); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
		t.Fatalf("post-upgrade commit on %s: %v", branch, err)
	}
}

// setupPreReshapeAncestor creates a fresh database migrated to
// preReshapeVersion, seeds issues anc-x/anc-y/anc-z plus the edge
// anc-x -> anc-y in the composite-PK shape, commits that as the shared
// ancestor, and forks clone_a/clone_b from it.
func setupPreReshapeAncestor(t *testing.T) *crossUpgradeEnv {
	t.Helper()
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	ctx, cancel := context.WithTimeout(context.Background(), crossUpgradeTimeout)
	t.Cleanup(cancel)

	dbName := uniqueTestDBName(t)
	admin, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { admin.Close() })
	// Skip DROP DATABASE on cleanup — rapid CREATE/DROP cycles crash the Dolt
	// container (see setupConcurrentTestStore); orphans die with the container.
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

	env := &crossUpgradeEnv{ctx: ctx, conn: conn}

	if _, err := schema.MigrateUpTo(ctx, conn, preReshapeVersion); err != nil {
		t.Fatalf("migrate ancestor to %04d: %v", preReshapeVersion, err)
	}

	// wisps is a dolt-ignored, clone-local table, so a branch fork would not
	// inherit it — but a real v0040 clone HAS it (created when migration 0020
	// ran there), and post-0040 migrations read and ALTER it. Materialize it
	// into the ancestor commit so both "clones" start with it, then restore
	// the ignore rows for parity with production dolt_ignore state. The
	// wisp_% tables stay untracked: no post-0040 main migration touches them,
	// and each branch's ignored-migration pass recreates them clone-locally,
	// just like a real clone. (Same trick as testutil's
	// MaterializeLocalTableSchemasForBranchTests, scoped to wisps.)
	env.exec(t, "DELETE FROM dolt_ignore WHERE pattern IN ('wisps', 'wisp_%')")

	for _, id := range []string{"anc-x", "anc-y", "anc-z"} {
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, '', '', '', '', 'open', 2, 'task')",
			id, id); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}
	// The shared edge, in the old composite-PK shape, synced to both clones
	// before the upgrade window opens.
	env.exec(t, "INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by) "+
		"VALUES ('anc-x', 'anc-y', 'blocks', '2020-01-01 00:00:00', 'sync')")
	env.exec(t, "CALL DOLT_COMMIT('-Am', 'shared ancestor at v0040')")

	env.exec(t, "REPLACE INTO dolt_ignore VALUES ('wisps', true)")
	env.exec(t, "REPLACE INTO dolt_ignore VALUES ('wisp_%', true)")
	env.exec(t, "CALL DOLT_COMMIT('-Am', 'restore wisp ignore patterns')")

	env.exec(t, "CALL DOLT_BRANCH('clone_a', 'HEAD')")
	env.exec(t, "CALL DOLT_BRANCH('clone_b', 'HEAD')")
	return env
}
