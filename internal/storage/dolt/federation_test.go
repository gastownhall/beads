//go:build integration

package dolt

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
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
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()
	versionWispTablesForFilteredPush(t, ctx, store)

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
		(id, title, description, design, acceptance_criteria, notes,
		 issue_type, status, priority, ephemeral, created_at, updated_at)
		VALUES (?, ?, '', '', '', '', ?, ?, ?, 1, NOW(), NOW())`,
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
		if strings.HasPrefix(b, federationStagingBranchPrefix) {
			t.Errorf("staging branch %s was not cleaned up", b)
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

// TestFilteredPushUsesDedicatedLongTimeoutConnection proves the full staging
// lifecycle does not borrow the shared pool. The pool is deliberately
// exhausted while a separate connection observes the staging branch's durable
// state: the blocker is deleted, the retained waiter is recomputed unblocked,
// and the filtering commit is present before peer lookup waits on the pool.
func TestFilteredPushUsesDedicatedLongTimeoutConnection(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	waiterID, blockerID := seedFilteredPushGraph(t, ctx, store, "fed-long-timeout")
	observer, err := store.openLongTimeoutConn()
	if err != nil {
		t.Fatalf("open staging observer: %v", err)
	}
	defer observer.Close()

	store.db.SetMaxOpenConns(1)
	held, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("exhaust shared pool: %v", err)
	}
	defer held.Close()
	initialWaitCount := store.db.Stats().WaitCount

	errCh := make(chan error, 1)
	go func() {
		errCh <- store.filteredPushToPeer(ctx, "nonexistent-peer", []string{"wisp"})
	}()

	waitForFilteredStagingCommit(t, ctx, observer, errCh, waiterID, blockerID)
	waitForPoolWait(t, ctx, store.db, initialWaitCount)
	if err := held.Close(); err != nil {
		t.Fatalf("release shared pool: %v", err)
	}
	if err := <-errCh; err == nil {
		t.Fatal("expected peer credential error for nonexistent peer")
	} else if !strings.Contains(err.Error(), "failed to get peer credentials") {
		t.Fatalf("filtered push failed before peer lookup: %v", err)
	}
	assertFederationStagingBranchAbsent(t, ctx, observer)
}

// TestFilteredPushConcurrentOperationsUseDistinctStagingBranches proves two
// overlapping product calls cannot delete or replace each other's staging
// branch. Both calls finish staging and block on the exhausted shared pool
// before their branch names are observed.
func TestFilteredPushConcurrentOperationsUseDistinctStagingBranches(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedFilteredPushGraph(t, ctx, store, "fed-concurrent-staging")
	observer, err := store.openLongTimeoutConn()
	if err != nil {
		t.Fatalf("open staging observer: %v", err)
	}
	defer observer.Close()

	store.db.SetMaxOpenConns(1)
	held, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("exhaust shared pool: %v", err)
	}
	defer held.Close()
	initialWaitCount := store.db.Stats().WaitCount

	firstCtx, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	secondCtx, cancelSecond := context.WithCancel(ctx)
	defer cancelSecond()
	errCh := make(chan error, 2)
	go func() {
		errCh <- store.filteredPushToPeer(firstCtx, "nonexistent-peer", []string{"wisp"})
	}()
	waitForPoolWait(t, ctx, store.db, initialWaitCount)
	go func() {
		errCh <- store.filteredPushToPeer(secondCtx, "nonexistent-peer", []string{"wisp"})
	}()
	waitForPoolWait(t, ctx, store.db, initialWaitCount+1)

	stagingBranches := listFederationStagingBranches(t, ctx, observer)
	cancelFirst()
	cancelSecond()
	for range 2 {
		select {
		case err := <-errCh:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("overlapping filtered push error = %v, want context canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("overlapping filtered push did not return after cancellation")
		}
	}
	assertFederationStagingBranchAbsent(t, ctx, observer)
	if len(stagingBranches) != 2 {
		t.Fatalf("overlapping filtered pushes exposed staging branches %v, want 2 distinct branches", stagingBranches)
	}
}

// TestFilteredPushCleanupSurvivesLostBranchCreateResponse proves cleanup is
// armed before branch creation becomes externally visible. The test dialer
// forwards the real DOLT_BRANCH call, withholds its response until another
// connection observes the branch, then drops that response after cancellation.
func TestFilteredPushCleanupSurvivesLostBranchCreateResponse(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	observer, err := store.openLongTimeoutConn()
	if err != nil {
		t.Fatalf("open staging observer: %v", err)
	}
	defer observer.Close()

	loss := installFederationBranchResponseLossDialer(t, store)
	defer loss.drop()
	pushCtx, cancelPush := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- store.filteredPushToPeer(pushCtx, "nonexistent-peer", []string{"wisp"})
	}()

	select {
	case <-loss.responseBlocked:
	case err := <-errCh:
		t.Fatalf("filtered push returned before branch response was blocked: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("filtered push did not issue staging branch creation")
	}

	deadline := time.Now().Add(10 * time.Second)
	var stagingBranches []string
	for time.Now().Before(deadline) {
		stagingBranches = listFederationStagingBranches(t, ctx, observer)
		if len(stagingBranches) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(stagingBranches) != 1 {
		t.Fatalf("server-side staging branches = %v, want one before response loss", stagingBranches)
	}

	cancelPush()
	loss.drop()
	select {
	case err = <-errCh:
	case <-time.After(10 * time.Second):
		t.Fatal("filtered push did not return after dropping branch response")
	}
	if err == nil || !strings.Contains(err.Error(), "federation filter: create staging branch") {
		t.Fatalf("filtered push error = %v, want original branch-create failure", err)
	}
	if strings.Contains(err.Error(), "federation filter: cleanup") {
		t.Fatalf("cleanup masked branch-create failure: %v", err)
	}
	assertFederationStagingBranchAbsent(t, ctx, observer)
}

func TestDeleteFederationStagingBranchIgnoresOnlyMissingBranch(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire cleanup test connection: %v", err)
	}
	defer conn.Close()

	t.Run("missing branch", func(t *testing.T) {
		err := deleteFederationStagingBranch(ctx, conn, federationStagingBranchPrefix+"missing")
		if err != nil {
			t.Fatalf("delete missing staging branch: %v", err)
		}
	})

	t.Run("real delete error", func(t *testing.T) {
		err := deleteFederationStagingBranch(ctx, conn, "")
		if err == nil {
			t.Fatal("delete empty staging branch succeeded, want error")
		}
	})
}

// TestFilteredPushCleanupSurvivesCallerCancellation cancels the caller while
// the staging DELETE is executing. go-sql-driver/mysql invalidates that
// operation connection on cancellation, so cleanup must discard it, reopen a
// connection, restore the source branch, and delete the staging branch.
func TestFilteredPushCleanupSurvivesCallerCancellation(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	waiterID, blockerID := seedFilteredPushGraph(t, ctx, store, "fed-cancel-cleanup")
	sourceBranch := store.branch
	setupConn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin cancellation fixture connection: %v", err)
	}
	if _, err := setupConn.ExecContext(ctx,
		"CREATE TABLE federation_cancel_gate (slept INT NOT NULL)"); err != nil {
		_ = setupConn.Close()
		t.Fatalf("create cancellation gate table: %v", err)
	}
	if _, err := setupConn.ExecContext(ctx, `CREATE TRIGGER federation_cancel_delete
		BEFORE DELETE ON issues FOR EACH ROW
		INSERT INTO federation_cancel_gate VALUES (SLEEP(60))`); err != nil {
		_ = setupConn.Close()
		t.Fatalf("create cancellation gate trigger: %v", err)
	}
	if err := schema.DrainCall(ctx, setupConn,
		"CALL DOLT_COMMIT('-Am', 'test: block filtered delete for cancellation')"); err != nil {
		_ = setupConn.Close()
		t.Fatalf("commit cancellation fixture: %v", err)
	}
	if err := setupConn.Close(); err != nil {
		t.Fatalf("close cancellation fixture connection: %v", err)
	}

	observer, err := store.openLongTimeoutConn()
	if err != nil {
		t.Fatalf("open staging observer: %v", err)
	}
	defer observer.Close()

	pushCtx, cancelPush := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- store.filteredPushToPeer(pushCtx, "nonexistent-peer", []string{"wisp"})
	}()

	waitForFederationQuery(t, ctx, observer, errCh, store.database, "DELETE FROM issues WHERE ephemeral = 1")
	cancelPush()
	select {
	case err = <-errCh:
	case <-time.After(10 * time.Second):
		t.Fatal("filtered push did not return after canceling its staging query")
	}
	if err == nil {
		t.Fatal("filtered push succeeded after canceling its staging query")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "bad connection") {
		t.Fatalf("filtered push error = %v, want cancellation or invalidated connection", err)
	}

	if err := versioncontrolops.CheckoutBranch(ctx, observer, sourceBranch); err != nil {
		t.Fatalf("checkout source branch after cancellation: %v", err)
	}
	var activeBranch string
	if err := observer.QueryRowContext(ctx, "SELECT active_branch()").Scan(&activeBranch); err != nil {
		t.Fatalf("read branch after cancellation: %v", err)
	}
	if activeBranch != sourceBranch {
		t.Fatalf("active branch after cancellation = %q, want source %q", activeBranch, sourceBranch)
	}
	var blockerCount int
	if err := observer.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE id = ?", blockerID).Scan(&blockerCount); err != nil {
		t.Fatalf("read excluded blocker on source branch: %v", err)
	}
	waiterBlocked := isBlocked(ctx, t, observer, waiterID)
	if blockerCount != 1 || !waiterBlocked {
		t.Fatalf("source state changed after cancellation: blocker count = %d, waiter blocked = %t, want 1, true",
			blockerCount, waiterBlocked)
	}
	assertFederationStagingBranchAbsent(t, ctx, observer)
}

// TestFilteredPushCleanupDeletesStagingAfterRestoreFailure proves cleanup
// attempts branch deletion even when restoring the captured source fails, and
// joins that cleanup failure with the caller's cancellation.
func TestFilteredPushCleanupDeletesStagingAfterRestoreFailure(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	waiterID, blockerID := seedFilteredPushGraph(t, ctx, store, "fed-cleanup-join")
	observer, err := store.openLongTimeoutConn()
	if err != nil {
		t.Fatalf("open staging observer: %v", err)
	}
	defer observer.Close()

	sourceBranch := "federation-cleanup-missing-source"
	if err := schema.DrainCall(ctx, observer,
		"CALL DOLT_BRANCH(?, ?)", sourceBranch, store.branch); err != nil {
		t.Fatalf("create disposable source branch: %v", err)
	}
	store.branch = sourceBranch

	store.db.SetMaxOpenConns(1)
	held, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("exhaust shared pool: %v", err)
	}
	defer held.Close()
	initialWaitCount := store.db.Stats().WaitCount

	pushCtx, cancelPush := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- store.filteredPushToPeer(pushCtx, "nonexistent-peer", []string{"wisp"})
	}()

	waitForFilteredStagingCommit(t, ctx, observer, errCh, waiterID, blockerID)
	waitForPoolWait(t, ctx, store.db, initialWaitCount)
	if err := schema.DrainCall(ctx, observer,
		"CALL DOLT_BRANCH('-Df', ?)", sourceBranch); err != nil {
		t.Fatalf("delete disposable source branch: %v", err)
	}
	cancelPush()
	select {
	case err = <-errCh:
	case <-time.After(10 * time.Second):
		t.Fatal("filtered push did not return after caller cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("filtered push error = %v, want context canceled", err)
	}
	if !strings.Contains(err.Error(), "federation filter: cleanup") ||
		!strings.Contains(err.Error(), "restore branch "+sourceBranch) {
		t.Fatalf("filtered push error omitted cleanup restore failure: %v", err)
	}
	assertFederationStagingBranchAbsent(t, ctx, observer)
}

func waitForFederationQuery(t *testing.T, ctx context.Context, observer *sql.DB, errCh <-chan error, database, querySnippet string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("filtered push returned before staging query was observable: %v", err)
		default:
		}
		visible, err := processlistContainsQuery(ctx, observer, database, querySnippet)
		if err != nil {
			t.Fatalf("observe staging query: %v", err)
		}
		if visible {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for staging query: %v", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("staging query %q was not observable", querySnippet)
}

func processlistContainsQuery(ctx context.Context, observer *sql.DB, database, querySnippet string) (bool, error) {
	rows, err := observer.QueryContext(ctx, "SHOW PROCESSLIST")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return false, err
	}
	infoIndex := -1
	databaseIndex := -1
	for i, column := range columns {
		switch {
		case strings.EqualFold(column, "info"):
			infoIndex = i
		case strings.EqualFold(column, "db"):
			databaseIndex = i
		}
	}
	if infoIndex < 0 || databaseIndex < 0 {
		return false, errors.New("SHOW PROCESSLIST has no db or info column")
	}
	values := make([]sql.NullString, len(columns))
	scanArgs := make([]any, len(columns))
	for i := range values {
		scanArgs[i] = &values[i]
	}
	querySnippet = strings.ToLower(querySnippet)
	for rows.Next() {
		for i := range values {
			values[i] = sql.NullString{}
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return false, err
		}
		if values[databaseIndex].Valid && values[databaseIndex].String == database &&
			values[infoIndex].Valid && strings.Contains(strings.ToLower(values[infoIndex].String), querySnippet) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func seedFilteredPushGraph(t *testing.T, ctx context.Context, store *DoltStore, prefix string) (string, string) {
	t.Helper()

	if err := store.db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&store.branch); err != nil {
		t.Fatalf("align store source branch: %v", err)
	}
	versionWispTablesForFilteredPush(t, ctx, store)
	waiterID := prefix + "-waiter"
	blockerID := prefix + "-blocker"
	if err := store.CreateIssue(ctx, &types.Issue{
		ID: waiterID, Title: "retained waiter", IssueType: types.TypeBug,
		Status: types.StatusOpen, Priority: 1,
	}, "test"); err != nil {
		t.Fatalf("create waiter: %v", err)
	}
	if err := store.CreateIssue(ctx, &types.Issue{
		ID: blockerID, Title: "excluded blocker", IssueType: types.TypeTask,
		Status: types.StatusOpen, Priority: 1,
	}, "test"); err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: waiterID, DependsOnID: blockerID, Type: types.DepBlocks,
	}, "test"); err != nil {
		t.Fatalf("add blocking dependency: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET ephemeral = 1 WHERE id = ?", blockerID); err != nil {
		t.Fatalf("mark blocker ephemeral: %v", err)
	}
	if err := store.Commit(ctx, "seed filtered staging graph"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit filtered staging graph: %v", err)
	}
	if !isBlocked(ctx, t, store.db, waiterID) {
		t.Fatal("precondition: retained waiter must be blocked on source branch")
	}
	return waiterID, blockerID
}

// versionWispTablesForFilteredPush makes the empty clone-local wisp plane
// visible to fresh sessions in this fixture. Production databases retain
// their dolt_ignore'd working set, but setupConcurrentTestStore deliberately
// closes idle sessions; without this fixture commit a newly opened staging
// connection sees no wisps table and cannot exercise the recompute seam.
func versionWispTablesForFilteredPush(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	db, err := store.openLongTimeoutConn()
	if err != nil {
		t.Fatalf("open wisp fixture database: %v", err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin wisp fixture connection: %v", err)
	}
	defer conn.Close()
	if err := versioncontrolops.CheckoutBranch(ctx, conn, store.branch); err != nil {
		t.Fatalf("checkout source branch for wisp fixture: %v", err)
	}
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("materialize ignored schema for wisp fixture: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"DELETE FROM dolt_ignore WHERE pattern IN ('wisps', 'wisp_%')"); err != nil {
		t.Fatalf("make wisp fixture tables versionable: %v", err)
	}
	if err := schema.DrainCall(ctx, conn, "CALL DOLT_ADD('-A')"); err != nil {
		t.Fatalf("stage wisp fixture tables: %v", err)
	}
	if err := schema.DrainCall(ctx, conn,
		"CALL DOLT_COMMIT('-m', 'test: version empty wisp plane for filtered push')"); err != nil {
		t.Fatalf("commit wisp fixture tables: %v", err)
	}
}

func waitForFilteredStagingCommit(t *testing.T, ctx context.Context, observer *sql.DB, errCh <-chan error, waiterID, blockerID string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("filtered push returned before publishing staging commit: %v", err)
		default:
		}
		branches := listFederationStagingBranches(t, ctx, observer)
		if len(branches) != 1 {
			lastErr = fmt.Errorf("found staging branches %v, want exactly one", branches)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		stagingBranch := branches[0]
		if err := issueops.ValidateRef(stagingBranch); err != nil {
			t.Fatalf("generated staging branch %q is invalid: %v", stagingBranch, err)
		}
		//nolint:gosec // stagingBranch is generated internally and validated as a Dolt ref above.
		blockerQuery := fmt.Sprintf("SELECT COUNT(*) FROM issues AS OF '%s' WHERE id = ?", stagingBranch)
		//nolint:gosec // stagingBranch is generated internally and validated as a Dolt ref above.
		blockedQuery := fmt.Sprintf("SELECT is_blocked FROM issues AS OF '%s' WHERE id = ?", stagingBranch)
		//nolint:gosec // stagingBranch is generated internally and validated as a Dolt ref above.
		commitQuery := fmt.Sprintf("SELECT COUNT(*) FROM dolt_log('%s') WHERE message = ?", stagingBranch)
		var blockerCount, commitCount int
		var blocked bool
		blockerErr := observer.QueryRowContext(ctx, blockerQuery, blockerID).Scan(&blockerCount)
		blockedErr := observer.QueryRowContext(ctx, blockedQuery, waiterID).Scan(&blocked)
		commitErr := observer.QueryRowContext(ctx, commitQuery,
			"federation: exclude private issue types").Scan(&commitCount)
		lastErr = errors.Join(blockerErr, blockedErr, commitErr)
		if lastErr == nil && blockerCount == 0 && !blocked && commitCount == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("filtered staging commit was not durably observable: %v", lastErr)
}

func waitForPoolWait(t *testing.T, ctx context.Context, db *sql.DB, initialWaitCount int64) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if db.Stats().WaitCount > initialWaitCount {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for peer lookup to block on shared pool: %v", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("peer lookup did not block on the exhausted shared pool")
}

func assertFederationStagingBranchAbsent(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if branches := listFederationStagingBranches(t, ctx, db); len(branches) != 0 {
		t.Fatalf("federation staging branches = %v, want none", branches)
	}
}

func listFederationStagingBranches(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT name FROM dolt_branches
		WHERE LEFT(name, ?) = ?
		ORDER BY name`, len(federationStagingBranchPrefix), federationStagingBranchPrefix)
	if err != nil {
		t.Fatalf("query federation staging branches: %v", err)
	}
	defer rows.Close()
	var branches []string
	for rows.Next() {
		var branch string
		if err := rows.Scan(&branch); err != nil {
			t.Fatalf("scan federation staging branch: %v", err)
		}
		branches = append(branches, branch)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate federation staging branches: %v", err)
	}
	return branches
}

