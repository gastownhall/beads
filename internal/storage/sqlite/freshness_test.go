package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// getInode returns the inode of a file (Unix only)
func getInode(path string) uint64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if sys := info.Sys(); sys != nil {
		if stat, ok := sys.(*syscall.Stat_t); ok {
			return stat.Ino
		}
	}
	return 0
}

// testFreshnessEnv creates two independent connections to the same database file.
// conn1 simulates the daemon's long-lived connection.
// conn2 simulates an external process (like git merge bringing in new data).
type testFreshnessEnv struct {
	t      *testing.T
	tmpDir string
	dbPath string
	store1 *SQLiteStorage // "daemon" connection
	conn2  *sql.DB        // "external" connection
}

func setupFreshnessTest(t *testing.T) *testFreshnessEnv {
	t.Helper()

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "beads-freshness-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	// Create "daemon" storage (conn1)
	store1, err := New(ctx, dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to create store1: %v", err)
	}

	// Initialize issue_prefix (required for beads)
	if err := store1.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		store1.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create "external" connection (conn2) - simulates another process
	conn2, err := sql.Open("sqlite3", "file:"+dbPath+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		store1.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to create conn2: %v", err)
	}

	// Enable WAL mode on conn2 too
	if _, err := conn2.Exec("PRAGMA journal_mode=WAL"); err != nil {
		conn2.Close()
		store1.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to enable WAL on conn2: %v", err)
	}

	env := &testFreshnessEnv{
		t:      t,
		tmpDir: tmpDir,
		dbPath: dbPath,
		store1: store1,
		conn2:  conn2,
	}

	// Register cleanup with t.Cleanup() for automatic cleanup even on panic
	t.Cleanup(func() {
		conn2.Close()
		store1.Close()
		os.RemoveAll(tmpDir)
	})

	return env
}

// insertIssueExternal inserts an issue directly via conn2, bypassing store1.
// This simulates what happens when git merge brings in database changes.
func (env *testFreshnessEnv) insertIssueExternal(id, title, status string) {
	env.t.Helper()

	_, err := env.conn2.Exec(`
		INSERT INTO issues (id, title, status, priority, issue_type, created_at, updated_at)
		VALUES (?, ?, ?, 2, 'task', datetime('now'), datetime('now'))
	`, id, title, status)
	if err != nil {
		env.t.Fatalf("external insert failed: %v", err)
	}
}

// updateIssueExternal updates an issue directly via conn2.
func (env *testFreshnessEnv) updateIssueExternal(id, status string) {
	env.t.Helper()

	// Handle closed_at constraint: if closing, set closed_at; otherwise clear it
	var query string
	if status == "closed" {
		query = `UPDATE issues SET status = ?, closed_at = datetime('now'), updated_at = datetime('now') WHERE id = ?`
	} else {
		query = `UPDATE issues SET status = ?, closed_at = NULL, updated_at = datetime('now') WHERE id = ?`
	}
	_, err := env.conn2.Exec(query, status, id)
	if err != nil {
		env.t.Fatalf("external update failed: %v", err)
	}
}

// deleteIssueExternal deletes an issue directly via conn2.
func (env *testFreshnessEnv) deleteIssueExternal(id string) {
	env.t.Helper()

	_, err := env.conn2.Exec(`DELETE FROM issues WHERE id = ?`, id)
	if err != nil {
		env.t.Fatalf("external delete failed: %v", err)
	}
}

// TestExternalInsertDetection verifies that store1 sees issues inserted via conn2.
// This test will FAIL on main (before the fix) because the daemon's connection
// may hold a stale WAL snapshot and not see external writes.
func TestExternalInsertDetection(t *testing.T) {
	env := setupFreshnessTest(t)

	ctx := context.Background()

	// Verify no issues initially via store1
	issues, err := env.store1.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("initial SearchIssues failed: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues initially, got %d", len(issues))
	}

	// Insert issue via external connection (simulates branch merge)
	env.insertIssueExternal("bd-ext1", "External Insert Test", "open")

	// Query via store1 - should see the new issue
	// Note: Without freshness checking, this may fail due to WAL snapshot isolation
	issue, err := env.store1.GetIssue(ctx, "bd-ext1")
	if err != nil {
		t.Errorf("GetIssue failed: %v (daemon did not see external insert)", err)
		return
	}
	if issue == nil {
		t.Error("issue is nil (daemon connection has stale snapshot)")
		return
	}
	if issue.Title != "External Insert Test" {
		t.Errorf("wrong title: got %q, want %q", issue.Title, "External Insert Test")
	}
}

