package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// TestMigrateUpCountsCloneLocalOnlyPass pins MigrateUp's count for a pass
// whose only numbered work is the clone-local series: the state a clone that
// pulled the migrated main series on an older binary is in after upgrading
// (gastownhall/beads#7038). The pass changes the schema, so it must report
// work; it used to return only the main series' count, 0 here, which told a
// writable open to keep the pool it had pinned to the pre-migration session
// root and told `bd migrate schema` the database was already current. A
// converged pass still reports 0.
func TestMigrateUpCountsCloneLocalOnlyPass(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	_, store := cloneLocalBehindFixture(t, ctx, false)
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	defer conn.Close()

	applied, err := schema.MigrateUp(ctx, conn)
	if err != nil {
		t.Fatalf("MigrateUp (clone-local only): %v", err)
	}
	if applied <= 0 {
		t.Errorf("MigrateUp (clone-local only) = %d, want the clone-local migrations it applied counted", applied)
	}
	if v, err := schema.CurrentIgnoredVersion(ctx, conn); err != nil || v != schema.LatestIgnoredVersion() {
		t.Fatalf("clone-local cursor after MigrateUp = %d (%v), want v%d", v, err, schema.LatestIgnoredVersion())
	}
	if v, err := schema.CurrentVersion(ctx, conn); err != nil || v != schema.LatestVersion() {
		t.Fatalf("main cursor after MigrateUp = %d (%v), want it left at v%d", v, err, schema.LatestVersion())
	}

	again, err := schema.MigrateUp(ctx, conn)
	if err != nil {
		t.Fatalf("MigrateUp (converged): %v", err)
	}
	if again != 0 {
		t.Errorf("MigrateUp (converged) = %d, want 0", again)
	}
}

// TestMigratingOpen_FirstReadSucceedsAfterCloneLocalOnlyPass is the open-side
// half: a writable open whose migration pass applies only the clone-local
// series must hand back a store whose very first issue read sees the
// migrated lease schema, through the existing post-migration pool refresh
// (be-itm5). The issue read is the first statement on the returned store; a
// second, unrelated query would advance the session root itself and mask a
// stale pool.
func TestMigratingOpen_FirstReadSucceedsAfterCloneLocalOnlyPass(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	open, _ := cloneLocalBehindFixture(t, ctx, false)
	rw, err := open(&Config{})
	if err != nil {
		t.Fatalf("New (writable, clone-local only pending): %v", err)
	}
	defer rw.Close()

	issues, err := rw.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("first issue read after a clone-local-only migrating open: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "cl-1" {
		t.Fatalf("first issue read = %d issues, want cl-1", len(issues))
	}
}