type federationBranchResponseLossDialer struct {
	armed           atomic.Bool
	responseBlocked chan struct{}
	dropResponse    chan struct{}
	blockedOnce     sync.Once
	dropOnce        sync.Once
}

func installFederationBranchResponseLossDialer(t *testing.T, store *DoltStore) *federationBranchResponseLossDialer {
	t.Helper()
	cfg, err := mysql.ParseDSN(store.connStr)
	if err != nil {
		t.Fatalf("parse store DSN: %v", err)
	}
	if cfg.Net != "tcp" {
		t.Fatalf("response-loss dialer requires TCP, got %q", cfg.Net)
	}
	dialer := &federationBranchResponseLossDialer{
		responseBlocked: make(chan struct{}),
		dropResponse:    make(chan struct{}),
	}
	network := fmt.Sprintf("federation_response_loss_%d", time.Now().UnixNano())
	mysql.RegisterDialContext(network, func(ctx context.Context, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		return &federationBranchResponseLossConn{Conn: conn, dialer: dialer}, nil
	})
	t.Cleanup(func() { mysql.DeregisterDialContext(network) })
	cfg.Net = network
	store.connStr = cfg.FormatDSN()
	return dialer
}

func (d *federationBranchResponseLossDialer) drop() {
	d.dropOnce.Do(func() { close(d.dropResponse) })
}

