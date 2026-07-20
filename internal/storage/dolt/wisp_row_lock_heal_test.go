package dolt

import (
	"context"
	"fmt"
	"testing"
)

// TestDoltNew_WispRowLockSelfHeal_RealDolt is the regression test for
// bd-rotq2: wisps is dolt-ignored, so its schema is clone-local, and a
// workspace that bootstraps from a remote whose synced cursor is already
// >= 0054 never executes the synced migration that added wisps.row_lock —
// leaving a wisps table the post-0054 binary cannot insert into (Error 1054,
// observed in prod on claude-code-vm). The fix carries the column on the
// ignored track (ignored/0013), which runs at store open, so an affected
// workspace self-heals on its next connect. The test reproduces the broken
// state directly (drop the column, rewind the ignored cursor past 0013 —
// the synced cursor stays at latest, exactly like a bootstrap) and asserts
// a reopen restores the column.
func TestDoltNew_WispRowLockSelfHeal_RealDolt(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)

	cfg := &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
		MaxOpenConns:    1,
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New (create): %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	hasRowLock := func(stage string) bool {
		t.Helper()
		// wisps is dolt-ignored, so its schema lives in the working set and a
		// pooled connection can hold a read view predating the migration's
		// ALTER. Close the view first so the probe sees current state.
		_, _ = store.db.ExecContext(ctx, "COMMIT")
		var n int
		if err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
			   WHERE TABLE_SCHEMA = DATABASE()
			     AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'row_lock'`).Scan(&n); err != nil {
			t.Fatalf("%s: probe wisps.row_lock: %v", stage, err)
		}
		return n > 0
	}

	// A fresh workspace must come out of migrations with the column.
	if !hasRowLock("fresh workspace") {
		t.Fatal("fresh workspace: wisps.row_lock missing after full migration run")
	}

	// Reproduce the bootstrap-from-remote state: local wisps predates 0054
	// (no row_lock) while the SYNCED cursor stays at latest, and the ignored
	// cursor predates the healing migration.
	if _, err := store.db.ExecContext(ctx, "ALTER TABLE wisps DROP COLUMN row_lock"); err != nil {
		t.Fatalf("drop row_lock: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"DELETE FROM ignored_schema_migrations WHERE version >= 13"); err != nil {
		t.Fatalf("rewind ignored cursor: %v", err)
	}
	if hasRowLock("after break") {
		t.Fatal("test setup failed to remove wisps.row_lock")
	}
	store.Close()

	// Reopen: the ignored track runs at store open and must restore the column.
	store, err = New(ctx, cfg)
	if err != nil {
		t.Fatalf("New (reopen): %v", err)
	}
	if !hasRowLock("after reopen") {
		t.Fatal("wisps.row_lock not restored by ignored/0013 on reopen — bootstrap-broken workspaces stay broken")
	}

	// The heal must be usable, not just present: a wisp-shaped insert that
	// names row_lock (what the post-0054 binary does) succeeds.
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO wisps (id, title, description, row_lock) VALUES ('wisp-rotq2-heal', 't', '', 1)`); err != nil {
		t.Fatalf("wisp insert naming row_lock after heal: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM wisps WHERE id = 'wisp-rotq2-heal'"); err != nil {
		t.Fatalf("cleanup wisp row: %v", err)
	}

	// Idempotence: another reopen with the column present must not error.
	store.Close()
	store, err = New(ctx, cfg)
	if err != nil {
		t.Fatalf("New (third open, idempotence): %v", err)
	}
	if !hasRowLock("after third open") {
		t.Fatal("wisps.row_lock lost on subsequent open")
	}
}
