package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestRunInTransactionBasic verifies the RunInTransaction method exists and
// can be called.
func TestRunInTransactionBasic(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Test that we can call RunInTransaction
	callCount := 0
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		callCount++
		return nil
	})

	if err != nil {
		t.Errorf("RunInTransaction returned error: %v", err)
	}

	if callCount != 1 {
		t.Errorf("expected callback to be called once, got %d", callCount)
	}
}

// TestRunInTransactionRollbackOnError verifies that returning an error
// from the callback does not cause a panic and the error is propagated.
func TestRunInTransactionRollbackOnError(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	expectedErr := "intentional test error"
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return &testError{msg: expectedErr}
	})

	if err == nil {
		t.Error("expected error to be returned, got nil")
	}

	if err.Error() != expectedErr {
		t.Errorf("expected error %q, got %q", expectedErr, err.Error())
	}
}

// TestRunInTransactionPanicRecovery verifies that panics in the callback
// are recovered and re-raised after rollback.
func TestRunInTransactionPanicRecovery(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic to be re-raised, but no panic occurred")
		} else if r != "test panic" {
			t.Errorf("unexpected panic value: %v", r)
		}
	}()

	_ = store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		panic("test panic")
	})

	t.Error("should not reach here - panic should have been re-raised")
}

// TestTransactionCreateIssue tests creating an issue within a transaction.
func TestTransactionCreateIssue(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	var createdID string
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		issue := &types.Issue{
			Title:     "Test Issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := tx.CreateIssue(ctx, issue, "test-actor"); err != nil {
			return err
		}
		createdID = issue.ID
		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	if createdID == "" {
		t.Error("expected issue ID to be set after creation")
	}

	// Verify issue exists after commit
	issue, err := store.GetIssue(ctx, createdID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if issue == nil {
		t.Error("expected issue to exist after transaction commit")
	}
	if issue.Title != "Test Issue" {
		t.Errorf("expected title 'Test Issue', got %q", issue.Title)
	}
}

// TestTransactionRollbackOnCreateError tests that issues are not created
// when transaction rolls back due to error.
func TestTransactionRollbackOnCreateError(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	var createdID string
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		issue := &types.Issue{
			Title:     "Test Issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := tx.CreateIssue(ctx, issue, "test-actor"); err != nil {
			return err
		}
		createdID = issue.ID

		// Return error to trigger rollback
		return &testError{msg: "intentional rollback"}
	})

	if err == nil {
		t.Error("expected error from transaction")
	}

	// Verify issue does NOT exist after rollback
	if createdID != "" {
		issue, err := store.GetIssue(ctx, createdID)
		if err != nil {
			t.Fatalf("GetIssue failed: %v", err)
		}
		if issue != nil {
			t.Error("expected issue to NOT exist after transaction rollback")
		}
	}
}

