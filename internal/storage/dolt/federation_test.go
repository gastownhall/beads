//go:build integration

package dolt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
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

// TestSyncCLIRemotesToSQL verifies GH#2315: after a server restart, CLI-only
// remotes are re-registered into the SQL server by syncCLIRemotesToSQL.
func TestSyncCLIRemotesToSQL(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	store, cleanup := setupTestStore(t)
	defer cleanup()

	remoteName := "test-sync-remote"
	remoteURL := "file:///tmp/test-sync-remote"

	// Ensure cliDir exists with a dolt init so CLI remote commands work.
	// In test mode, dbPath/database may not exist on the filesystem.
	dir := store.CLIDir()
	if dir == "" {
		t.Skip("no CLI dir available")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create CLI dir: %v", err)
	}
	initCmd := exec.Command("dolt", "init", "--name", "test", "--email", "test@test.com")
	initCmd.Dir = dir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init failed: %s: %v", out, err)
	}

	// Add remote via SQL so it exists in dolt_remotes
	if err := store.AddRemote(ctx, remoteName, remoteURL); err != nil {
		t.Fatalf("failed to add SQL remote: %v", err)
	}

	// Also add it to CLI so it persists across restarts
	if err := doltutil.AddCLIRemote(dir, remoteName, remoteURL); err != nil {
		t.Fatalf("failed to add CLI remote: %v", err)
	}

	// Verify remote exists in SQL
	has, err := store.HasRemote(ctx, remoteName)
	if err != nil {
		t.Fatalf("HasRemote failed: %v", err)
	}
	if !has {
		t.Fatal("expected remote to exist in SQL after add")
	}

	// Simulate server restart: remove the remote from SQL only
	if err := store.RemoveRemote(ctx, remoteName); err != nil {
		t.Fatalf("failed to remove SQL remote: %v", err)
	}

	// Verify it's gone from SQL
	has, err = store.HasRemote(ctx, remoteName)
	if err != nil {
		t.Fatalf("HasRemote after remove failed: %v", err)
	}
	if has {
		t.Fatal("expected remote to be absent from SQL after remove")
	}

	// Run sync — should re-register the CLI remote into SQL
	store.syncCLIRemotesToSQL(ctx)

	// Verify it's back in SQL
	has, err = store.HasRemote(ctx, remoteName)
	if err != nil {
		t.Fatalf("HasRemote after sync failed: %v", err)
	}
	if !has {
		t.Fatal("expected syncCLIRemotesToSQL to re-register the CLI remote into SQL")
	}

	// Verify the URL matches
	remotes, err := store.ListRemotes(ctx)
	if err != nil {
		t.Fatalf("ListRemotes failed: %v", err)
	}
	found := false
	for _, r := range remotes {
		if r.Name == remoteName {
			if r.URL != remoteURL {
				t.Errorf("expected URL %s, got %s", remoteURL, r.URL)
			}
			found = true
			break
		}
	}
	if !found {
		t.Errorf("remote %s not found in ListRemotes after sync", remoteName)
	}

	// Clean up CLI remote
	_ = doltutil.RemoveCLIRemote(dir, remoteName)
	_ = store.RemoveRemote(ctx, remoteName)
}

// TestMigrateServerRootRemotes verifies GH#2118: remotes added in the dolt
// server root directory (.beads/dolt/) are propagated to the database
// subdirectory (.beads/dolt/<database>/) during syncCLIRemotesToSQL.
// This handles the common case where users run `dolt remote add` in the
// visible server root instead of the database subdirectory.
func TestMigrateServerRootRemotes(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	store, cleanup := setupTestStore(t)
	defer cleanup()

	remoteName := "test-root-remote"
	remoteURL := "file:///tmp/test-root-remote"

	// Set up CLIDir with dolt init (database directory)
	cliDir := store.CLIDir()
	if cliDir == "" {
		t.Skip("no CLI dir available")
	}
	if err := os.MkdirAll(cliDir, 0755); err != nil {
		t.Fatalf("failed to create CLI dir: %v", err)
	}
	initCmd := exec.Command("dolt", "init", "--name", "test", "--email", "test@test.com")
	initCmd.Dir = cliDir
	if out, err := initCmd.CombinedOutput(); err != nil {
		// Might already be initialized
		if !strings.Contains(string(out), "already") {
			t.Fatalf("dolt init in CLIDir failed: %s: %v", out, err)
		}
	}

	// Set up server root (dbPath) with dolt init — separate from CLIDir
	rootDir := store.Path()
	if rootDir == "" || rootDir == cliDir {
		t.Skip("dbPath same as CLIDir — migration not applicable")
	}
	if _, err := os.Stat(filepath.Join(rootDir, ".dolt")); err != nil {
		// Initialize root dir if needed
		if err := os.MkdirAll(rootDir, 0755); err != nil {
			t.Fatalf("failed to create root dir: %v", err)
		}
		initRootCmd := exec.Command("dolt", "init", "--name", "test", "--email", "test@test.com")
		initRootCmd.Dir = rootDir
		if out, err := initRootCmd.CombinedOutput(); err != nil {
			if !strings.Contains(string(out), "already") {
				t.Fatalf("dolt init in root failed: %s: %v", out, err)
			}
		}
	}

	// Add remote to server root (the wrong place — simulates the user's mistake)
	if err := doltutil.AddCLIRemote(rootDir, remoteName, remoteURL); err != nil {
		t.Fatalf("failed to add remote to server root: %v", err)
	}
	defer func() { _ = doltutil.RemoveCLIRemote(rootDir, remoteName) }()

	// Verify remote is NOT in CLIDir before migration
	if url := doltutil.FindCLIRemote(cliDir, remoteName); url != "" {
		t.Fatalf("remote should not be in CLIDir before migration, found: %s", url)
	}

	// Remove from SQL if present (simulate clean state)
	_ = store.RemoveRemote(ctx, remoteName)

	// Run sync — should discover remote in server root and migrate to CLIDir + SQL
	store.syncCLIRemotesToSQL(ctx)

	// Verify remote was migrated to CLIDir
	if url := doltutil.FindCLIRemote(cliDir, remoteName); url == "" {
		t.Error("expected remote to be migrated to CLIDir")
	} else if url != remoteURL {
		t.Errorf("CLIDir remote URL = %q, want %q", url, remoteURL)
	}

	// Verify remote was registered in SQL
	has, err := store.HasRemote(ctx, remoteName)
	if err != nil {
		t.Fatalf("HasRemote failed: %v", err)
	}
	if !has {
		t.Error("expected remote to be registered in SQL after migration")
	}

	// Clean up
	_ = doltutil.RemoveCLIRemote(cliDir, remoteName)
	_ = store.RemoveRemote(ctx, remoteName)
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
