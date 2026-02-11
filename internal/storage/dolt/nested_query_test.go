//go:build cgo

package dolt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestServerModeNestedQueries tests that SearchIssues works correctly in server mode.
//
// This test reproduces a bug where MySQL server mode (go-sql-driver/mysql) cannot
// handle multiple active result sets on a single connection. When SearchIssues
// iterates over query results (sql.Rows) and calls GetIssue (another query) before
// closing the rows, it causes "driver: bad connection" errors.
//
// The fix ensures rows are fully consumed and closed before executing nested queries.
func TestServerModeNestedQueries(t *testing.T) {
	// Skip if dolt is not available
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping server mode test")
	}

	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "dolt-nested-query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize dolt repo
	cmd := exec.Command("dolt", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init dolt repo: %v", err)
	}

	// Start server on non-standard ports to avoid conflicts
	server := NewServer(ServerConfig{
		DataDir:        tmpDir,
		SQLPort:        13308, // Different port from other tests
		RemotesAPIPort: 18082,
		Host:           "127.0.0.1",
		LogFile:        filepath.Join(tmpDir, "server.log"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := server.Start(ctx); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() {
		if err := server.Stop(); err != nil {
			t.Logf("warning: failed to stop server: %v", err)
		}
	}()

	// Connect using server mode
	store, err := New(ctx, &Config{
		Path:       tmpDir,
		Database:   "beads",
		ServerMode: true,
		ServerHost: "127.0.0.1",
		ServerPort: 13308,
	})
	if err != nil {
		t.Fatalf("failed to create server mode store: %v", err)
	}
	defer store.Close()

	// Set issue prefix (required for creating issues)
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create multiple issues - we need at least 2 to trigger the nested query bug
	// The bug occurs when SearchIssues iterates over results and calls GetIssue
	// for each row while the original rows cursor is still open.
	issues := []*types.Issue{
		{
			Title:       "First test issue for nested query",
			Description: "Testing nested query handling",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		},
		{
			Title:       "Second test issue for nested query",
			Description: "Another issue to ensure multiple results",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		},
		{
			Title:       "Third test issue for nested query",
			Description: "More issues increase chance of triggering the bug",
			Status:      types.StatusOpen,
			Priority:    3,
			IssueType:   types.TypeBug,
		},
	}

	for i, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %d: %v", i+1, err)
		}
		t.Logf("Created issue: %s", issue.ID)
	}

	// Now call SearchIssues which should trigger the bug in unfixed code.
	// SearchIssues internally:
	// 1. Queries for issue IDs
	// 2. Iterates over rows with rows.Next()
	// 3. Calls GetIssue(id) for each row - THIS IS A NESTED QUERY
	// 4. In MySQL server mode, step 3 fails with "driver: bad connection"
	//    because the connection can't have multiple active result sets.
	t.Log("Calling SearchIssues - this triggers nested queries in server mode...")

	results, err := store.SearchIssues(ctx, "nested query", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed with error: %v", err)
	}

	if len(results) != 3 {
		t.Errorf("expected 3 issues matching 'nested query', got %d", len(results))
	}

	// Also test with a status filter to exercise another code path
	openStatus := types.StatusOpen
	results, err = store.SearchIssues(ctx, "", types.IssueFilter{Status: &openStatus})
	if err != nil {
		t.Fatalf("SearchIssues with status filter failed: %v", err)
	}

	if len(results) < 3 {
		t.Errorf("expected at least 3 open issues, got %d", len(results))
	}

	t.Logf("Server mode nested query test passed: SearchIssues returned %d results", len(results))
}