// TestTransactionMultipleIssues tests creating multiple issues atomically.
func TestTransactionMultipleIssues(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	var ids []string
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		for i := 0; i < 3; i++ {
			issue := &types.Issue{
				Title:     "Test Issue",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := tx.CreateIssue(ctx, issue, "test-actor"); err != nil {
				return err
			}
			ids = append(ids, issue.ID)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify all issues exist
	for _, id := range ids {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue failed for %s: %v", id, err)
		}
		if issue == nil {
			t.Errorf("expected issue %s to exist", id)
		}
	}
}

// TestTransactionUpdateIssue tests updating an issue within a transaction.
func TestTransactionUpdateIssue(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create issue first
	issue := &types.Issue{
		Title:     "Original Title",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Update in transaction
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return tx.UpdateIssue(ctx, issue.ID, map[string]interface{}{
			"title": "Updated Title",
		}, "test-actor")
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify update
	updated, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if updated.Title != "Updated Title" {
		t.Errorf("expected title 'Updated Title', got %q", updated.Title)
	}
}

// TestTransactionCloseIssue tests closing an issue within a transaction.
func TestTransactionCloseIssue(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create issue first
	issue := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Close in transaction
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return tx.CloseIssue(ctx, issue.ID, "Done", "test-actor", "")
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify closed
	closed, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if closed.Status != types.StatusClosed {
		t.Errorf("expected status 'closed', got %q", closed.Status)
	}
	if closed.CloseReason != "Done" {
		t.Errorf("expected close_reason 'Done', got %q", closed.CloseReason)
	}
}

// TestTransactionDeleteIssue tests deleting an issue within a transaction.
func TestTransactionDeleteIssue(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create issue first
	issue := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Delete in transaction
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return tx.DeleteIssue(ctx, issue.ID)
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify deleted
	deleted, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if deleted != nil {
		t.Error("expected issue to be deleted")
	}
}

// TestTransactionGetIssue tests read-your-writes within a transaction.
func TestTransactionGetIssue(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Create issue
		issue := &types.Issue{
			Title:     "Test Issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := tx.CreateIssue(ctx, issue, "test-actor"); err != nil {
			return err
		}

		// Read it back within same transaction (read-your-writes)
		retrieved, err := tx.GetIssue(ctx, issue.ID)
		if err != nil {
			return err
		}
		if retrieved == nil {
			t.Error("expected to read issue within transaction")
		}
		if retrieved.Title != "Test Issue" {
			t.Errorf("expected title 'Test Issue', got %q", retrieved.Title)
		}

		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}
}

// TestTransactionCreateIssues tests batch issue creation within a transaction.
func TestTransactionCreateIssues(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	var ids []string
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		issues := []*types.Issue{
			{Title: "Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
			{Title: "Issue 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{Title: "Issue 3", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask},
		}
		if err := tx.CreateIssues(ctx, issues, "test-actor"); err != nil {
			return err
		}
		for _, issue := range issues {
			ids = append(ids, issue.ID)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify all issues exist
	for i, id := range ids {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue failed for %s: %v", id, err)
		}
		if issue == nil {
			t.Errorf("expected issue %s to exist", id)
		}
		expectedTitle := "Issue " + string(rune('1'+i))
		if issue.Title != expectedTitle {
			t.Errorf("expected title %q, got %q", expectedTitle, issue.Title)
		}
	}
}

// TestTransactionAddDependency tests adding a dependency within a transaction.
func TestTransactionAddDependency(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create two issues first
	issue1 := &types.Issue{Title: "Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Issue 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue1, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add dependency in transaction
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		dep := &types.Dependency{
			IssueID:     issue1.ID,
			DependsOnID: issue2.ID,
			Type:        types.DepBlocks,
		}
		return tx.AddDependency(ctx, dep, "test-actor")
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify dependency exists
	deps, err := store.GetDependencies(ctx, issue1.ID)
	if err != nil {
		t.Fatalf("GetDependencies failed: %v", err)
	}
	if len(deps) != 1 {
		t.Errorf("expected 1 dependency, got %d", len(deps))
	}
	if deps[0].ID != issue2.ID {
		t.Errorf("expected dependency on %s, got %s", issue2.ID, deps[0].ID)
	}
}

// TestTransactionAddDependency_ParentChildCustomTypes tests that parent-child
// dependencies with custom issue types work in the transaction code path.
// This mirrors TestParentChildWithCustomTypes in dependencies_test.go to ensure
// both storage layers (direct and transaction) stay in sync after the type-based
// validation was removed.
func TestTransactionAddDependency_ParentChildCustomTypes(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Register custom types
	if err := store.SetConfig(ctx, "types.custom", "theme,shot"); err != nil {
		t.Fatalf("SetConfig for custom types failed: %v", err)
	}

	// Create issues with custom and built-in types
	theme := &types.Issue{Title: "shooter/ai", Status: types.StatusOpen, Priority: 1, IssueType: types.IssueType("theme")}
	epic := &types.Issue{Title: "Auth System", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	shot := &types.Issue{Title: "Quick fix", Status: types.StatusOpen, Priority: 1, IssueType: types.IssueType("shot")}

	if err := store.CreateIssue(ctx, theme, "test-actor"); err != nil {
		t.Fatalf("CreateIssue theme failed: %v", err)
	}
	if err := store.CreateIssue(ctx, epic, "test-actor"); err != nil {
		t.Fatalf("CreateIssue epic failed: %v", err)
	}
	if err := store.CreateIssue(ctx, shot, "test-actor"); err != nil {
		t.Fatalf("CreateIssue shot failed: %v", err)
	}

	// Add parent-child dependencies in transaction: theme → epic → shot
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Epic (child) depends on theme (parent)
		if err := tx.AddDependency(ctx, &types.Dependency{
			IssueID:     epic.ID,
			DependsOnID: theme.ID,
			Type:        types.DepParentChild,
		}, "test-actor"); err != nil {
			return fmt.Errorf("theme→epic parent-child failed: %w", err)
		}

		// Shot (child) depends on epic (parent)
		if err := tx.AddDependency(ctx, &types.Dependency{
			IssueID:     shot.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		}, "test-actor"); err != nil {
			return fmt.Errorf("epic→shot parent-child failed: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify dependencies
	epicDeps, err := store.GetDependencies(ctx, epic.ID)
	if err != nil {
		t.Fatalf("GetDependencies for epic failed: %v", err)
	}
	if len(epicDeps) != 1 || epicDeps[0].ID != theme.ID {
		t.Errorf("Expected epic to depend on theme, got %v", epicDeps)
	}

	shotDeps, err := store.GetDependencies(ctx, shot.ID)
	if err != nil {
		t.Fatalf("GetDependencies for shot failed: %v", err)
	}
	if len(shotDeps) != 1 || shotDeps[0].ID != epic.ID {
		t.Errorf("Expected shot to depend on epic, got %v", shotDeps)
	}
}

// TestTransactionAddDependency_RelatesTo tests that bidirectional relates-to
// dependencies work in transaction context. This is a regression test for
// Decision 004 Phase 4 - the cycle detection must exempt relates-to type
// since bidirectional relationships are semantically valid.
func TestTransactionAddDependency_RelatesTo(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create two issues
	issue1 := &types.Issue{Title: "Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Issue 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue1, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add bidirectional relates-to in a single transaction
	// This should NOT fail cycle detection since relates-to is exempt
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// First direction: issue1 relates-to issue2
		dep1 := &types.Dependency{
			IssueID:     issue1.ID,
			DependsOnID: issue2.ID,
			Type:        types.DepRelatesTo,
		}
		if err := tx.AddDependency(ctx, dep1, "test-actor"); err != nil {
			return fmt.Errorf("first relates-to failed: %w", err)
		}

		// Second direction: issue2 relates-to issue1 (would be a cycle for other types)
		dep2 := &types.Dependency{
			IssueID:     issue2.ID,
			DependsOnID: issue1.ID,
			Type:        types.DepRelatesTo,
		}
		if err := tx.AddDependency(ctx, dep2, "test-actor"); err != nil {
			return fmt.Errorf("second relates-to failed: %w", err)
		}

		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify both directions exist
	deps1, err := store.GetDependenciesWithMetadata(ctx, issue1.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}
	found1 := false
	for _, d := range deps1 {
		if d.ID == issue2.ID && d.DependencyType == types.DepRelatesTo {
			found1 = true
		}
	}
	if !found1 {
		t.Errorf("issue1 should have relates-to link to issue2")
	}

	deps2, err := store.GetDependenciesWithMetadata(ctx, issue2.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}
	found2 := false
	for _, d := range deps2 {
		if d.ID == issue1.ID && d.DependencyType == types.DepRelatesTo {
			found2 = true
		}
	}
	if !found2 {
		t.Errorf("issue2 should have relates-to link to issue1")
	}
}

// TestTransactionAddDependency_RepliesTo tests that replies-to dependencies
// preserve thread_id in transaction context (Decision 004 Phase 4).
func TestTransactionAddDependency_RepliesTo(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create original message and reply
	original := &types.Issue{
		Title:     "Original Message",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: "message",
		Sender:    "alice",
	}
	reply := &types.Issue{
		Title:     "Re: Original Message",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: "message",
		Sender:    "bob",
	}
	if err := store.CreateIssue(ctx, original, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if err := store.CreateIssue(ctx, reply, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add replies-to with thread_id in transaction
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		dep := &types.Dependency{
			IssueID:     reply.ID,
			DependsOnID: original.ID,
			Type:        types.DepRepliesTo,
			ThreadID:    original.ID, // Thread root
		}
		return tx.AddDependency(ctx, dep, "test-actor")
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify the dependency and thread_id were preserved
	deps, err := store.GetDependenciesWithMetadata(ctx, reply.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}
	found := false
	for _, d := range deps {
		if d.ID == original.ID && d.DependencyType == types.DepRepliesTo {
			found = true
		}
	}
	if !found {
		t.Errorf("reply should have replies-to link to original")
	}

	// Verify thread_id by querying dependencies table directly
	var threadID string
	err = store.UnderlyingDB().QueryRowContext(ctx,
		`SELECT thread_id FROM dependencies WHERE issue_id = ? AND depends_on_id = ?`,
		reply.ID, original.ID).Scan(&threadID)
	if err != nil {
		t.Fatalf("Failed to query thread_id: %v", err)
	}
	if threadID != original.ID {
		t.Errorf("thread_id = %q, want %q", threadID, original.ID)
	}
}

// TestTransactionRemoveDependency tests removing a dependency within a transaction.
func TestTransactionRemoveDependency(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create two issues and add dependency
	issue1 := &types.Issue{Title: "Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Issue 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue1, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	dep := &types.Dependency{IssueID: issue1.ID, DependsOnID: issue2.ID, Type: types.DepBlocks}
	if err := store.AddDependency(ctx, dep, "test-actor"); err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	// Remove dependency in transaction
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return tx.RemoveDependency(ctx, issue1.ID, issue2.ID, "test-actor")
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify dependency is gone
	deps, err := store.GetDependencies(ctx, issue1.ID)
	if err != nil {
		t.Fatalf("GetDependencies failed: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("expected 0 dependencies, got %d", len(deps))
	}
}

// TestTransactionAddLabel tests adding a label within a transaction.
func TestTransactionAddLabel(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create issue first
	issue := &types.Issue{Title: "Test Issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add label in transaction
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return tx.AddLabel(ctx, issue.ID, "test-label", "test-actor")
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify label exists
	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetLabels failed: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label, got %d", len(labels))
	}
	if labels[0] != "test-label" {
		t.Errorf("expected label 'test-label', got %s", labels[0])
	}
}

// TestTransactionRemoveLabel tests removing a label within a transaction.
func TestTransactionRemoveLabel(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create issue and add label
	issue := &types.Issue{Title: "Test Issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if err := store.AddLabel(ctx, issue.ID, "test-label", "test-actor"); err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}

	// Remove label in transaction
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return tx.RemoveLabel(ctx, issue.ID, "test-label", "test-actor")
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify label is gone
	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetLabels failed: %v", err)
	}
	if len(labels) != 0 {
		t.Errorf("expected 0 labels, got %d", len(labels))
	}
}

// TestTransactionAtomicIssueWithDependency tests creating issue + adding dependency atomically.
func TestTransactionAtomicIssueWithDependency(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create parent issue first
	parent := &types.Issue{Title: "Parent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, parent, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	var childID string
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Create child issue
		child := &types.Issue{Title: "Child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := tx.CreateIssue(ctx, child, "test-actor"); err != nil {
			return err
		}
		childID = child.ID

		// Add dependency: child blocks parent (child must be done before parent)
		dep := &types.Dependency{
			IssueID:     parent.ID,
			DependsOnID: child.ID,
			Type:        types.DepBlocks,
		}
		return tx.AddDependency(ctx, dep, "test-actor")
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify both issue and dependency exist
	child, err := store.GetIssue(ctx, childID)
	if err != nil || child == nil {
		t.Error("expected child issue to exist")
	}

	deps, err := store.GetDependencies(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetDependencies failed: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != childID {
		t.Error("expected dependency from parent to child")
	}
}

// TestTransactionAtomicIssueWithLabels tests creating issue + adding labels atomically.
func TestTransactionAtomicIssueWithLabels(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	var issueID string
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Create issue
		issue := &types.Issue{Title: "Test Issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		if err := tx.CreateIssue(ctx, issue, "test-actor"); err != nil {
			return err
		}
		issueID = issue.ID

		// Add multiple labels
		for _, label := range []string{"label1", "label2", "label3"} {
			if err := tx.AddLabel(ctx, issue.ID, label, "test-actor"); err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify issue and all labels exist
	issue, err := store.GetIssue(ctx, issueID)
	if err != nil || issue == nil {
		t.Error("expected issue to exist")
	}

	labels, err := store.GetLabels(ctx, issueID)
	if err != nil {
		t.Fatalf("GetLabels failed: %v", err)
	}
	if len(labels) != 3 {
		t.Errorf("expected 3 labels, got %d", len(labels))
	}
}

// TestTransactionEmpty tests that an empty transaction commits successfully.
func TestTransactionEmpty(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Do nothing - empty transaction
		return nil
	})

	if err != nil {
		t.Errorf("empty transaction should succeed, got error: %v", err)
	}
}

// TestTransactionConcurrent tests multiple concurrent transactions.
func TestTransactionConcurrent(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	const numGoroutines = 10
	errors := make(chan error, numGoroutines)
	ids := make(chan string, numGoroutines)

	// Launch concurrent transactions
	for i := 0; i < numGoroutines; i++ {
		go func(index int) {
			err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
				issue := &types.Issue{
					Title:     "Concurrent Issue",
					Status:    types.StatusOpen,
					Priority:  index % 4,
					IssueType: types.TypeTask,
				}
				if err := tx.CreateIssue(ctx, issue, "test-actor"); err != nil {
					return err
				}
				ids <- issue.ID
				return nil
			})
			errors <- err
		}(i)
	}

	// Collect results
	var errs []error
	var createdIDs []string
	for i := 0; i < numGoroutines; i++ {
		if err := <-errors; err != nil {
			errs = append(errs, err)
		}
	}
	close(ids)
	for id := range ids {
		createdIDs = append(createdIDs, id)
	}

	if len(errs) > 0 {
		t.Errorf("some transactions failed: %v", errs)
	}

	if len(createdIDs) != numGoroutines {
		t.Errorf("expected %d issues created, got %d", numGoroutines, len(createdIDs))
	}

	// Verify all issues exist
	for _, id := range createdIDs {
		issue, err := store.GetIssue(ctx, id)
		if err != nil || issue == nil {
			t.Errorf("expected issue %s to exist", id)
		}
	}
}

// TestTransactionNestedFailure tests that when first op succeeds but second fails,
// both are rolled back.
func TestTransactionNestedFailure(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	var firstIssueID string
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// First operation succeeds
		issue1 := &types.Issue{
			Title:     "First Issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := tx.CreateIssue(ctx, issue1, "test-actor"); err != nil {
			return err
		}
		firstIssueID = issue1.ID

		// Second operation fails
		issue2 := &types.Issue{
			Title:    "", // Invalid - missing title
			Status:   types.StatusOpen,
			Priority: 2,
		}
		return tx.CreateIssue(ctx, issue2, "test-actor")
	})

	if err == nil {
		t.Error("expected error from invalid second issue")
	}

	// Verify first issue was NOT created (rolled back)
	if firstIssueID != "" {
		issue, err := store.GetIssue(ctx, firstIssueID)
		if err != nil {
			t.Fatalf("GetIssue failed: %v", err)
		}
		if issue != nil {
			t.Error("expected first issue to be rolled back, but it exists")
		}
	}
}

// TestTransactionAtomicPlanApproval simulates a VC plan approval workflow:
// creating multiple issues with dependencies and labels atomically.
func TestTransactionAtomicPlanApproval(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	var epicID, task1ID, task2ID string
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Create epic
		epic := &types.Issue{
			Title:     "Epic: Feature Implementation",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeEpic,
		}
		if err := tx.CreateIssue(ctx, epic, "test-actor"); err != nil {
			return err
		}
		epicID = epic.ID

		// Create task 1
		task1 := &types.Issue{
			Title:     "Task 1: Setup",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := tx.CreateIssue(ctx, task1, "test-actor"); err != nil {
			return err
		}
		task1ID = task1.ID

		// Create task 2
		task2 := &types.Issue{
			Title:     "Task 2: Implementation",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := tx.CreateIssue(ctx, task2, "test-actor"); err != nil {
			return err
		}
		task2ID = task2.ID

		// Add dependencies: task2 depends on task1
		dep := &types.Dependency{
			IssueID:     task2ID,
			DependsOnID: task1ID,
			Type:        types.DepBlocks,
		}
		if err := tx.AddDependency(ctx, dep, "test-actor"); err != nil {
			return err
		}

		// Add labels to all issues
		for _, id := range []string{epicID, task1ID, task2ID} {
			if err := tx.AddLabel(ctx, id, "feature-x", "test-actor"); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify all issues exist
	for _, id := range []string{epicID, task1ID, task2ID} {
		issue, err := store.GetIssue(ctx, id)
		if err != nil || issue == nil {
			t.Errorf("expected issue %s to exist", id)
		}
	}

	// Verify dependency
	deps, err := store.GetDependencies(ctx, task2ID)
	if err != nil {
		t.Fatalf("GetDependencies failed: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != task1ID {
		t.Error("expected task2 to depend on task1")
	}

	// Verify labels
	for _, id := range []string{epicID, task1ID, task2ID} {
		labels, err := store.GetLabels(ctx, id)
		if err != nil {
			t.Fatalf("GetLabels failed: %v", err)
		}
		if len(labels) != 1 || labels[0] != "feature-x" {
			t.Errorf("expected 'feature-x' label on %s", id)
		}
	}
}

// TestTransactionSetConfig tests setting a config value within a transaction.
func TestTransactionSetConfig(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return tx.SetConfig(ctx, "test.key", "test-value")
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify config was set
	value, err := store.GetConfig(ctx, "test.key")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}
	if value != "test-value" {
		t.Errorf("expected 'test-value', got %q", value)
	}
}

// TestTransactionGetConfig tests reading config within a transaction (read-your-writes).
func TestTransactionGetConfig(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Set config
		if err := tx.SetConfig(ctx, "test.key", "test-value"); err != nil {
			return err
		}

		// Read it back within same transaction
		value, err := tx.GetConfig(ctx, "test.key")
		if err != nil {
			return err
		}
		if value != "test-value" {
			t.Errorf("expected 'test-value' within transaction, got %q", value)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}
}

// TestTransactionConfigRollback tests that config changes are rolled back on error.
func TestTransactionConfigRollback(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		if err := tx.SetConfig(ctx, "test.key", "test-value"); err != nil {
			return err
		}
		return &testError{msg: "intentional rollback"}
	})

	if err == nil {
		t.Error("expected error from transaction")
	}

	// Verify config was NOT set (rolled back)
	value, err := store.GetConfig(ctx, "test.key")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}
	if value != "" {
		t.Errorf("expected empty value after rollback, got %q", value)
	}
}

// TestTransactionSetMetadata tests setting a metadata value within a transaction.
func TestTransactionSetMetadata(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return tx.SetMetadata(ctx, "test.metadata", "metadata-value")
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify metadata was set
	value, err := store.GetMetadata(ctx, "test.metadata")
	if err != nil {
		t.Fatalf("GetMetadata failed: %v", err)
	}
	if value != "metadata-value" {
		t.Errorf("expected 'metadata-value', got %q", value)
	}
}

// TestTransactionGetMetadata tests reading metadata within a transaction (read-your-writes).
func TestTransactionGetMetadata(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Set metadata
		if err := tx.SetMetadata(ctx, "test.metadata", "metadata-value"); err != nil {
			return err
		}

		// Read it back within same transaction
		value, err := tx.GetMetadata(ctx, "test.metadata")
		if err != nil {
			return err
		}
		if value != "metadata-value" {
			t.Errorf("expected 'metadata-value' within transaction, got %q", value)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}
}

// TestTransactionMetadataRollback tests that metadata changes are rolled back on error.
func TestTransactionMetadataRollback(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		if err := tx.SetMetadata(ctx, "test.metadata", "metadata-value"); err != nil {
			return err
		}
		return &testError{msg: "intentional rollback"}
	})

	if err == nil {
		t.Error("expected error from transaction")
	}

	// Verify metadata was NOT set (rolled back)
	value, err := store.GetMetadata(ctx, "test.metadata")
	if err != nil {
		t.Fatalf("GetMetadata failed: %v", err)
	}
	if value != "" {
		t.Errorf("expected empty value after rollback, got %q", value)
	}
}

// TestTransactionAddComment tests adding a comment within a transaction.
func TestTransactionAddComment(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create issue first
	issue := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add comment in transaction
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return tx.AddComment(ctx, issue.ID, "commenter", "This is a test comment")
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify comment exists via events
	events, err := store.GetEvents(ctx, issue.ID, 10)
	if err != nil {
		t.Fatalf("GetEvents failed: %v", err)
	}

	found := false
	for _, e := range events {
		if e.EventType == types.EventCommented && e.Comment != nil && *e.Comment == "This is a test comment" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected comment event to exist")
	}
}

// TestTransactionAddCommentToCreatedIssue tests adding a comment to an issue created in the same transaction.
func TestTransactionAddCommentToCreatedIssue(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	var issueID string
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Create issue
		issue := &types.Issue{
			Title:     "Test Issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := tx.CreateIssue(ctx, issue, "test-actor"); err != nil {
			return err
		}
		issueID = issue.ID

		// Add comment to the issue we just created
		return tx.AddComment(ctx, issue.ID, "commenter", "Comment on new issue")
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify both issue and comment exist
	issue, err := store.GetIssue(ctx, issueID)
	if err != nil || issue == nil {
		t.Error("expected issue to exist")
	}

	events, err := store.GetEvents(ctx, issueID, 10)
	if err != nil {
		t.Fatalf("GetEvents failed: %v", err)
	}

	found := false
	for _, e := range events {
		if e.EventType == types.EventCommented && e.Comment != nil && *e.Comment == "Comment on new issue" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected comment event to exist")
	}
}

// TestTransactionAddCommentNonexistentIssue tests that adding a comment to a nonexistent issue fails.
func TestTransactionAddCommentNonexistentIssue(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return tx.AddComment(ctx, "nonexistent-id", "commenter", "This should fail")
	})

	if err == nil {
		t.Error("expected error when commenting on nonexistent issue")
	}
}

// TestTransactionCommentRollback tests that comments are rolled back on error.
func TestTransactionCommentRollback(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create issue first
	issue := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		if err := tx.AddComment(ctx, issue.ID, "commenter", "This comment should be rolled back"); err != nil {
			return err
		}
		return &testError{msg: "intentional rollback"}
	})

	if err == nil {
		t.Error("expected error from transaction")
	}

	// Verify comment was NOT added (rolled back)
	events, err := store.GetEvents(ctx, issue.ID, 10)
	if err != nil {
		t.Fatalf("GetEvents failed: %v", err)
	}

	for _, e := range events {
		if e.EventType == types.EventCommented {
			t.Error("expected no comment events after rollback")
		}
	}
}

// TestTransactionAtomicConfigWithIssue tests atomically creating an issue and setting config.
func TestTransactionAtomicConfigWithIssue(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	var issueID string
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Create issue
		issue := &types.Issue{
			Title:     "Test Issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := tx.CreateIssue(ctx, issue, "test-actor"); err != nil {
			return err
		}
		issueID = issue.ID

		// Set config referencing the issue
		if err := tx.SetConfig(ctx, "last_created_issue", issue.ID); err != nil {
			return err
		}

		// Set metadata
		if err := tx.SetMetadata(ctx, "import_marker", "test-import-123"); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify all three operations succeeded
	issue, err := store.GetIssue(ctx, issueID)
	if err != nil || issue == nil {
		t.Error("expected issue to exist")
	}

	configValue, err := store.GetConfig(ctx, "last_created_issue")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}
	if configValue != issueID {
		t.Errorf("expected config value %q, got %q", issueID, configValue)
	}

	metadataValue, err := store.GetMetadata(ctx, "import_marker")
	if err != nil {
		t.Fatalf("GetMetadata failed: %v", err)
	}
	if metadataValue != "test-import-123" {
		t.Errorf("expected metadata value 'test-import-123', got %q", metadataValue)
	}
}

// TestTransactionConfigOverwrite tests that SetConfig overwrites existing values.
func TestTransactionConfigOverwrite(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Set initial value
	if err := store.SetConfig(ctx, "test.key", "initial"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// Overwrite in transaction
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return tx.SetConfig(ctx, "test.key", "updated")
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify overwrite
	value, err := store.GetConfig(ctx, "test.key")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}
	if value != "updated" {
		t.Errorf("expected 'updated', got %q", value)
	}
}

// TestTransactionGetConfigNonexistent tests getting a nonexistent config key returns empty string.
func TestTransactionGetConfigNonexistent(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		value, err := tx.GetConfig(ctx, "nonexistent.key")
		if err != nil {
			return err
		}
		if value != "" {
			t.Errorf("expected empty string for nonexistent key, got %q", value)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}
}

// TestTransactionGetMetadataNonexistent tests getting a nonexistent metadata key returns empty string.
func TestTransactionGetMetadataNonexistent(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		value, err := tx.GetMetadata(ctx, "nonexistent.metadata")
		if err != nil {
			return err
		}
		if value != "" {
			t.Errorf("expected empty string for nonexistent metadata, got %q", value)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}
}

// testError is a simple error type for testing
type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

// TestTransactionSearchIssuesBasic tests basic search within a transaction.
func TestTransactionSearchIssuesBasic(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create some issues first
	closedAt := time.Now()
	issues := []*types.Issue{
		{Title: "Alpha task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{Title: "Beta task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{Title: "Gamma feature", Status: types.StatusClosed, Priority: 3, IssueType: types.TypeFeature, ClosedAt: &closedAt},
	}
	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Search within transaction
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Search by query
		results, err := tx.SearchIssues(ctx, "task", types.IssueFilter{})
		if err != nil {
			return err
		}
		if len(results) != 2 {
			t.Errorf("expected 2 issues matching 'task', got %d", len(results))
		}

		// Search by status
		closedStatus := types.StatusClosed
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{Status: &closedStatus})
		if err != nil {
			return err
		}
		if len(results) != 1 {
			t.Errorf("expected 1 closed issue, got %d", len(results))
		}

		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}
}

// TestTransactionSearchIssuesReadYourWrites is the KEY test: create an issue and search
// for it within the same transaction (read-your-writes consistency).
func TestTransactionSearchIssuesReadYourWrites(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create an existing issue outside the transaction
	existingIssue := &types.Issue{
		Title:     "Existing Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, existingIssue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	var newIssueID string
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Create a new issue within the transaction
		newIssue := &types.Issue{
			Title:       "Unique Searchable Title XYZ123",
			Description: "This has special content ABC789",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeFeature,
		}
		if err := tx.CreateIssue(ctx, newIssue, "test-actor"); err != nil {
			return err
		}
		newIssueID = newIssue.ID

		// CRITICAL: Search for the just-created issue by title
		results, err := tx.SearchIssues(ctx, "XYZ123", types.IssueFilter{})
		if err != nil {
			return err
		}
		if len(results) != 1 {
			t.Errorf("read-your-writes FAILED: expected 1 issue with title 'XYZ123', got %d", len(results))
			return nil
		}
		if results[0].ID != newIssueID {
			t.Errorf("read-your-writes FAILED: found wrong issue, expected %s, got %s", newIssueID, results[0].ID)
		}

		// Search for it by description
		results, err = tx.SearchIssues(ctx, "ABC789", types.IssueFilter{})
		if err != nil {
			return err
		}
		if len(results) != 1 {
			t.Errorf("read-your-writes FAILED: expected 1 issue with description 'ABC789', got %d", len(results))
		}

		// Search by type filter
		featureType := types.TypeFeature
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{IssueType: &featureType})
		if err != nil {
			return err
		}
		if len(results) != 1 {
			t.Errorf("read-your-writes FAILED: expected 1 feature type issue, got %d", len(results))
		}

		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}

	// Verify the issue was committed
	issue, err := store.GetIssue(ctx, newIssueID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if issue == nil {
		t.Error("expected issue to be committed, but it wasn't found")
	}
}

// TestTransactionSearchIssuesWithFilters tests various filter options within transaction.
func TestTransactionSearchIssuesWithFilters(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	closedAt := time.Now()
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Create issues with different attributes
		issues := []*types.Issue{
			{Title: "P1 Bug", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug, Assignee: "alice"},
			{Title: "P2 Task", Status: types.StatusInProgress, Priority: 2, IssueType: types.TypeTask, Assignee: "bob"},
			{Title: "P3 Feature", Status: types.StatusClosed, Priority: 3, IssueType: types.TypeFeature, Assignee: "alice", ClosedAt: &closedAt},
		}
		for _, issue := range issues {
			if err := tx.CreateIssue(ctx, issue, "test-actor"); err != nil {
				return err
			}
		}

		// Filter by assignee
		assignee := "alice"
		results, err := tx.SearchIssues(ctx, "", types.IssueFilter{Assignee: &assignee})
		if err != nil {
			return err
		}
		if len(results) != 2 {
			t.Errorf("expected 2 issues assigned to alice, got %d", len(results))
		}

		// Filter by priority
		priority := 1
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{Priority: &priority})
		if err != nil {
			return err
		}
		if len(results) != 1 {
			t.Errorf("expected 1 P1 issue, got %d", len(results))
		}

		// Filter by type
		bugType := types.TypeBug
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{IssueType: &bugType})
		if err != nil {
			return err
		}
		if len(results) != 1 {
			t.Errorf("expected 1 bug, got %d", len(results))
		}

		// Combined filter: status + assignee
		inProgressStatus := types.StatusInProgress
		bobAssignee := "bob"
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{
			Status:   &inProgressStatus,
			Assignee: &bobAssignee,
		})
		if err != nil {
			return err
		}
		if len(results) != 1 {
			t.Errorf("expected 1 in_progress issue assigned to bob, got %d", len(results))
		}

		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}
}

