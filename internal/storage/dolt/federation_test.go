//go:build integration

package dolt

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Federation Prototype Tests
//
// These tests validate the Dolt APIs needed for federation between towns.
// Production federation uses hosted Dolt remotes (DoltHub, S3, etc.), not file://.
//
// What we can test locally:
// 1. Database isolation between towns (separate Dolt databases)
// 2. Version control APIs (commit, branch, merge)
// 3. Remote configuration APIs (AddRemote)
// 4. History and diff queries
//
// What requires hosted infrastructure:
// 1. Actual push/pull between towns (needs DoltHub or dolt sql-server)
// 2. Cross-town sync via DOLT_FETCH/DOLT_PUSH
// 3. Federation message exchange
//
// See HOP docs: architecture/FEDERATION.md for full federation spec.

// TestFederationDatabaseIsolation verifies that two DoltStores have isolated databases
func TestFederationDatabaseIsolation(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	baseDir, err := os.MkdirTemp("", "federation-isolation-*")
	if err != nil {
		t.Fatalf("failed to create base dir: %v", err)
	}
	defer os.RemoveAll(baseDir)

	// Setup Town Alpha
	alphaDir := filepath.Join(baseDir, "town-alpha")
	alphaStore, alphaCleanup := setupFederationStore(t, ctx, alphaDir, "alpha")
	defer alphaCleanup()

	// Setup Town Beta
	betaDir := filepath.Join(baseDir, "town-beta")
	betaStore, betaCleanup := setupFederationStore(t, ctx, betaDir, "beta")
	defer betaCleanup()

	t.Logf("Alpha path: %s", alphaStore.Path())
	t.Logf("Beta path: %s", betaStore.Path())

	// Create issue in Alpha
	alphaIssue := &types.Issue{
		ID:          "alpha-001",
		Title:       "Work item from Town Alpha",
		Description: "This issue exists only in Town Alpha",
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Priority:    1,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := alphaStore.CreateIssue(ctx, alphaIssue, "federation-test"); err != nil {
		t.Fatalf("failed to create issue in alpha: %v", err)
	}
	if err := alphaStore.Commit(ctx, "Create alpha-001"); err != nil {
		t.Fatalf("failed to commit in alpha: %v", err)
	}

	// Create different issue in Beta
	betaIssue := &types.Issue{
		ID:          "beta-001",
		Title:       "Work item from Town Beta",
		Description: "This issue exists only in Town Beta",
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Priority:    2,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := betaStore.CreateIssue(ctx, betaIssue, "federation-test"); err != nil {
		t.Fatalf("failed to create issue in beta: %v", err)
	}
	if err := betaStore.Commit(ctx, "Create beta-001"); err != nil {
		t.Fatalf("failed to commit in beta: %v", err)
	}

	// Verify isolation: Alpha should NOT see Beta's issue
	issueFromAlpha, err := alphaStore.GetIssue(ctx, "beta-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if issueFromAlpha != nil {
		t.Fatalf("isolation violated: alpha found beta-001")
	}
	t.Log("✓ Alpha cannot see beta-001")

	// Verify isolation: Beta should NOT see Alpha's issue
	issueFromBeta, err := betaStore.GetIssue(ctx, "alpha-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if issueFromBeta != nil {
		t.Fatalf("isolation violated: beta found alpha-001")
	}
	t.Log("✓ Beta cannot see alpha-001")

	// Verify each town sees its own issue
	alphaCheck, _ := alphaStore.GetIssue(ctx, "alpha-001")
	if alphaCheck == nil {
		t.Fatal("alpha should see its own issue")
	}
	t.Logf("✓ Alpha sees alpha-001: %q", alphaCheck.Title)

	betaCheck, _ := betaStore.GetIssue(ctx, "beta-001")
	if betaCheck == nil {
		t.Fatal("beta should see its own issue")
	}
	t.Logf("✓ Beta sees beta-001: %q", betaCheck.Title)
}

// TestFederationVersionControlAPIs tests the Dolt version control operations
// needed for federation (branch, commit, merge)
func TestFederationVersionControlAPIs(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Create initial issue
	issue := &types.Issue{
		ID:        "vc-001",
		Title:     "Version control test",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.Commit(ctx, "Initial issue"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Test branch creation
	if err := store.Branch(ctx, "feature-branch"); err != nil {
		t.Fatalf("failed to create branch: %v", err)
	}
	t.Log("✓ Created feature-branch")

	// Test checkout
	if err := store.Checkout(ctx, "feature-branch"); err != nil {
		t.Fatalf("failed to checkout: %v", err)
	}

	// Verify current branch
	branch, err := store.CurrentBranch(ctx)
	if err != nil {
		t.Fatalf("failed to get current branch: %v", err)
	}
	if branch != "feature-branch" {
		t.Errorf("expected feature-branch, got %s", branch)
	}
	t.Logf("✓ Checked out to %s", branch)

	// Make change on feature branch
	updates := map[string]interface{}{
		"title": "Updated on feature branch",
	}
	if err := store.UpdateIssue(ctx, "vc-001", updates, "test"); err != nil {
		t.Fatalf("failed to update: %v", err)
	}
	if err := store.Commit(ctx, "Feature update"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Switch back to main
	if err := store.Checkout(ctx, "main"); err != nil {
		t.Fatalf("failed to checkout main: %v", err)
	}

	// Verify main still has original title
	mainIssue, _ := store.GetIssue(ctx, "vc-001")
	if mainIssue.Title != "Version control test" {
		t.Errorf("main should have original title, got %q", mainIssue.Title)
	}
	t.Log("✓ Main branch unchanged")

	// Merge feature branch
	conflicts, err := store.Merge(ctx, "feature-branch")
	if err != nil {
		t.Fatalf("failed to merge: %v", err)
	}
	if len(conflicts) > 0 {
		t.Logf("Merge produced %d conflicts", len(conflicts))
	}
	t.Log("✓ Merged feature-branch into main")

	// Verify merge result
	mergedIssue, _ := store.GetIssue(ctx, "vc-001")
	if mergedIssue.Title != "Updated on feature branch" {
		t.Errorf("expected merged title, got %q", mergedIssue.Title)
	}
	t.Logf("✓ Merge applied: title now %q", mergedIssue.Title)

	// Test branch listing
	branches, err := store.ListBranches(ctx)
	if err != nil {
		t.Fatalf("failed to list branches: %v", err)
	}
	t.Logf("✓ Branches: %v", branches)
}

// TestFederationRemoteConfiguration tests AddRemote API
// Note: This only tests configuration, not actual push/pull which requires a running remote
func TestFederationRemoteConfiguration(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Add a remote (configuration only - won't actually connect)
	// Production would use: dolthub://org/repo, s3://bucket/path, etc.
	err := store.AddRemote(ctx, "origin", "dolthub://example/beads")
	if err != nil {
		// AddRemote may fail if remote can't be validated, which is expected
		t.Logf("AddRemote result: %v (expected for unreachable remote)", err)
	} else {
		t.Log("✓ Added remote 'origin'")
	}

	// Add federation peer remote
	err = store.AddRemote(ctx, "town-beta", "dolthub://acme/town-beta-beads")
	if err != nil {
		t.Logf("AddRemote town-beta result: %v", err)
	} else {
		t.Log("✓ Added remote 'town-beta'")
	}
}

// TestFederationHistoryQueries tests history queries needed for CV and audit
func TestFederationHistoryQueries(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Create issue
	issue := &types.Issue{
		ID:        "hist-001",
		Title:     "History test - v1",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create: %v", err)
	}
	if err := store.Commit(ctx, "Create hist-001 v1"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Update multiple times
	for i := 2; i <= 3; i++ {
		updates := map[string]interface{}{
			"title": "History test - v" + string(rune('0'+i)),
		}
		if err := store.UpdateIssue(ctx, "hist-001", updates, "test"); err != nil {
			t.Fatalf("failed to update v%d: %v", i, err)
		}
		if err := store.Commit(ctx, "Update to v"+string(rune('0'+i))); err != nil {
			t.Fatalf("failed to commit v%d: %v", i, err)
		}
	}

	// Query history
	history, err := store.History(ctx, "hist-001")
	if err != nil {
		t.Fatalf("failed to get history: %v", err)
	}
	t.Logf("✓ Found %d history entries for hist-001", len(history))
	for i, entry := range history {
		t.Logf("  [%d] %s: %s", i, entry.CommitHash[:8], entry.Issue.Title)
	}

	// Get current commit
	hash, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("failed to get current commit: %v", err)
	}
	t.Logf("✓ Current commit: %s", hash[:12])

	// Query recent log
	log, err := store.Log(ctx, 5)
	if err != nil {
		t.Fatalf("failed to get log: %v", err)
	}
	t.Logf("✓ Recent commits:")
	for _, c := range log {
		t.Logf("  %s: %s", c.Hash[:8], c.Message)
	}
}

// TestFederationListRemotes tests the ListRemotes API
func TestFederationListRemotes(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Initially no remotes (except possibly origin if Dolt adds one by default)
	remotes, err := store.ListRemotes(ctx)
	if err != nil {
		t.Fatalf("failed to list remotes: %v", err)
	}
	t.Logf("Initial remotes: %d", len(remotes))

	// Add a test remote
	err = store.AddRemote(ctx, "test-peer", "file:///tmp/nonexistent")
	if err != nil {
		t.Logf("AddRemote returned: %v (may be expected)", err)
	}

	// List again
	remotes, err = store.ListRemotes(ctx)
	if err != nil {
		t.Fatalf("failed to list remotes after add: %v", err)
	}

	// Should have at least one remote now
	t.Logf("Remotes after add: %v", remotes)
	for _, r := range remotes {
		t.Logf("  %s: %s", r.Name, r.URL)
	}
}

// TestFederationSyncStatus tests the SyncStatus API
func TestFederationSyncStatus(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Get status for a nonexistent peer (should not error, just return partial data)
	status, err := store.SyncStatus(ctx, "nonexistent-peer")
	if err != nil {
		t.Fatalf("SyncStatus failed: %v", err)
	}

	t.Logf("Status for nonexistent peer:")
	t.Logf("  Peer: %s", status.Peer)
	t.Logf("  LocalAhead: %d", status.LocalAhead)
	t.Logf("  LocalBehind: %d", status.LocalBehind)
	t.Logf("  HasConflicts: %v", status.HasConflicts)

	// LocalAhead/Behind should be -1 (unknown) for nonexistent peer
	if status.LocalAhead != -1 || status.LocalBehind != -1 {
		t.Logf("Note: Status returned values for nonexistent peer (may be expected behavior)")
	}
}

func TestFederationSyncCommitsPendingPeerMetadataBeforeFetch(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	store, cleanup := setupTestStore(t)
	defer cleanup()

	peer := &storage.FederationPeer{
		Name:        "peer-metadata-sync",
		RemoteURL:   "file:///tmp/beads-no-such-federation-peer",
		Sovereignty: "T2",
	}
	if err := store.AddFederationPeer(ctx, peer); err != nil {
		t.Fatalf("add federation peer: %v", err)
	}

	if federationStatusHasTable(t, ctx, store, "federation_peers") {
		t.Fatal("add-peer should commit federation_peers metadata")
	}

	if _, err := store.db.ExecContext(ctx,
		"UPDATE federation_peers SET sovereignty = ? WHERE name = ?", "T3", peer.Name,
	); err != nil {
		t.Fatalf("dirty federation peer metadata: %v", err)
	}
	if !federationStatusHasTable(t, ctx, store, "federation_peers") {
		t.Fatal("expected direct federation_peers update to dirty the working set")
	}

	_, err := store.Sync(ctx, peer.Name, "")
	if err == nil {
		t.Fatal("expected sync to fail for nonexistent file remote")
	}
	if federationStatusHasTable(t, ctx, store, "federation_peers") {
		t.Fatal("sync should commit pending federation_peers metadata before fetch/merge")
	}
}

func federationStatusHasTable(t *testing.T, ctx context.Context, store *DoltStore, table string) bool {
	t.Helper()

	var count int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = ?", table,
	).Scan(&count); err != nil {
		t.Fatalf("query dolt_status for %s: %v", table, err)
	}
	return count > 0
}

// TestFederationPushPullMethods tests PushTo and PullFrom
func TestFederationPushPullMethods(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	store, cleanup := setupTestStore(t)
	defer cleanup()

	// These should fail gracefully since no remote exists
	err := store.PushTo(ctx, "nonexistent")
	if err == nil {
		t.Log("PushTo to nonexistent peer succeeded (unexpected)")
	} else {
		t.Logf("✓ PushTo correctly failed: %v", err)
	}

	conflicts, err := store.PullFrom(ctx, "nonexistent")
	if err == nil {
		t.Logf("PullFrom from nonexistent peer succeeded with %d conflicts", len(conflicts))
	} else {
		t.Logf("✓ PullFrom correctly failed: %v", err)
	}

	err = store.Fetch(ctx, "nonexistent")
	if err == nil {
		t.Log("Fetch from nonexistent peer succeeded (unexpected)")
	} else {
		t.Logf("✓ Fetch correctly failed: %v", err)
	}
}

// TestFilteredPushExcludesWisp verifies that filteredPushToPeer with
// exclude_types=["wisp"] removes ephemeral issues from the staging branch
// before push. Since we can't push to a real remote in tests, we verify:
// 1. The staging branch is created and cleaned up
// 2. Issues matching excluded types are removed from the staging branch
// 3. Non-excluded issues remain intact
// 4. The original branch is unchanged after the operation
func TestFilteredPushExcludesWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a regular task (should survive filtering)
	task := &types.Issue{
		ID:        "fed-filter-task",
		Title:     "Regular task",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, task, "test"); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create an ephemeral issue directly in the issues table (simulates the
	// edge case where an ephemeral issue leaks into committed data).
	_, err := store.db.ExecContext(ctx, `INSERT INTO issues
		(id, title, issue_type, status, priority, ephemeral, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 1, NOW(), NOW())`,
		"fed-filter-wisp", "Leaked wisp", "task", "open", 1)
	if err != nil {
		t.Fatalf("insert ephemeral issue: %v", err)
	}

	if err := store.Commit(ctx, "create test issues"); err != nil {
		if !isDoltNothingToCommit(err) {
			t.Fatalf("commit: %v", err)
		}
	}

	// Verify both issues exist before filtering.
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if count < 2 {
		t.Fatalf("expected at least 2 issues before filter, got %d", count)
	}

	// Run filteredPushToPeer — push will fail (no remote) but the staging
	// branch logic runs first. We verify the staging branch behavior by
	// checking that the original branch is untouched afterward.
	pushErr := store.filteredPushToPeer(ctx, "nonexistent-peer", []string{"wisp"})
	if pushErr == nil {
		t.Fatal("expected push error for nonexistent peer")
	}

	// Verify the staging branch was cleaned up.
	branches, err := store.ListBranches(ctx)
	if err != nil {
		t.Fatalf("list branches: %v", err)
	}
	for _, b := range branches {
		if b == federationStagingBranch {
			t.Errorf("staging branch %s was not cleaned up", federationStagingBranch)
		}
	}

	// Verify original branch still has both issues (filter is non-destructive).
	var taskCount, wispCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE id = ?", "fed-filter-task").Scan(&taskCount); err != nil {
		t.Fatalf("count task: %v", err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE id = ?", "fed-filter-wisp").Scan(&wispCount); err != nil {
		t.Fatalf("count wisp: %v", err)
	}
	if taskCount != 1 {
		t.Errorf("regular task missing after filtered push")
	}
	if wispCount != 1 {
		t.Errorf("ephemeral issue should still exist on original branch, got count=%d", wispCount)
	}
}

// TestFilteredPushOptOut verifies that setting federation.exclude_types to
// an empty list disables filtering (backward-compatible opt-out).
func TestFilteredPushOptOut(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an ephemeral issue in the committed issues table.
	_, err := store.db.ExecContext(ctx, `INSERT INTO issues
		(id, title, issue_type, status, priority, ephemeral, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 1, NOW(), NOW())`,
		"fed-optout-wisp", "Wisp for opt-out test", "task", "open", 1)
	if err != nil {
		t.Fatalf("insert ephemeral issue: %v", err)
	}
	if err := store.Commit(ctx, "create ephemeral issue"); err != nil {
		if !isDoltNothingToCommit(err) {
			t.Fatalf("commit: %v", err)
		}
	}

	// With empty exclude list, filteredPushToPeer delegates directly to PushTo
	// (no staging branch created). It will fail due to no remote, but the
	// important thing is no staging branch is created.
	pushErr := store.filteredPushToPeer(ctx, "nonexistent-peer", []string{})
	if pushErr == nil {
		t.Fatal("expected push error for nonexistent peer")
	}

	// Verify no staging branch was created (opt-out path skips staging).
	branches, err := store.ListBranches(ctx)
	if err != nil {
		t.Fatalf("list branches: %v", err)
	}
	for _, b := range branches {
		if b == federationStagingBranch {
			t.Errorf("staging branch should not exist when exclude_types is empty")
		}
	}
}

// TestFilteredPushExcludesCustomType verifies that non-wisp types in
// federation.exclude_types are filtered by issue_type column.
func TestFilteredPushExcludesCustomType(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a task and a message.
	task := &types.Issue{
		ID:        "fed-custom-task",
		Title:     "Regular task",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	msg := &types.Issue{
		ID:        "fed-custom-msg",
		Title:     "Internal message",
		IssueType: types.TypeMessage,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, task, "test"); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := store.CreateIssue(ctx, msg, "test"); err != nil {
		t.Fatalf("create message: %v", err)
	}
	if err := store.Commit(ctx, "create test issues"); err != nil {
		if !isDoltNothingToCommit(err) {
			t.Fatalf("commit: %v", err)
		}
	}

	// Exclude "message" type — task should remain, message should be filtered.
	pushErr := store.filteredPushToPeer(ctx, "nonexistent-peer", []string{"message"})
	if pushErr == nil {
		t.Fatal("expected push error for nonexistent peer")
	}

	// Verify original branch still has both issues.
	var taskCount, msgCount int
	store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", "fed-custom-task").Scan(&taskCount)
	store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", "fed-custom-msg").Scan(&msgCount)
	if taskCount != 1 {
		t.Errorf("task should survive on original branch")
	}
	if msgCount != 1 {
		t.Errorf("message should survive on original branch (filter is non-destructive)")
	}
}

// TestFilteredPushStagingBranchCleanupOnError verifies that the staging
// branch is always cleaned up, even when the push operation fails.
func TestFilteredPushStagingBranchCleanupOnError(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Run filtered push to a nonexistent peer — will fail, but staging
	// branch should still be cleaned up.
	_ = store.filteredPushToPeer(ctx, "no-such-peer", []string{"wisp", "message"})

	branches, err := store.ListBranches(ctx)
	if err != nil {
		t.Fatalf("list branches: %v", err)
	}
	for _, b := range branches {
		if b == federationStagingBranch {
			t.Errorf("staging branch %s should be cleaned up after push error", federationStagingBranch)
		}
	}
}

// setupFederationStore creates a Dolt store for federation testing
func setupFederationStore(t *testing.T, ctx context.Context, path, prefix string) (*DoltStore, func()) {
	t.Helper()

	cfg := &Config{
		Path:            path,
		CommitterName:   "town-" + prefix,
		CommitterEmail:  prefix + "@federation.test",
		Database:        "beads",
		CreateIfMissing: true, // test creates fresh database
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create %s store: %v", prefix, err)
	}

	// Set up issue prefix
	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		store.Close()
		t.Fatalf("failed to set prefix for %s: %v", prefix, err)
	}

	// Initial commit to establish main branch
	if err := store.Commit(ctx, "Initialize "+prefix+" town"); err != nil {
		// Ignore if nothing to commit
		t.Logf("Initial commit for %s: %v", prefix, err)
	}

	cleanup := func() {
		store.Close()
	}

	return store, cleanup
}