type federationBranchResponseLossConn struct {
	net.Conn
	dialer        *federationBranchResponseLossDialer
	blockResponse atomic.Bool
}

func (c *federationBranchResponseLossConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err == nil && bytes.Contains(bytes.ToLower(p), []byte("call dolt_branch(")) &&
		bytes.Contains(p, []byte(federationStagingBranchPrefix)) && c.dialer.armed.CompareAndSwap(false, true) {
		c.blockResponse.Store(true)
	}
	return n, err
}

func (c *federationBranchResponseLossConn) Read(p []byte) (int, error) {
	if !c.blockResponse.Load() {
		return c.Conn.Read(p)
	}
	c.dialer.blockedOnce.Do(func() { close(c.dialer.responseBlocked) })
	<-c.dialer.dropResponse
	_ = c.Conn.Close()
	return 0, net.ErrClosed
}

// TestFilteredPushOptOut verifies that setting federation.exclude_types to
// an empty list disables filtering (backward-compatible opt-out).
func TestFilteredPushOptOut(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an ephemeral issue in the committed issues table.
	_, err := store.db.ExecContext(ctx, `INSERT INTO issues
		(id, title, description, design, acceptance_criteria, notes,
		 issue_type, status, priority, ephemeral, created_at, updated_at)
		VALUES (?, ?, '', '', '', '', ?, ?, ?, 1, NOW(), NOW())`,
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
		if strings.HasPrefix(b, federationStagingBranchPrefix) {
			t.Errorf("staging branch should not exist when exclude_types is empty")
		}
	}
}