// TestTransactionSearchIssuesWithLabels tests label filtering within transaction.
func TestTransactionSearchIssuesWithLabels(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Create issues
		issue1 := &types.Issue{Title: "Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		issue2 := &types.Issue{Title: "Issue 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := tx.CreateIssue(ctx, issue1, "test-actor"); err != nil {
			return err
		}
		if err := tx.CreateIssue(ctx, issue2, "test-actor"); err != nil {
			return err
		}

		// Add labels
		if err := tx.AddLabel(ctx, issue1.ID, "frontend", "test-actor"); err != nil {
			return err
		}
		if err := tx.AddLabel(ctx, issue1.ID, "urgent", "test-actor"); err != nil {
			return err
		}
		if err := tx.AddLabel(ctx, issue2.ID, "backend", "test-actor"); err != nil {
			return err
		}

		// Search by label (must have ALL labels)
		results, err := tx.SearchIssues(ctx, "", types.IssueFilter{Labels: []string{"frontend"}})
		if err != nil {
			return err
		}
		if len(results) != 1 {
			t.Errorf("expected 1 issue with 'frontend' label, got %d", len(results))
		}
		if len(results) > 0 && results[0].ID != issue1.ID {
			t.Errorf("expected issue1, got %s", results[0].ID)
		}

		// Verify labels are attached to the issue
		if len(results) > 0 && len(results[0].Labels) != 2 {
			t.Errorf("expected 2 labels on issue, got %d", len(results[0].Labels))
		}

		// Search by multiple labels (AND)
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{Labels: []string{"frontend", "urgent"}})
		if err != nil {
			return err
		}
		if len(results) != 1 {
			t.Errorf("expected 1 issue with both 'frontend' and 'urgent' labels, got %d", len(results))
		}

		// Search by any label (OR)
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{LabelsAny: []string{"frontend", "backend"}})
		if err != nil {
			return err
		}
		if len(results) != 2 {
			t.Errorf("expected 2 issues with either 'frontend' or 'backend' label, got %d", len(results))
		}

		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}
}