// TestServerModeDependencyQueries tests that dependency queries work in server mode.
//
// This tests the scanIssueIDs -> GetIssuesByIDs path which also has a nested query
// that can trigger "driver: bad connection" in MySQL server mode.
func TestServerModeDependencyQueries(t *testing.T) {
	// Skip if dolt is not available
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping server mode test")
	}

	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "dolt-dep-query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize dolt repo
	cmd := exec.Command("dolt", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init dolt repo: %v", err)
	}

	// Start server
	server := NewServer(ServerConfig{
		DataDir:        tmpDir,
		SQLPort:        13309,
		RemotesAPIPort: 18083,
		Host:           "127.0.0.1",
		LogFile:        filepath.Join(tmpDir, "server.log"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := server.Start(ctx); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() {
		if err := server.Stop(); err != nil {
			t.Logf("warning: failed to stop server: %v", err)
		}
	}()

	// Connect using server mode
	store, err := New(ctx, &Config{
		Path:       tmpDir,
		Database:   "beads",
		ServerMode: true,
		ServerHost: "127.0.0.1",
		ServerPort: 13309,
	})
	if err != nil {
		t.Fatalf("failed to create server mode store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create a parent issue and multiple child issues with dependencies
	parent := &types.Issue{
		Title:       "Parent issue",
		Description: "This blocks other issues",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("failed to create parent issue: %v", err)
	}
	t.Logf("Created parent issue: %s", parent.ID)

	// Create multiple children that depend on the parent
	children := make([]*types.Issue, 3)
	for i := 0; i < 3; i++ {
		child := &types.Issue{
			Title:       "Child issue",
			Description: "This is blocked by parent",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, child, "test"); err != nil {
			t.Fatalf("failed to create child issue %d: %v", i+1, err)
		}
		children[i] = child
		t.Logf("Created child issue: %s", child.ID)

		// Add dependency: child depends on parent
		dep := &types.Dependency{
			IssueID:     child.ID,
			DependsOnID: parent.ID,
			Type:        types.DepBlocks,
		}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}
	}

	// Now query dependents - this uses scanIssueIDs which has the nested query bug
	t.Log("Calling GetDependents - this triggers nested queries via scanIssueIDs...")

	dependents, err := store.GetDependents(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetDependents failed with error: %v", err)
	}

	if len(dependents) != 3 {
		t.Errorf("expected 3 dependents, got %d", len(dependents))
	}

	// Also test GetReadyWork which may use similar patterns
	t.Log("Calling GetReadyWork...")
	readyWork, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	// Parent should be ready (not blocked), children should not be ready (blocked)
	foundParent := false
	for _, issue := range readyWork {
		if issue.ID == parent.ID {
			foundParent = true
		}
		for _, child := range children {
			if issue.ID == child.ID {
				t.Errorf("child %s should not be in ready work (it's blocked)", child.ID)
			}
		}
	}
	if !foundParent {
		t.Error("parent should be in ready work (not blocked)")
	}

	// Test GetBlockedIssues - this has nested queries INSIDE the row iteration loop
	// Line 350 in queries.go calls s.GetIssue(ctx, id) while rows are still open
	// Line 357 then creates another query for blockerRows
	// This pattern should trigger "driver: bad connection" in server mode
	t.Log("Calling GetBlockedIssues - this has nested queries inside row iteration...")
	blockedIssues, err := store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues failed with error: %v", err)
	}

	if len(blockedIssues) != 3 {
		t.Errorf("expected 3 blocked issues, got %d", len(blockedIssues))
	}

	t.Logf("Server mode dependency query test passed")
}

// TestServerModeTransactionSearchIssues tests that SearchIssues within a transaction
// works correctly in server mode.
//
// The bug is specifically in transaction.go's SearchIssues method (not queries.go's).
// The transaction's SearchIssues calls GetIssue(id) inside the for rows.Next() loop
// while rows are still open, causing "driver: bad connection" in MySQL server mode.
func TestServerModeTransactionSearchIssues(t *testing.T) {
	// Skip if dolt is not available
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping server mode test")
	}

	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "dolt-tx-search-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize dolt repo
	cmd := exec.Command("dolt", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init dolt repo: %v", err)
	}

	// Start server on non-standard ports to avoid conflicts
	server := NewServer(ServerConfig{
		DataDir:        tmpDir,
		SQLPort:        13310,
		RemotesAPIPort: 18084,
		Host:           "127.0.0.1",
		LogFile:        filepath.Join(tmpDir, "server.log"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := server.Start(ctx); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() {
		if err := server.Stop(); err != nil {
			t.Logf("warning: failed to stop server: %v", err)
		}
	}()

	// Connect using server mode
	store, err := New(ctx, &Config{
		Path:       tmpDir,
		Database:   "beads",
		ServerMode: true,
		ServerHost: "127.0.0.1",
		ServerPort: 13310,
	})
	if err != nil {
		t.Fatalf("failed to create server mode store: %v", err)
	}
	defer store.Close()

	// Set issue prefix (required for creating issues)
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create multiple issues outside the transaction first
	issues := []*types.Issue{
		{
			Title:       "Transaction search issue one",
			Description: "Testing transaction SearchIssues",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		},
		{
			Title:       "Transaction search issue two",
			Description: "More testing transaction SearchIssues",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		},
		{
			Title:       "Transaction search issue three",
			Description: "Even more testing transaction SearchIssues",
			Status:      types.StatusOpen,
			Priority:    3,
			IssueType:   types.TypeBug,
		},
	}

	for i, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %d: %v", i+1, err)
		}
		t.Logf("Created issue: %s", issue.ID)
	}

	// Now run SearchIssues within a transaction - this is where the bug is!
	// The transaction's SearchIssues (transaction.go:111) iterates over rows
	// and calls t.GetIssue(ctx, id) at line 146 WHILE rows are still open.
	// In MySQL server mode, this causes "driver: bad connection".
	t.Log("Calling SearchIssues within a transaction - this should trigger the bug...")

	err = store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// This calls doltTransaction.SearchIssues which has the nested query bug
		results, err := tx.SearchIssues(ctx, "Transaction search", types.IssueFilter{})
		if err != nil {
			return err
		}
		t.Logf("Transaction SearchIssues returned %d results", len(results))
		if len(results) != 3 {
			t.Errorf("expected 3 issues matching 'Transaction search', got %d", len(results))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	t.Log("Server mode transaction SearchIssues test passed")
}

// TestServerModeScanIssueIDs tests that scanIssueIDs correctly closes rows before
// calling GetIssuesByIDs in server mode.
//
// This test reproduces the second bug fixed in the "driver: bad connection" commit.
// The scanIssueIDs function (dependencies.go) iterates over rows, collects IDs, then
// calls GetIssuesByIDs. Without explicitly closing rows first, MySQL server mode fails
// because it can't have multiple active result sets on one connection.
//
// To reliably trigger this bug, we force single-connection behavior by setting
// MaxOpenConns(1) on the connection pool.
func TestServerModeScanIssueIDs(t *testing.T) {
	// Skip if dolt is not available
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping server mode test")
	}

	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "dolt-scanissueids-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize dolt repo
	cmd := exec.Command("dolt", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init dolt repo: %v", err)
	}

	// Start server on non-standard ports to avoid conflicts
	server := NewServer(ServerConfig{
		DataDir:        tmpDir,
		SQLPort:        13311,
		RemotesAPIPort: 18085,
		Host:           "127.0.0.1",
		LogFile:        filepath.Join(tmpDir, "server.log"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := server.Start(ctx); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() {
		if err := server.Stop(); err != nil {
			t.Logf("warning: failed to stop server: %v", err)
		}
	}()

	// Connect using server mode
	store, err := New(ctx, &Config{
		Path:       tmpDir,
		Database:   "beads",
		ServerMode: true,
		ServerHost: "127.0.0.1",
		ServerPort: 13311,
	})
	if err != nil {
		t.Fatalf("failed to create server mode store: %v", err)
	}
	defer store.Close()

	// CRITICAL: Force single-connection behavior to reliably trigger the bug.
	// With a connection pool, consecutive queries might use different connections,
	// masking the "multiple active result sets" issue. By setting MaxOpenConns(1),
	// we force all queries to use the same connection, which exposes the bug.
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)

	// Set issue prefix (required for creating issues)
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create a parent issue and multiple child issues with dependencies
	// This setup will exercise scanIssueIDs when we call GetDependents
	parent := &types.Issue{
		Title:       "Parent for scanIssueIDs test",
		Description: "This issue has multiple dependents",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("failed to create parent issue: %v", err)
	}
	t.Logf("Created parent issue: %s", parent.ID)

	// Create multiple children that depend on the parent
	// We need multiple to ensure scanIssueIDs has IDs to pass to GetIssuesByIDs
	for i := 0; i < 3; i++ {
		child := &types.Issue{
			Title:       "Child for scanIssueIDs test",
			Description: "This child depends on parent",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, child, "test"); err != nil {
			t.Fatalf("failed to create child issue %d: %v", i+1, err)
		}
		t.Logf("Created child issue: %s", child.ID)

		// Add dependency: child depends on parent (blocks relationship)
		dep := &types.Dependency{
			IssueID:     child.ID,
			DependsOnID: parent.ID,
			Type:        types.DepBlocks,
		}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}
	}

	// Now call GetDependents which uses scanIssueIDs internally.
	// The code path is:
	// 1. GetDependents queries for dependent issue IDs
	// 2. Passes rows to scanIssueIDs
	// 3. scanIssueIDs iterates rows, collects IDs
	// 4. scanIssueIDs calls GetIssuesByIDs (BUG: rows not closed yet!)
	// 5. GetIssuesByIDs fails with "driver: bad connection"
	t.Log("Calling GetDependents - this exercises scanIssueIDs -> GetIssuesByIDs path...")

	dependents, err := store.GetDependents(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetDependents failed with error: %v", err)
	}

	if len(dependents) != 3 {
		t.Errorf("expected 3 dependents, got %d", len(dependents))
	}

	// Also test GetDependencies which has the same pattern
	t.Log("Calling GetDependencies...")
	for _, dep := range dependents {
		deps, err := store.GetDependencies(ctx, dep.ID)
		if err != nil {
			t.Fatalf("GetDependencies failed for %s: %v", dep.ID, err)
		}
		if len(deps) != 1 {
			t.Errorf("expected 1 dependency for %s, got %d", dep.ID, len(deps))
		}
	}

	t.Log("Server mode scanIssueIDs test passed")
}