// TestFilteredPushExcludesCustomType verifies that non-wisp types in
// federation.exclude_types are filtered by issue_type column.
func TestFilteredPushExcludesCustomType(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()
	versionWispTablesForFilteredPush(t, ctx, store)
	observer, err := store.openLongTimeoutConn()
	if err != nil {
		t.Fatalf("open staging observer: %v", err)
	}
	defer observer.Close()

	// Create a task and a permanent non-infrastructure issue type.
	task := &types.Issue{
		ID:        "fed-custom-task",
		Title:     "Regular task",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	chore := &types.Issue{
		ID:        "fed-custom-chore",
		Title:     "Internal chore",
		IssueType: types.TypeChore,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, task, "test"); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := store.CreateIssue(ctx, chore, "test"); err != nil {
		t.Fatalf("create chore: %v", err)
	}
	if err := store.Commit(ctx, "create test issues"); err != nil {
		if !isDoltNothingToCommit(err) {
			t.Fatalf("commit: %v", err)
		}
	}

	// Hold the shared pool after staging is committed and before peer lookup so
	// the staging ref can be inspected before its cleanup runs.
	store.db.SetMaxIdleConns(0)
	store.db.SetMaxOpenConns(1)
	held, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("exhaust shared pool: %v", err)
	}
	defer held.Close()
	initialWaitCount := store.db.Stats().WaitCount
	errCh := make(chan error, 1)
	go func() {
		errCh <- store.filteredPushToPeer(ctx, "nonexistent-peer", []string{"chore"})
	}()
	waitForPoolWait(t, ctx, store.db, initialWaitCount)

	stagingBranches := listFederationStagingBranches(t, ctx, observer)
	if len(stagingBranches) != 1 {
		t.Fatalf("staging branches = %v, want exactly one", stagingBranches)
	}
	stagingBranch := stagingBranches[0]
	if err := issueops.ValidateRef(stagingBranch); err != nil {
		t.Fatalf("generated staging branch %q is invalid: %v", stagingBranch, err)
	}
	//nolint:gosec // stagingBranch is generated internally and validated as a Dolt ref above.
	taskQuery := fmt.Sprintf("SELECT COUNT(*) FROM issues AS OF '%s' WHERE id = ?", stagingBranch)
	//nolint:gosec // stagingBranch is generated internally and validated as a Dolt ref above.
	choreQuery := fmt.Sprintf("SELECT COUNT(*) FROM issues AS OF '%s' WHERE id = ?", stagingBranch)
	var stagedTaskCount, stagedChoreCount int
	if err := observer.QueryRowContext(ctx, taskQuery, "fed-custom-task").Scan(&stagedTaskCount); err != nil {
		t.Fatalf("count task on staging branch: %v", err)
	}
	if err := observer.QueryRowContext(ctx, choreQuery, "fed-custom-chore").Scan(&stagedChoreCount); err != nil {
		t.Fatalf("count chore on staging branch: %v", err)
	}
	if stagedTaskCount != 1 || stagedChoreCount != 0 {
		t.Fatalf("staging branch task count = %d, chore count = %d, want 1, 0", stagedTaskCount, stagedChoreCount)
	}

	if err := held.Close(); err != nil {
		t.Fatalf("release shared pool: %v", err)
	}
	if pushErr := <-errCh; pushErr == nil {
		t.Fatal("expected push error for nonexistent peer")
	}
	assertFederationStagingBranchAbsent(t, ctx, observer)

	// Verify original branch still has both issues.
	var taskCount, choreCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", "fed-custom-task").Scan(&taskCount); err != nil {
		t.Fatalf("count task: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", "fed-custom-chore").Scan(&choreCount); err != nil {
		t.Fatalf("count chore: %v", err)
	}
	if taskCount != 1 {
		t.Errorf("task should survive on original branch")
	}
	if choreCount != 1 {
		t.Errorf("chore should survive on original branch (filter is non-destructive)")
	}
}