// TestExternalUpdateDetection verifies that store1 sees updates made via conn2.
func TestExternalUpdateDetection(t *testing.T) {
	env := setupFreshnessTest(t)

	ctx := context.Background()

	// Create issue via store1 first
	issue := &types.Issue{
		Title:     "Update Test",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := env.store1.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	issueID := issue.ID

	// Verify initial status
	got, err := env.store1.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if got.Status != types.StatusOpen {
		t.Fatalf("expected status %q, got %q", types.StatusOpen, got.Status)
	}

	// Update via external connection
	env.updateIssueExternal(issueID, "closed")

	// Query via store1 - should see updated status
	got, err = env.store1.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if got.Status != "closed" {
		t.Errorf("daemon returned stale status: got %q, want %q", got.Status, "closed")
	}
}

// TestExternalDeleteDetection verifies that store1 sees deletions made via conn2.
func TestExternalDeleteDetection(t *testing.T) {
	env := setupFreshnessTest(t)

	ctx := context.Background()

	// Create issue via store1
	issue := &types.Issue{
		Title:     "Delete Test",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := env.store1.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	issueID := issue.ID

	// Verify issue exists
	got, err := env.store1.GetIssue(ctx, issueID)
	if err != nil || got == nil {
		t.Fatalf("issue should exist initially: err=%v", err)
	}

	// Delete via external connection
	env.deleteIssueExternal(issueID)

	// Query via store1 - should NOT find the issue
	got, err = env.store1.GetIssue(ctx, issueID)
	if err == nil && got != nil {
		t.Error("daemon still returns deleted issue (stale snapshot)")
	}
}

// TestDataVersionChanges verifies that PRAGMA data_version increments on writes.
// This is the foundation of our staleness detection.
func TestDataVersionChanges(t *testing.T) {
	env := setupFreshnessTest(t)

	// Get initial data_version from store1's connection
	var version1 int64
	if err := env.store1.db.QueryRow("PRAGMA data_version").Scan(&version1); err != nil {
		t.Fatalf("failed to get data_version: %v", err)
	}

	// Write via external connection
	env.insertIssueExternal("bd-ver1", "Version Test", "open")

	// Get data_version again - should have changed
	var version2 int64
	if err := env.store1.db.QueryRow("PRAGMA data_version").Scan(&version2); err != nil {
		t.Fatalf("failed to get data_version after write: %v", err)
	}

	if version2 == version1 {
		t.Errorf("data_version did not change after external write: before=%d, after=%d", version1, version2)
	}
}

// TestDetectionTiming verifies that external changes are detected within 1 second.
func TestDetectionTiming(t *testing.T) {
	env := setupFreshnessTest(t)

	ctx := context.Background()

	// Insert via external connection
	env.insertIssueExternal("bd-timing1", "Timing Test", "open")

	// Poll until visible or timeout
	deadline := time.Now().Add(2 * time.Second)
	var found bool

	for time.Now().Before(deadline) {
		issue, err := env.store1.GetIssue(ctx, "bd-timing1")
		if err == nil && issue != nil {
			found = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !found {
		t.Error("issue not visible within 2 seconds (freshness checking not working)")
	}
}

// TestSameConnectionStaleness tests if a SINGLE connection can become stale.
// This simulates the daemon's long-lived connection pool behavior.
func TestSameConnectionStaleness(t *testing.T) {
	env := setupFreshnessTest(t)

	ctx := context.Background()

	// Get a connection from store1's pool
	conn, err := env.store1.db.Conn(ctx)
	if err != nil {
		t.Fatalf("failed to get connection: %v", err)
	}
	defer conn.Close()

	// Do a query on this connection (starts implicit read transaction)
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
		t.Fatalf("initial query failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 issues initially, got %d", count)
	}

	// External connection inserts data
	env.insertIssueExternal("bd-stale1", "Staleness Test", "open")

	// Query on the SAME connection - should see the new data
	// In WAL mode, each query should start a fresh implicit transaction
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
		t.Fatalf("second query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("same connection is stale: expected 1 issue, got %d", count)
	}

	// Also verify GetIssue works on the same connection
	var title string
	err = conn.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", "bd-stale1").Scan(&title)
	if err != nil {
		t.Errorf("GetIssue on same connection failed: %v (connection is stale)", err)
	}
}

// TestPooledConnectionStaleness tests if pooled connections become stale.
// This is closer to the real daemon scenario where connections are reused.
func TestPooledConnectionStaleness(t *testing.T) {
	env := setupFreshnessTest(t)

	ctx := context.Background()

	// Do multiple queries to "warm up" the connection pool
	for i := range 5 {
		var count int
		if err := env.store1.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
			t.Fatalf("warmup query %d failed: %v", i, err)
		}
	}

	// Insert via external connection
	env.insertIssueExternal("bd-pool1", "Pool Staleness Test", "open")

	// Query via store1's pool - should see the new data
	var count int
	if err := env.store1.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
		t.Fatalf("query after external insert failed: %v", err)
	}
	if count != 1 {
		t.Errorf("pooled connection is stale: expected 1 issue, got %d", count)
	}

	// Also verify via GetIssue
	issue, err := env.store1.GetIssue(ctx, "bd-pool1")
	if err != nil {
		t.Errorf("GetIssue via pool failed: %v", err)
	}
	if issue == nil {
		t.Error("issue is nil (pool connection stale)")
	}
}

// TestDatabaseFileReplacement tests what happens when the database file is REPLACED.
// This is the real git merge scenario - the .db file is swapped for a different file.
func TestDatabaseFileReplacement(t *testing.T) {
	// Create a temp directory for the first database
	tmpDir1, err := os.MkdirTemp("", "beads-replace-test-1-*")
	if err != nil {
		t.Fatalf("failed to create temp dir 1: %v", err)
	}
	defer os.RemoveAll(tmpDir1)

	dbPath1 := filepath.Join(tmpDir1, "test.db")
	ctx := context.Background()

	// Create "daemon" storage (this stays open)
	daemonStore, err := New(ctx, dbPath1)
	if err != nil {
		t.Fatalf("failed to create daemon store: %v", err)
	}
	defer daemonStore.Close()

	// Enable freshness checking (this is what the daemon does)
	daemonStore.EnableFreshnessChecking()

	// Initialize
	if err := daemonStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create an issue via daemon
	issue := &types.Issue{
		Title:     "Original Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := daemonStore.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Verify issue exists
	got, err := daemonStore.GetIssue(ctx, issue.ID)
	if err != nil || got == nil {
		t.Fatalf("issue should exist: err=%v", err)
	}

	// Now simulate git merge: create a NEW database with different data
	tmpDir2, err := os.MkdirTemp("", "beads-replace-test-2-*")
	if err != nil {
		t.Fatalf("failed to create temp dir 2: %v", err)
	}
	defer os.RemoveAll(tmpDir2)

	dbPath2 := filepath.Join(tmpDir2, "test.db")

	// Create a separate storage for the "branch" database
	branchStore, err := New(ctx, dbPath2)
	if err != nil {
		t.Fatalf("failed to create branch store: %v", err)
	}
	if err := branchStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		branchStore.Close()
		t.Fatalf("failed to set issue_prefix on branch: %v", err)
	}

	// Create a NEW issue in the branch database
	branchIssue := &types.Issue{
		Title:     "Branch Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := branchStore.CreateIssue(ctx, branchIssue, "test-user"); err != nil {
		branchStore.Close()
		t.Fatalf("CreateIssue on branch failed: %v", err)
	}
	branchIssueID := branchIssue.ID // Save the auto-generated ID
	t.Logf("Branch issue ID: %s", branchIssueID)

	// Verify the issue exists in branch store before closing
	verifyIssue, _ := branchStore.GetIssue(ctx, branchIssueID)
	if verifyIssue == nil {
		t.Fatalf("Branch issue not found in branch store before close!")
	}
	t.Logf("Branch issue verified in branch store: %s", verifyIssue.Title)

	// Close branch store and checkpoint WAL
	branchStore.Close()

	// Verify the branch database file has the issue after closing
	verifyStore, err := New(ctx, dbPath2)
	if err != nil {
		t.Fatalf("failed to open branch db for verification: %v", err)
	}
	verifyStore.SetConfig(ctx, "issue_prefix", "bd")
	verifyIssue2, _ := verifyStore.GetIssue(ctx, branchIssueID)
	if verifyIssue2 == nil {
		t.Fatalf("Branch issue not found in branch db after close!")
	}
	t.Logf("Branch issue verified in branch db after close")
	verifyStore.Close()

	// Log sizes before replacement
	info1Before, _ := os.Stat(dbPath1)
	info2Before, _ := os.Stat(dbPath2)
	inodeBefore := getInode(dbPath1)
	t.Logf("Before replacement: daemon db=%d bytes, branch db=%d bytes, inode=%d", info1Before.Size(), info2Before.Size(), inodeBefore)

	// REPLACE the daemon's database file with the branch database
	// This is what git merge does - replaces the file atomically via rename
	// Using os.Rename creates a new inode at the target path

	// First, remove the old WAL/SHM files to ensure clean state
	os.Remove(dbPath1 + "-wal")
	os.Remove(dbPath1 + "-shm")

	// Read branch db content
	srcDB, err := os.ReadFile(dbPath2)
	if err != nil {
		t.Fatalf("failed to read branch db: %v", err)
	}
	t.Logf("Read %d bytes from branch db", len(srcDB))

	// Create a temp file in the same directory as dbPath1 (same filesystem for atomic rename)
	tempFile := dbPath1 + ".new"
	if err := os.WriteFile(tempFile, srcDB, 0644); err != nil {
		t.Fatalf("failed to write temp db file: %v", err)
	}

	// Atomic rename - this is what git does
	// The old file is unlinked, but the daemon's open file descriptor still points to it
	// The new file gets a NEW inode at dbPath1
	if err := os.Rename(tempFile, dbPath1); err != nil {
		t.Fatalf("failed to rename db file: %v", err)
	}
	t.Logf("Atomically replaced daemon db (via rename)")

	// Log the new inode
	inodeAfter := getInode(dbPath1)
	t.Logf("Inode changed: %d -> %d (should be different!)", inodeBefore, inodeAfter)
	if inodeBefore == inodeAfter {
		t.Logf("WARNING: inode did not change - atomic replace may not have worked")
	}

	// Copy WAL and SHM files if they exist (for completeness)
	if wal, err := os.ReadFile(dbPath2 + "-wal"); err == nil {
		os.WriteFile(dbPath1+"-wal", wal, 0644)
		t.Logf("Copied WAL file: %d bytes", len(wal))
	}
	if shm, err := os.ReadFile(dbPath2 + "-shm"); err == nil {
		os.WriteFile(dbPath1+"-shm", shm, 0644)
		t.Logf("Copied SHM file: %d bytes", len(shm))
	}

	// Verify the replaced file can be opened independently
	verifyAfterReplace, err := New(ctx, dbPath1)
	if err != nil {
		t.Fatalf("failed to open replaced db: %v", err)
	}
	verifyAfterReplace.SetConfig(ctx, "issue_prefix", "bd")
	verifyIssue3, _ := verifyAfterReplace.GetIssue(ctx, branchIssueID)
	if verifyIssue3 != nil {
		t.Logf("SUCCESS: Branch issue visible in replaced db via independent connection")
	} else {
		t.Logf("FAIL: Branch issue NOT visible in replaced db via independent connection")
		// List all issues
		issues, _ := verifyAfterReplace.SearchIssues(ctx, "", types.IssueFilter{})
		for _, iss := range issues {
			t.Logf("  Found issue: %s - %s", iss.ID, iss.Title)
		}
	}
	verifyAfterReplace.Close()

	// Add small delay to ensure file system updates are flushed
	time.Sleep(100 * time.Millisecond)

	// Verify the file was actually replaced by checking mtime
	info, _ := os.Stat(dbPath1)
	t.Logf("DB file after replacement: mtime=%v size=%d", info.ModTime(), info.Size())

	// Debug: check freshness checker state
	if daemonStore.freshness != nil {
		t.Logf("Freshness checker enabled: %v", daemonStore.freshness.IsEnabled())
		inode, mtime, size := daemonStore.freshness.DebugState()
		t.Logf("Freshness tracked state: inode=%d, mtime=%v, size=%d", inode, mtime, size)
		// Get current file state
		if stat, err := os.Stat(dbPath1); err == nil {
			t.Logf("Current file state: mtime=%v, size=%d", stat.ModTime(), stat.Size())
		}
	} else {
		t.Logf("Freshness checker is nil!")
	}

	// Query via daemon store - can it see the branch issue?
	// This is the actual bug scenario - file was replaced but daemon doesn't know
	branchIssueResult, err := daemonStore.GetIssue(ctx, branchIssueID)
	if err != nil {
		t.Logf("GetIssue for branch issue failed: %v", err)
	}
	if branchIssueResult == nil {
		// Debug: check what's in the database directly
		var count int
		daemonStore.db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&count)
		t.Logf("DEBUG: issue count in daemon's DB: %d", count)

		// Check directly from the file
		debugStore, _ := New(ctx, dbPath1)
		if debugStore != nil {
			debugStore.SetConfig(ctx, "issue_prefix", "bd")
			debugIssue, _ := debugStore.GetIssue(ctx, branchIssueID)
			if debugIssue != nil {
				t.Logf("DEBUG: branch issue IS visible via fresh connection")
			} else {
				t.Logf("DEBUG: branch issue NOT visible via fresh connection either")
			}
			debugStore.Close()
		}

		t.Errorf("daemon store cannot see branch issue %s after file replacement (this is the bug!)", branchIssueID)
	}
}

