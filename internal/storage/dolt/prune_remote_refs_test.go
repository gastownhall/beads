package dolt

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// TestDoltNew_PruneRemoteRefs_RealDolt is the regression test for bd-agctw:
// on a workspace with a registered remote and a pushed ref, a post-flatten GC
// is a silent no-op because the cached remote-tracking ref still anchors the
// entire pre-flatten chain. The fix is PruneRemoteRefs between the squash and
// the GC. The test proves the anchor exists after Flatten (the old chain is
// still readable through the remote ref), prunes it, and verifies the anchor
// is gone and GC runs clean. Tags are asserted list-only: they anchor history
// the same way but must never be deleted by the prune.
func TestDoltNew_PruneRemoteRefs_RealDolt(t *testing.T) {
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
		MaxOpenConns:    1,
	})
	if err != nil {
		t.Fatalf("New (create): %v", err)
	}
	db := store.db
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), testTimeout)
		defer dropCancel()
		_, _ = db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	mustExec := func(stage, q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}

	// Register a remote and push, caching remotes/origin/main; then grow local
	// history past the pushed tip so the remote ref anchors an old chain.
	mustExec("add remote", "CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(tmpDir, "remote"))
	mustExec("commit 1", "CALL DOLT_COMMIT('--allow-empty', '-m', 'test: commit 1')")
	mustExec("push", "CALL DOLT_PUSH('origin', 'main')")
	mustExec("commit 2", "CALL DOLT_COMMIT('--allow-empty', '-m', 'test: commit 2')")
	mustExec("tag", "CALL DOLT_TAG('prune-test-tag')")

	refs, err := store.ListRemoteRefs(ctx)
	if err != nil {
		t.Fatalf("ListRemoteRefs: %v", err)
	}
	if len(refs) != 1 || refs[0] != "remotes/origin/main" {
		t.Fatalf("ListRemoteRefs = %v, want [remotes/origin/main]", refs)
	}

	tags, err := store.ListTags(ctx)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags) != 1 || tags[0] != "prune-test-tag" {
		t.Fatalf("ListTags = %v, want [prune-test-tag]", tags)
	}

	if err := store.Flatten(ctx); err != nil {
		t.Fatalf("Flatten: %v", err)
	}

	// The bug precondition: after the squash, local main is just the initial
	// commit plus the flattened snapshot, but the pre-flatten chain is still
	// fully reachable through the cached remote-tracking ref — exactly what
	// makes the follow-up GC a no-op.
	var localCommits, anchoredCommits int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&localCommits); err != nil {
		t.Fatalf("count local commits: %v", err)
	}
	if localCommits != 2 {
		t.Fatalf("local commits after flatten = %d, want 2 (init + snapshot)", localCommits)
	}
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_log('remotes/origin/main')").Scan(&anchoredCommits); err != nil {
		t.Fatalf("count commits anchored by remote ref: %v", err)
	}
	if anchoredCommits < 2 {
		t.Fatalf("commits anchored by remote ref = %d, want >= 2 (old chain)", anchoredCommits)
	}

	pruned, err := store.PruneRemoteRefs(ctx)
	if err != nil {
		t.Fatalf("PruneRemoteRefs: %v", err)
	}
	if len(pruned) != 1 || pruned[0] != "remotes/origin/main" {
		t.Fatalf("PruneRemoteRefs = %v, want [remotes/origin/main]", pruned)
	}

	refs, err = store.ListRemoteRefs(ctx)
	if err != nil {
		t.Fatalf("ListRemoteRefs after prune: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("ListRemoteRefs after prune = %v, want empty", refs)
	}

	// The anchor must actually be gone, not just hidden from the listing.
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_log('remotes/origin/main')").Scan(&anchoredCommits); err == nil {
		t.Fatalf("dolt_log('remotes/origin/main') still resolves after prune (%d commits)", anchoredCommits)
	}

	// Prune must not touch tags.
	tags, err = store.ListTags(ctx)
	if err != nil {
		t.Fatalf("ListTags after prune: %v", err)
	}
	if len(tags) != 1 || tags[0] != "prune-test-tag" {
		t.Fatalf("ListTags after prune = %v, want [prune-test-tag]", tags)
	}

	if err := store.DoltGC(ctx); err != nil {
		t.Fatalf("DoltGC after prune: %v", err)
	}

	// Idempotence: pruning an already-clean workspace is a quiet no-op.
	pruned, err = store.PruneRemoteRefs(ctx)
	if err != nil {
		t.Fatalf("PruneRemoteRefs (second run): %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("PruneRemoteRefs second run = %v, want empty", pruned)
	}
}
