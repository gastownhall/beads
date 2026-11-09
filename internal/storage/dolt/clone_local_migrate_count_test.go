package dolt

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// regressCloneLocalOnly puts a freshly created database into #7038's
// consumer-clone shape: the main series current (what `bd dolt pull` on the
// old binary brought down), the clone-local series behind with granted_node —
// ignored/0016 — missing (what the pull cannot bring). Both objects are
// dolt-ignored, so the working set stays clean.
func regressCloneLocalOnly(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	for _, stmt := range []string{
		"ALTER TABLE leases DROP COLUMN granted_node",
		"DELETE FROM ignored_schema_migrations WHERE version > 13",
	} {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// TestMigrateUpCountsCloneLocalOnlyPass is #7038's AC-3: a migration pass
// whose only work is the clone-local (ignored) series — exactly the
// pull-then-upgrade consumer clone — must report positive work, so the
// callers that gate on "did this pass change the database?" (the migrating
// open's post-migration pool rebuild, `bd migrate schema`'s applied-status
// bookkeeping) do not conclude the database is already current right after
// it applied the clone-local series. A converged database still reports 0.
func TestMigrateUpCountsCloneLocalOnlyPass(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)
	store, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New (create): %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	regressCloneLocalOnly(t, ctx, store)

	// The pinned-connection contract MigrateUp documents: the pass suspends
	// session state, so it must own one session for its whole run.
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	defer func() { conn.Close() }()

	applied, err := schema.MigrateUp(ctx, conn)
	if err != nil {
		t.Fatalf("MigrateUp (clone-local-only pass): %v", err)
	}
	if applied <= 0 {
		t.Fatalf("MigrateUp on a clone-local-only pass applied = %d, want > 0: the pass just applied the clone-local series the pull could not bring, and its callers gate a pool rebuild and migrate-status bookkeeping on this count (#7038)", applied)
	}

	// The count was not the only thing that moved: the series really
	// converged, so the database a second pass reports is honestly zero.
	var ignored int
	if err := conn.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations").Scan(&ignored); err != nil {
		t.Fatalf("read ignored cursor: %v", err)
	}
	if ignored != schema.LatestIgnoredVersion() {
		t.Errorf("ignored cursor = %d after the pass, want %d", ignored, schema.LatestIgnoredVersion())
	}
	var granted int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'leases' AND column_name = 'granted_node'").Scan(&granted); err != nil {
		t.Fatalf("probe granted_node: %v", err)
	}
	if granted != 1 {
		t.Errorf("granted_node column count = %d after the pass, want 1", granted)
	}
	converged, err := schema.MigrateUp(ctx, conn)
	if err != nil {
		t.Fatalf("MigrateUp (converged): %v", err)
	}
	if converged != 0 {
		t.Errorf("MigrateUp on a converged database applied = %d, want 0: the count fix must not invent work", converged)
	}
}

// TestMigratingOpenServesFirstReadAfterCloneLocalOnlyPass is the consumer
// side of the count fix (#7038 AC-3): the open that migrates only the
// clone-local series must rebuild its connection pool — the pool's Ping
// pinned a connection to the pre-migration session root, and that
// connection serves stale reads that do not self-heal on retry (be-itm5) —
// so the first issue read through the freshly opened store selects the
// lease column the pass just added instead of failing on it.
func TestMigratingOpenServesFirstReadAfterCloneLocalOnlyPass(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)
	setup, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New (create): %v", err)
	}
	if err := setup.SetConfig(ctx, "issue_prefix", "mco"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := setup.CreateIssue(ctx, &types.Issue{
		ID:        "mco-1",
		Title:     "Readable through the first migrating open",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, "test-user"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	regressCloneLocalOnly(t, ctx, setup)
	if err := setup.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}

	// The migrating open: not read-only, not a gateway, so it applies the
	// pending clone-local series and then must rebuild its pool.
	store, err := New(ctx, &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       dbName,
	})
	if err != nil {
		t.Fatalf("New (migrating open): %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues (first read after a clone-local-only migrating open) = %v; the open's stale pinned pool still serves the pre-migration schema the pass just replaced", err)
	}
	found := false
	for _, issue := range issues {
		if issue.ID == "mco-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("SearchIssues did not return mco-1 after the migrating open: %+v", issues)
	}
}