// TestTransactionSearchIssuesRollback verifies uncommitted issues aren't visible outside transaction.
func TestTransactionSearchIssuesRollback(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Try to create an issue but rollback (by returning an error)
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		issue := &types.Issue{
			Title:     "RollbackTestIssue999",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := tx.CreateIssue(ctx, issue, "test-actor"); err != nil {
			return err
		}

		// Verify it's visible within the transaction
		results, err := tx.SearchIssues(ctx, "RollbackTestIssue999", types.IssueFilter{})
		if err != nil {
			return err
		}
		if len(results) != 1 {
			t.Errorf("expected issue to be visible within transaction, got %d results", len(results))
		}

		// Return error to trigger rollback
		return &testError{msg: "intentional rollback"}
	})

	if err == nil {
		t.Fatal("expected error from rollback, got nil")
	}

	// Verify the issue is NOT visible outside the transaction (it was rolled back)
	results, err := store.SearchIssues(ctx, "RollbackTestIssue999", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 issues after rollback, got %d - rollback didn't work!", len(results))
	}
}

// TestTransactionSearchIssuesLimit tests the limit filter within transaction.
func TestTransactionSearchIssuesLimit(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Create several issues
		for i := 0; i < 10; i++ {
			issue := &types.Issue{
				Title:     "Limit Test Issue",
				Status:    types.StatusOpen,
				Priority:  i % 5,
				IssueType: types.TypeTask,
			}
			if err := tx.CreateIssue(ctx, issue, "test-actor"); err != nil {
				return err
			}
		}

		// Search with limit
		results, err := tx.SearchIssues(ctx, "", types.IssueFilter{Limit: 3})
		if err != nil {
			return err
		}
		if len(results) != 3 {
			t.Errorf("expected 3 issues with limit, got %d", len(results))
		}

		// Search without limit should return all
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			return err
		}
		if len(results) != 10 {
			t.Errorf("expected 10 issues without limit, got %d", len(results))
		}

		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}
}

