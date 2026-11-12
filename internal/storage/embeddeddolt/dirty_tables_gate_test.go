//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedDirtyTablesGate_BlocksReopenThenWorkingSetReconcileRecovers
// covers gastownhall/beads#4566: a pending schema migration that alters a
// table with pre-existing dirty (uncommitted) content must refuse a plain
// open, but a working-set-reconcile open (used by "bd dolt commit" / "bd vc
// commit") must still be able to open the store, so the commit that clears
// the dirty state can actually run. Without this, the documented recovery
// deadlocks against the very guard it is meant to clear.
func TestEmbeddedDirtyTablesGate_BlocksReopenThenWorkingSetReconcileRecovers(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	// Create and fully migrate the embedded database.
	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "testdb"); err != nil {
		store.Close()
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		store.Close()
		t.Fatalf("Commit (init): %v", err)
	}

	// Leave `issues` dirty in the working set: CreateIssue writes through a
	// SQL transaction but does not run a Dolt commit, so the new row stays
	// uncommitted.
	issue := &types.Issue{
		ID:        "testdb-1",
		Title:     "dirty working set issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		store.Close()
		t.Fatalf("CreateIssue: %v", err)
	}

	// Regress to schema v51, pinned as an absolute version rather than
	// relative to schema.LatestVersion():
	//   - not 52: mainSource.currentVersion()==52 routes into the
	//     failed-v53-migration recovery path (failed0053DirtyTablesAreRecoverable,
	//     internal/storage/schema/schema.go), which treats a dirty `issues`
	//     table as expected fallout of a crashed 0053 pass and would swallow
	//     the guard this test asserts on.
	//   - absolute (51), not schema.LatestVersion()-2: that expression only
	//     equals 51 by coincidence of the migration count today. A future
	//     migration landing would shift it to 52, silently routing this test
	//     into the recovery path above instead of the guard it means to
	//     exercise. 51 also still leaves migration 0053 (which alters
	//     `issues` via a rig-wisp repair INSERT/UPDATE) pending, so the dirty
	//     `issues` table is still touched by a pending migration.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		store.Close()
		t.Fatalf("OpenSQL: %v", err)
	}
	const regressedVersion = 51
	if _, err := db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version > ?", regressedVersion); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}
	_ = cleanup()
	store.Close()

	// Plain Open must hit the dirty-table guard.
	gated, gateErr := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if gateErr == nil {
		gated.Close()
		t.Fatal("Open (reopen) = nil, want *schema.DirtyTablesError for a dirty table touched by a pending migration")
	}
	var dirtyErr *schema.DirtyTablesError
	if !errors.As(gateErr, &dirtyErr) {
		t.Fatalf("Open error = %T (%v), want error wrapping *schema.DirtyTablesError", gateErr, gateErr)
	}
	found := false
	for _, table := range dirtyErr.Tables {
		if table == "issues" {
			found = true
		}
	}
	if !found {
		t.Fatalf("DirtyTablesError.Tables = %v, want to include %q", dirtyErr.Tables, "issues")
	}

	// The working-set-reconcile open must succeed WITHOUT migrating: the
	// schema stays behind, and the store is otherwise usable.
	reconcileStore, err := embeddeddolt.OpenForWorkingSetReconcile(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenForWorkingSetReconcile: %v", err)
	}
	if current, err := schemaVersion(ctx, dataDir, "testdb"); err != nil {
		reconcileStore.Close()
		t.Fatalf("read schema version: %v", err)
	} else if current != regressedVersion {
		reconcileStore.Close()
		t.Fatalf("schema version = %d, want %d (migration must stay skipped)", current, regressedVersion)
	}

	// Committing the working set (the documented #4566 recovery) must
	// succeed and clear the dirty `issues` table.
	if err := reconcileStore.Commit(ctx, "checkpoint"); err != nil {
		reconcileStore.Close()
		t.Fatalf("Commit: %v", err)
	}
	reconcileStore.Close()

	// A plain reopen now migrates cleanly up to the latest schema.
	migrated, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open (post-commit reopen): %v", err)
	}
	defer migrated.Close()
	if current, err := schemaVersion(ctx, dataDir, "testdb"); err != nil {
		t.Fatalf("read schema version: %v", err)
	} else if current != schema.LatestVersion() {
		t.Fatalf("schema version after reopen = %d, want latest %d", current, schema.LatestVersion())
	}
}

// TestEmbeddedIgnoredDirtyTablesGate_WorkingSetReconcileRecovers covers
// gastownhall/beads#5168: the ignored-source twin of the #4566 guard. A
// dolt_ignore'd wisp table that is tracked and dirty, touched by a pending
// ignored migration, must not leave "bd dolt commit" (a working-set-reconcile
// open) unable to open the store: the reconcile open must succeed, its commit
// must clear the dirty state, and a plain reopen must then migrate.
func TestEmbeddedIgnoredDirtyTablesGate_WorkingSetReconcileRecovers(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "testdb"); err != nil {
		store.Close()
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		store.Close()
		t.Fatalf("Commit (init): %v", err)
	}
	wisp := &types.Issue{
		ID:        "testdb-wisp-1",
		Title:     "wisp with real comments",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		store.Close()
		t.Fatalf("CreateIssue(wisp): %v", err)
	}
	if _, err := store.AddIssueComment(ctx, wisp.ID, "tester", "first"); err != nil {
		store.Close()
		t.Fatalf("AddIssueComment(first): %v", err)
	}
	store.Close()

	// Track the wisp tables despite dolt_ignore (the reporter's store had
	// them in HEAD), then leave wisp_comments dirty with a second comment.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	for _, stmt := range []string{
		"CALL DOLT_ADD('-f', 'wisps', 'wisp_comments')",
		"CALL DOLT_COMMIT('-m', 'track wisp tables')",
		"INSERT INTO wisp_comments (id, issue_id, author, text, created_at) VALUES ('wc-dirty', 'testdb-wisp-1', 'tester', 'second', NOW())",
		// Regress the ignored cursor so ignored/0024 (ALTER TABLE
		// wisp_comments) is pending again.
		"DELETE FROM ignored_schema_migrations WHERE version > 23",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			_ = cleanup()
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	_ = cleanup()

	reconcileStore, err := embeddeddolt.OpenForWorkingSetReconcile(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenForWorkingSetReconcile = %v; want the reconcile open (bd dolt commit) to recover dirty dolt_ignore'd wisp tables like the #4566 main-source guard", err)
	}
	if err := reconcileStore.Commit(ctx, "checkpoint"); err != nil {
		reconcileStore.Close()
		t.Fatalf("Commit: %v", err)
	}
	reconcileStore.Close()

	migrated, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open (post-commit reopen): %v", err)
	}
	migrated.Close()
}

// schemaVersion reads the current main-source schema cursor via a short-lived
// raw SQL connection.
func schemaVersion(ctx context.Context, dataDir, database string) (int, error) {
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, database, "main")
	if err != nil {
		return 0, err
	}
	defer func() { _ = cleanup() }()
	var version int
	err = db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	return version, err
}
