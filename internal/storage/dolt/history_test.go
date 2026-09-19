package dolt

import (
	"errors"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// =============================================================================
// DoltStore.History Tests (the production path `bd history` actually calls —
// distinct from the unexported getIssueHistory below, which nothing calls)
// =============================================================================

// TestHistory_UsesDedicatedLongTimeoutConnection guards against regressing to
// the shared pool's 10s ReadTimeout (ga-ahnxx): a dolt_history_issues scan on
// an issue with many revisions can legitimately take longer than that,
// surfacing as an intermittent MySQL i/o timeout / invalid connection error
// even though bd show on the same id succeeds instantly (a fast point lookup
// on the live table, not the history table).
//
// store.db (the shared pool) is opened once at store-creation time with a
// baked-in DSN — mutating store.connStr afterward cannot affect it. Only a
// call path that re-parses store.connStr per invocation (openLongTimeoutConn,
// via withReadTxLongTimeout) is affected. So: break store.connStr after setup,
// then confirm History still routes through it (and fails) rather than
// silently falling back to the still-healthy shared pool.
func TestHistory_UsesDedicatedLongTimeoutConnection(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "history-timeout-test",
		Title:     "v1",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.Commit(ctx, "v1"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Sanity check: History works before we break anything, and it must
	// actually read the store's checked-out branch — setupTestStore checks
	// out an isolated test branch via a raw CALL DOLT_CHECKOUT on store.db
	// (testutil.StartTestBranch), which bypasses Store.Checkout and never
	// updates s.branch. If openLongTimeoutConn's fresh connection doesn't
	// also select that branch, this query silently reads the (schema-only,
	// issue-less) default branch instead and returns an empty result with a
	// nil error — a passing err check alone would not catch that.
	sanityHistory, err := store.History(ctx, issue.ID)
	if err != nil {
		t.Fatalf("History failed before connStr corruption: %v", err)
	}
	if len(sanityHistory) == 0 {
		t.Fatal("expected non-empty history for the created issue; got none — " +
			"History is likely reading the default branch instead of the " +
			"store's actual checked-out branch (see withReadTxLongTimeout)")
	}
	found := false
	for _, entry := range sanityHistory {
		if entry.Issue != nil && (entry.Issue.ID == issue.ID || entry.Issue.Title == issue.Title) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected history to contain the created issue %q (title %q), got entries: %+v",
			issue.ID, issue.Title, sanityHistory)
	}

	// Break store.connStr to an address that fails DNS resolution fast and
	// permanently (RFC 2606 .invalid TLD) — a clean signal distinct from
	// "connection refused", which the retry layer treats as transient and
	// would spend up to serverRetryMaxElapsed (30s) retrying.
	cfg, err := mysql.ParseDSN(store.connStr)
	if err != nil {
		t.Fatalf("failed to parse store.connStr: %v", err)
	}
	cfg.Addr = "ga-ahnxx-test.invalid:3306"
	store.connStr = cfg.FormatDSN()

	if _, err := store.History(ctx, issue.ID); err == nil {
		t.Fatal("expected History to fail after store.connStr was broken; " +
			"if it still succeeds, History is reading through the shared " +
			"pool (store.db) instead of a fresh connection via " +
			"openLongTimeoutConn/withReadTxLongTimeout, which is what lets " +
			"the pool's 10s ReadTimeout keep biting long dolt_history_issues scans")
	}
}

// =============================================================================
// getIssueHistory Tests
// =============================================================================

func TestGetIssueHistory(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an issue
	issue := &types.Issue{
		ID:          "history-test",
		Title:       "Original Title",
		Description: "Original description",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Commit the initial state
	if err := store.Commit(ctx, "Initial commit"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Update the issue
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"title":       "Updated Title",
		"description": "Updated description",
	}, "tester"); err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	// Commit the update
	if err := store.Commit(ctx, "Update commit"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Get history
	history, err := store.getIssueHistory(ctx, issue.ID)
	if err != nil {
		t.Fatalf("getIssueHistory failed: %v", err)
	}

	// Should have at least 2 history entries (initial + update)
	if len(history) < 2 {
		t.Errorf("expected at least 2 history entries, got %d", len(history))
	}

	// Most recent should have updated title
	if len(history) > 0 && history[0].Issue.Title != "Updated Title" {
		t.Errorf("expected most recent title 'Updated Title', got %q", history[0].Issue.Title)
	}
}

func TestGetIssueHistory_NonExistent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Get history for non-existent issue
	history, err := store.getIssueHistory(ctx, "nonexistent-id")
	if err != nil {
		t.Fatalf("getIssueHistory failed: %v", err)
	}

	if len(history) != 0 {
		t.Errorf("expected 0 history entries for non-existent issue, got %d", len(history))
	}
}

// =============================================================================
// getIssueAsOf Tests
// =============================================================================

func TestGetIssueAsOf(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an issue
	issue := &types.Issue{
		ID:          "asof-test",
		Title:       "Original Title",
		Description: "Original",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Commit initial state
	if err := store.Commit(ctx, "Initial state"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Get the initial commit hash
	initialHash, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("failed to get commit hash: %v", err)
	}

	// Update the issue
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"title": "Modified Title",
	}, "tester"); err != nil {
		t.Fatalf("failed to update: %v", err)
	}

	// Commit the change
	if err := store.Commit(ctx, "Modified state"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Query the issue as of the initial commit
	oldIssue, err := store.getIssueAsOf(ctx, issue.ID, initialHash)
	if err != nil {
		t.Fatalf("getIssueAsOf failed: %v", err)
	}

	if oldIssue == nil {
		t.Fatal("expected to find issue at historical commit")
	}

	if oldIssue.Title != "Original Title" {
		t.Errorf("expected historical title 'Original Title', got %q", oldIssue.Title)
	}

	// Current state should have modified title
	currentIssue, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get current issue: %v", err)
	}

	if currentIssue.Title != "Modified Title" {
		t.Errorf("expected current title 'Modified Title', got %q", currentIssue.Title)
	}
}