// TestTransactionSearchIssuesWithPriorityRange tests priority range filters within transaction.
func TestTransactionSearchIssuesWithPriorityRange(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Create issues with different priorities
		for i := 0; i < 5; i++ {
			issue := &types.Issue{
				Title:     "Priority Range Test",
				Status:    types.StatusOpen,
				Priority:  i, // P0, P1, P2, P3, P4
				IssueType: types.TypeTask,
			}
			if err := tx.CreateIssue(ctx, issue, "test-actor"); err != nil {
				return err
			}
		}

		// Filter by PriorityMin only (P2 and higher priority = lower number)
		minPriority := 2
		results, err := tx.SearchIssues(ctx, "", types.IssueFilter{PriorityMin: &minPriority})
		if err != nil {
			return err
		}
		// Should get P2, P3, P4
		if len(results) != 3 {
			t.Errorf("expected 3 issues with priority >= 2, got %d", len(results))
		}

		// Filter by PriorityMax only
		maxPriority := 1
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{PriorityMax: &maxPriority})
		if err != nil {
			return err
		}
		// Should get P0, P1
		if len(results) != 2 {
			t.Errorf("expected 2 issues with priority <= 1, got %d", len(results))
		}

		// Filter by priority range
		minP := 1
		maxP := 3
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{PriorityMin: &minP, PriorityMax: &maxP})
		if err != nil {
			return err
		}
		// Should get P1, P2, P3
		if len(results) != 3 {
			t.Errorf("expected 3 issues with priority 1-3, got %d", len(results))
		}

		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}
}