// BenchmarkFreshnessCheck measures the overhead of a freshness check (os.Stat + mutex).
// This runs without the bench tag to be easily accessible.
func BenchmarkFreshnessCheck(b *testing.B) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "beads-freshness-bench-*")
	if err != nil {
		b.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	// Create store
	store, err := New(ctx, dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Enable freshness checking
	store.EnableFreshnessChecking()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// This is what happens on every read operation
		store.checkFreshness()
	}
}

// TestBranchMergeNoErroneousDeletion tests the full branch merge scenario.
// This is an end-to-end test for the daemon stale cache fix.
//
// Scenario:
// 1. Main has issue A in DB
// 2. Branch is created, issue B is added
// 3. Branch merged to main (DB file replaced)
// 4. WITHOUT fix: daemon's stale connection sees old DB (only A)
// 5. WITHOUT fix: if auto-import runs with NoGitHistory=false, B could be deleted
// 6. WITH fix: freshness checker detects file replacement, reconnects, sees A and B
//
// On main (without fix): daemon doesn't see issue B after merge
// On fix branch: daemon sees both issues correctly
func TestBranchMergeNoErroneousDeletion(t *testing.T) {
	// === SETUP: Create "main" database with issue A ===
	tmpDir1, err := os.MkdirTemp("", "beads-merge-test-main-*")
	if err != nil {
		t.Fatalf("failed to create main temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir1)

	mainDBPath := filepath.Join(tmpDir1, "beads.db")
	ctx := context.Background()

	// Create main store with issue A
	mainStore, err := New(ctx, mainDBPath)
	if err != nil {
		t.Fatalf("failed to create main store: %v", err)
	}
	mainStore.SetConfig(ctx, "issue_prefix", "bd")

	issueA := &types.Issue{
		Title:     "Issue A (existed on main)",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := mainStore.CreateIssue(ctx, issueA, "test-user"); err != nil {
		t.Fatalf("failed to create issue A: %v", err)
	}
	issueAID := issueA.ID
	t.Logf("Created issue A on main: %s", issueAID)
	mainStore.Close()

	// === SETUP: Create "branch" database with issues A and B ===
	tmpDir2, err := os.MkdirTemp("", "beads-merge-test-branch-*")
	if err != nil {
		t.Fatalf("failed to create branch temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir2)

	branchDBPath := filepath.Join(tmpDir2, "beads.db")

	// Create branch store and copy issue A, then add issue B
	branchStore, err := New(ctx, branchDBPath)
	if err != nil {
		t.Fatalf("failed to create branch store: %v", err)
	}
	branchStore.SetConfig(ctx, "issue_prefix", "bd")

	// Copy issue A to branch
	issueACopy := &types.Issue{
		ID:        issueAID,
		Title:     "Issue A (existed on main)",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := branchStore.CreateIssue(ctx, issueACopy, "test-user"); err != nil {
		t.Fatalf("failed to copy issue A to branch: %v", err)
	}

	// Create issue B on branch
	issueB := &types.Issue{
		Title:     "Issue B (created on branch)",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeFeature,
	}
	if err := branchStore.CreateIssue(ctx, issueB, "test-user"); err != nil {
		t.Fatalf("failed to create issue B: %v", err)
	}
	issueBID := issueB.ID
	t.Logf("Created issue B on branch: %s", issueBID)
	branchStore.Close()

	// === SIMULATE DAEMON: Open main DB and enable freshness checking ===
	daemonStore, err := New(ctx, mainDBPath)
	if err != nil {
		t.Fatalf("failed to create daemon store: %v", err)
	}
	defer daemonStore.Close()

	// Enable freshness checking (this is what the daemon does)
	daemonStore.EnableFreshnessChecking()
	daemonStore.SetConfig(ctx, "issue_prefix", "bd")

	// Verify daemon sees only issue A initially
	issuesBeforeMerge, _ := daemonStore.SearchIssues(ctx, "", types.IssueFilter{})
	t.Logf("Daemon sees %d issue(s) before merge", len(issuesBeforeMerge))
	if len(issuesBeforeMerge) != 1 {
		t.Errorf("Expected 1 issue before merge, got %d", len(issuesBeforeMerge))
	}

	// === SIMULATE GIT MERGE: Replace main DB file with branch DB ===
	inodeBefore := getInode(mainDBPath)

	// Remove WAL/SHM files
	os.Remove(mainDBPath + "-wal")
	os.Remove(mainDBPath + "-shm")

	// Read branch DB and atomically replace main DB
	branchDBContent, err := os.ReadFile(branchDBPath)
	if err != nil {
		t.Fatalf("failed to read branch DB: %v", err)
	}

	tempFile := mainDBPath + ".new"
	if err := os.WriteFile(tempFile, branchDBContent, 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	if err := os.Rename(tempFile, mainDBPath); err != nil {
		t.Fatalf("failed to rename: %v", err)
	}

	inodeAfter := getInode(mainDBPath)
	t.Logf("Merge simulation: inode %d -> %d", inodeBefore, inodeAfter)

	// Small delay to ensure filesystem settles
	time.Sleep(100 * time.Millisecond)

	// === VERIFY: Daemon should see BOTH issues after merge ===
	// The freshness checker should detect the file replacement and reconnect

	issueAResult, err := daemonStore.GetIssue(ctx, issueAID)
	if err != nil || issueAResult == nil {
		t.Errorf("Issue A not visible after merge: err=%v", err)
	} else {
		t.Logf("Issue A visible after merge: %s", issueAResult.Title)
	}

	issueBResult, err := daemonStore.GetIssue(ctx, issueBID)
	if err != nil || issueBResult == nil {
		t.Errorf("Issue B not visible after merge (this is the bug!): err=%v", err)
	} else {
		t.Logf("Issue B visible after merge: %s", issueBResult.Title)
	}

	issuesAfterMerge, _ := daemonStore.SearchIssues(ctx, "", types.IssueFilter{})
	t.Logf("Daemon sees %d issue(s) after merge", len(issuesAfterMerge))

	if len(issuesAfterMerge) != 2 {
		t.Errorf("Expected 2 issues after merge, got %d", len(issuesAfterMerge))
		t.Logf("This demonstrates the stale cache bug - daemon doesn't see merged changes")
	}

	// === VERIFY: No erroneous deletions ===
	// In a buggy scenario without NoGitHistory protection, issue B could be
	// incorrectly added to deletions.jsonl. With the freshness fix, the daemon
	// sees the correct DB state and no deletion occurs.

	// Check if any deletions occurred (they shouldn't)
	// Note: This test doesn't create a deletions.jsonl file, so we verify
	// by ensuring both issues are still accessible
	finalIssueA, _ := daemonStore.GetIssue(ctx, issueAID)
	finalIssueB, _ := daemonStore.GetIssue(ctx, issueBID)

	if finalIssueA == nil {
		t.Error("ERRONEOUS DELETION: Issue A was deleted!")
	}
	if finalIssueB == nil {
		t.Error("ERRONEOUS DELETION: Issue B was deleted!")
	}
}

// TestConcurrentReadsWithReconnect verifies the race condition fix from GH#607.
// The race condition was:
// 1. Operation A calls checkFreshness() → no change → proceeds to use s.db
// 2. Operation B calls checkFreshness() → detects change → calls reconnect()
// 3. reconnect() closes s.db while Operation A is still using it
// 4. Operation A fails with "database is closed"
//
// The fix uses sync.RWMutex:
// - Read operations hold RLock during database access
// - reconnect() holds exclusive Lock, waiting for readers to finish
func TestConcurrentReadsWithReconnect(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "beads-concurrent-reconnect-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	// Create store
	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Initialize
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		store.Close()
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create an issue to query
	issue := &types.Issue{
		Title:     "Concurrent Test Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		store.Close()
		t.Fatalf("failed to create issue: %v", err)
	}
	issueID := issue.ID

	// Enable freshness checking
	store.EnableFreshnessChecking()

	// Track errors from concurrent operations
	const numGoroutines = 50
	const opsPerGoroutine = 100
	errChan := make(chan error, numGoroutines*opsPerGoroutine)
	doneChan := make(chan struct{})
	var wg sync.WaitGroup

	// Start goroutines that continuously call GetIssue
	for i := range numGoroutines {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := range opsPerGoroutine {
				select {
				case <-doneChan:
					return
				default:
				}

				_, err := store.GetIssue(ctx, issueID)
				if err != nil {
					errChan <- err
				}

				// Occasionally trigger reconnect by touching the file
				// This simulates external modifications
				if j%20 == 0 && goroutineID == 0 {
					// Touch the file to change mtime
					now := time.Now()
					os.Chtimes(dbPath, now, now)
				}
			}
		}(i)
	}

	// Also start a goroutine that forces reconnections
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 20 {
			select {
			case <-doneChan:
				return
			default:
			}

			// Force reconnection by calling it directly
			// This simulates what happens when freshness check detects changes
			_ = store.reconnect()

			// Small delay between reconnections
			time.Sleep(time.Duration(10+i) * time.Millisecond)
		}
	}()

	// Wait for all operations to complete (max 2 seconds)
	time.Sleep(2 * time.Second)
	close(doneChan)

	// Wait for all goroutines to finish
	wg.Wait()

	// Now safe to close store and error channel
	store.Close()
	close(errChan)

	// Count errors
	var dbClosedErrors int
	var otherErrors int
	for err := range errChan {
		errStr := err.Error()
		if errStr == "sql: database is closed" ||
			errStr == "database is closed" ||
			errStr == "sql: statement is closed" {
			dbClosedErrors++
		} else {
			otherErrors++
			t.Logf("Other error: %v", err)
		}
	}

	if dbClosedErrors > 0 {
		t.Errorf("Race condition detected: %d 'database is closed' errors occurred (GH#607 not fixed)", dbClosedErrors)
	}
	if otherErrors > 0 {
		t.Logf("Note: %d non-database-closed errors occurred (may be expected)", otherErrors)
	}
	t.Logf("Completed %d goroutines × %d ops with %d db closed errors, %d other errors",
		numGoroutines, opsPerGoroutine, dbClosedErrors, otherErrors)
}

// BenchmarkGetIssueWithFreshness measures GetIssue with freshness checking enabled.
func BenchmarkGetIssueWithFreshness(b *testing.B) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "beads-freshness-bench-*")
	if err != nil {
		b.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	// Create store
	store, err := New(ctx, dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Initialize
	store.SetConfig(ctx, "issue_prefix", "bd")

	// Create an issue to query
	issue := &types.Issue{
		Title:     "Benchmark Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
		b.Fatalf("failed to create issue: %v", err)
	}
	issueID := issue.ID

	// Enable freshness checking
	store.EnableFreshnessChecking()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := store.GetIssue(ctx, issueID)
		if err != nil {
			b.Fatalf("GetIssue failed: %v", err)
		}
	}
}
