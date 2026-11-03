package dolt

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDoltNew_WispIsBlockedSelfHealAtLatestIgnoredCursor_RealDolt is the
// regression test for gastownhall/beads#4468 (#5257): a clone whose ignored
// cursor is already past ignored/0006 and ignored/0015 can still carry a wisps
// table without is_blocked, and then every blocked-state path over the wisp
// family fails ("table ... does not have column is_blocked"). The repairs at
// ignored 7 and 15 fire only while one of those is pending, so before ignored
// 0032 nothing restored the column. The test builds that shape directly: drop
// the column and its index and hold the ignored cursor at 31 (the top of the
// series before 0032), then asserts a reopen restores both and the ready path
// over wisps works.
func TestDoltNew_WispIsBlockedSelfHealAtLatestIgnoredCursor_RealDolt(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)

	cfg := &Config{
		Path:            tmpDir,
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

	count := func(stage, query string) int {
		t.Helper()
		// wisps is dolt-ignored: close any read view predating the ALTER.
		_, _ = store.db.ExecContext(ctx, "COMMIT")
		var n int
		if err := store.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		return n
	}
	hasColumn := func(stage string) bool {
		return count(stage, `SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'is_blocked'`) > 0
	}
	hasIndex := func(stage string) bool {
		return count(stage, `SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND INDEX_NAME = 'idx_wisps_is_blocked'`) > 0
	}

	if !hasColumn("fresh workspace") || !hasIndex("fresh workspace") {
		t.Fatal("fresh workspace: wisps.is_blocked or its index missing after a full migration run")
	}

	// The #4468 shape: wisps without is_blocked while the ignored cursor sits
	// at 31, so no migration that adds the column is pending.
	for _, stmt := range []string{
		"ALTER TABLE wisps DROP INDEX idx_wisps_is_blocked",
		"ALTER TABLE wisps DROP COLUMN is_blocked",
		"DELETE FROM ignored_schema_migrations WHERE version > 31",
	} {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if hasColumn("after break") {
		t.Fatal("test setup failed to remove wisps.is_blocked")
	}
	if got := count("ignored cursor", "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations"); got != 31 {
		t.Fatalf("ignored cursor after break = %d, want 31", got)
	}
	store.Close()

	store, err = New(ctx, cfg)
	if err != nil {
		t.Fatalf("New (reopen): %v", err)
	}
	if !hasColumn("after reopen") {
		t.Fatal("wisps.is_blocked not restored on reopen: a clone past ignored 0015 without the column stays broken (#4468)")
	}
	if !hasIndex("after reopen") {
		t.Fatal("idx_wisps_is_blocked not restored on reopen")
	}
	if _, err := store.GetReadyWork(ctx, types.WorkFilter{}); err != nil {
		t.Fatalf("GetReadyWork after heal: %v", err)
	}

	// Idempotence: another open with the column present must not error.
	store.Close()
	store, err = New(ctx, cfg)
	if err != nil {
		t.Fatalf("New (third open, idempotence): %v", err)
	}
	if !hasColumn("after third open") {
		t.Fatal("wisps.is_blocked lost on a subsequent open")
	}
}