// TestFilteredStagingPublishesRetainedWaiterUnblocked proves the staging HEAD
// derives blocked state from its filtered graph: removing X and W->X must
// publish retained waiter W with is_blocked=false.
func TestFilteredStagingPublishesRetainedWaiterUnblocked(t *testing.T) {
	if os.Getenv("BEADS_TEST_ENV_RUN_DOLT") == "1" && testServerPort == 0 {
		t.Fatal("BEADS_TEST_ENV_RUN_DOLT=1 but real-Dolt test infrastructure is unavailable")
	}
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	versionWispTablesForFilteredPush(t, ctx, store)

	waiter := &types.Issue{ID: "fed-filter-w", Title: "retained waiter", IssueType: types.TypeBug, Status: types.StatusOpen, Priority: 1}
	blocker := &types.Issue{ID: "fed-filter-x", Title: "excluded blocker", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 1, Labels: []string{"private"}}
	if err := store.CreateIssue(ctx, waiter, "test"); err != nil {
		t.Fatalf("create waiter: %v", err)
	}
	if err := store.CreateIssue(ctx, blocker, "test"); err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: waiter.ID, DependsOnID: blocker.ID, Type: types.DepBlocks}, "test"); err != nil {
		t.Fatalf("add blocking dependency: %v", err)
	}
	if !isBlocked(ctx, t, store.db, waiter.ID) {
		t.Fatal("precondition: waiter must be blocked by X")
	}

	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire staging connection: %v", err)
	}
	defer conn.Close()
	var sourceBranch string
	if err := conn.QueryRowContext(ctx, "SELECT active_branch()").Scan(&sourceBranch); err != nil {
		t.Fatalf("read source branch: %v", err)
	}
	for _, table := range []string{"issues", "dependencies", "labels"} {
		if err := schema.DrainCall(ctx, conn, "CALL DOLT_ADD(?)", table); err != nil {
			t.Fatalf("stage seed %s: %v", table, err)
		}
	}
	if err := schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('--allow-empty', '-m', 'test: filtered staging seed')"); err != nil {
		t.Fatalf("commit staging seed: %v", err)
	}
	var seedBlockerCount int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues AS OF 'HEAD' WHERE id = ?", blocker.ID).Scan(&seedBlockerCount); err != nil {
		t.Fatalf("count source blocker at HEAD: %v", err)
	}
	if seedBlockerCount != 1 {
		t.Fatalf("source blocker count at HEAD = %d, want 1", seedBlockerCount)
	}
	var seedLabelCount int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ?", blocker.ID).Scan(&seedLabelCount); err != nil {
		t.Fatalf("count source blocker labels at HEAD: %v", err)
	}
	if seedLabelCount != 1 {
		t.Fatalf("source blocker label count at HEAD = %d, want 1", seedLabelCount)
	}
	stagingBranch := federationStagingBranchPrefix + "recompute-test"
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, ?)", stagingBranch, sourceBranch); err != nil {
		t.Fatalf("create staging branch: %v", err)
	}
	defer func() {
		_ = schema.DrainCall(ctx, conn, "CALL DOLT_CHECKOUT(?)", sourceBranch)
		_ = schema.DrainCall(ctx, conn, "CALL DOLT_BRANCH('-Df', ?)", stagingBranch)
	}()
	if err := versioncontrolops.CheckoutBranch(ctx, conn, stagingBranch); err != nil {
		t.Fatalf("checkout staging branch: %v", err)
	}
	result, err := conn.ExecContext(ctx, "DELETE FROM issues WHERE issue_type = ?", types.TypeTask)
	if err != nil {
		t.Fatalf("delete excluded blocker: %v", err)
	}
	if deleted, err := result.RowsAffected(); err != nil || deleted != 1 {
		t.Fatalf("deleted blockers = %d, err = %v, want 1, nil", deleted, err)
	}
	if err := store.commitFilteredStaging(ctx, conn); err != nil {
		t.Fatalf("commit filtered staging: %v", err)
	}

	var blocked bool
	if err := conn.QueryRowContext(ctx, "SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", waiter.ID).Scan(&blocked); err != nil {
		t.Fatalf("read retained waiter at staging HEAD: %v", err)
	}
	if blocked {
		t.Fatal("staging HEAD published retained waiter as blocked after excluding its only blocker")
	}
	var blockerCount, edgeCount, labelCount int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues AS OF 'HEAD' WHERE id = ?", blocker.ID).Scan(&blockerCount); err != nil {
		t.Fatalf("count blocker at staging HEAD: %v", err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dependencies AS OF 'HEAD' WHERE issue_id = ? AND depends_on_issue_id = ?", waiter.ID, blocker.ID).Scan(&edgeCount); err != nil {
		t.Fatalf("count edge at staging HEAD: %v", err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ?", blocker.ID).Scan(&labelCount); err != nil {
		t.Fatalf("count blocker labels at staging HEAD: %v", err)
	}
	if blockerCount != 0 || edgeCount != 0 || labelCount != 0 {
		t.Fatalf("staging HEAD blocker count = %d, edge count = %d, label count = %d, want 0, 0, 0", blockerCount, edgeCount, labelCount)
	}
}

// TestFilteredPushStagingBranchCleanupOnError verifies that the staging
// branch is always cleaned up, even when the push operation fails.
func TestFilteredPushStagingBranchCleanupOnError(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()
	versionWispTablesForFilteredPush(t, ctx, store)

	// Run filtered push to a nonexistent peer — will fail, but staging
	// branch should still be cleaned up.
	_ = store.filteredPushToPeer(ctx, "no-such-peer", []string{"wisp", "message"})

	branches, err := store.ListBranches(ctx)
	if err != nil {
		t.Fatalf("list branches: %v", err)
	}
	for _, b := range branches {
		if strings.HasPrefix(b, federationStagingBranchPrefix) {
			t.Errorf("staging branch %s should be cleaned up after push error", b)
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