func TestGetIssueAsOf_InvalidRef(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Try with SQL injection attempt
	_, err := store.getIssueAsOf(ctx, "test-id", "'; DROP TABLE issues; --")
	if err == nil {
		t.Error("expected error for invalid ref, got nil")
	}
}

func TestGetIssueAsOf_NonExistentIssue(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create and commit something to have a valid ref
	issue := &types.Issue{
		ID:        "asof-other",
		Title:     "Other",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	if err := store.Commit(ctx, "Commit"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	hash, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("failed to get commit hash: %v", err)
	}

	// Query non-existent issue at valid commit
	_, err = store.getIssueAsOf(ctx, "nonexistent", hash)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent issue, got: %v", err)
	}
}

// =============================================================================
// getInternalConflicts Tests
// =============================================================================

func TestGetInternalConflicts_NoConflicts(t *testing.T) {
	// Skip: The dolt_conflicts system table schema varies by Dolt version.
	// Some versions use (table, num_conflicts), others use (table_name, num_conflicts).
	// This needs to be fixed in the implementation to handle version differences.
	t.Skip("Skipping: dolt_conflicts table schema varies by Dolt version")
}

// =============================================================================
// ResolveConflicts Tests
// =============================================================================

func TestResolveConflicts_InvalidTable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Try with SQL injection attempt
	err := store.ResolveConflicts(ctx, "issues; DROP TABLE", "ours")
	if err == nil {
		t.Error("expected error for invalid table name")
	}
}

func TestResolveConflicts_InvalidStrategy(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	err := store.ResolveConflicts(ctx, "issues", "invalid_strategy")
	if err == nil {
		t.Error("expected error for invalid strategy")
	}
}

// Note: TestValidateRef and TestValidateTableName are already defined in dolt_test.go