// TestTransactionSearchIssuesWithDateRange tests date range filters within transaction.
func TestTransactionSearchIssuesWithDateRange(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		now := time.Now()
		past := now.Add(-48 * time.Hour)
		future := now.Add(24 * time.Hour)

		// Create issues - CreatedAt is set automatically
		issue1 := &types.Issue{
			Title:     "Recent Issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := tx.CreateIssue(ctx, issue1, "test-actor"); err != nil {
			return err
		}

		// Filter by CreatedAfter (should find the issue created just now)
		createdAfter := past
		results, err := tx.SearchIssues(ctx, "", types.IssueFilter{CreatedAfter: &createdAfter})
		if err != nil {
			return err
		}
		if len(results) != 1 {
			t.Errorf("expected 1 issue created after past, got %d", len(results))
		}

		// Filter by CreatedBefore with future time (should find the issue)
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{CreatedBefore: &future})
		if err != nil {
			return err
		}
		if len(results) != 1 {
			t.Errorf("expected 1 issue created before future, got %d", len(results))
		}

		// Filter by CreatedBefore with past time (should find nothing)
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{CreatedBefore: &past})
		if err != nil {
			return err
		}
		if len(results) != 0 {
			t.Errorf("expected 0 issues created before past, got %d", len(results))
		}

		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}
}

// TestTransactionSearchIssuesWithIDs tests IDs filter within transaction.
func TestTransactionSearchIssuesWithIDs(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Create several issues and collect their IDs
		var issueIDs []string
		for i := 0; i < 5; i++ {
			issue := &types.Issue{
				Title:     "IDs Filter Test",
				Status:    types.StatusOpen,
				Priority:  i,
				IssueType: types.TypeTask,
			}
			if err := tx.CreateIssue(ctx, issue, "test-actor"); err != nil {
				return err
			}
			issueIDs = append(issueIDs, issue.ID)
		}

		// Filter by specific IDs (first 2)
		results, err := tx.SearchIssues(ctx, "", types.IssueFilter{IDs: issueIDs[:2]})
		if err != nil {
			return err
		}
		if len(results) != 2 {
			t.Errorf("expected 2 issues when filtering by 2 IDs, got %d", len(results))
		}

		// Filter by single ID
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{IDs: []string{issueIDs[0]}})
		if err != nil {
			return err
		}
		if len(results) != 1 {
			t.Errorf("expected 1 issue when filtering by 1 ID, got %d", len(results))
		}
		if results[0].ID != issueIDs[0] {
			t.Errorf("expected issue ID %s, got %s", issueIDs[0], results[0].ID)
		}

		// Filter by non-existent ID
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{IDs: []string{"nonexistent-id"}})
		if err != nil {
			return err
		}
		if len(results) != 0 {
			t.Errorf("expected 0 issues for non-existent ID, got %d", len(results))
		}

		// Empty IDs filter should return all issues
		results, err = tx.SearchIssues(ctx, "", types.IssueFilter{IDs: []string{}})
		if err != nil {
			return err
		}
		if len(results) != 5 {
			t.Errorf("expected 5 issues with empty IDs filter, got %d", len(results))
		}

		return nil
	})

	if err != nil {
		t.Fatalf("RunInTransaction failed: %v", err)
	}
}
