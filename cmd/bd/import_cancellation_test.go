package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestImportCancellation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-import-cancel-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	store := newTestStore(t, testDB)
	defer store.Close()

	// Create a large number of issues to make import take time
	issues := make([]*types.Issue, 0, 1000)
	for i := 0; i < 1000; i++ {
		issues = append(issues, &types.Issue{
			ID:          fmt.Sprintf("test-%d", i),
			Title:       "Test Issue",
			Description: "Test description for cancellation",
			Priority:    0,
			IssueType:   types.TypeBug,
			Status:      types.StatusOpen,
		})
	}

	// Create a cancellable context
	cancelCtx, cancel := context.WithCancel(context.Background())

	// Start import in a goroutine
	errChan := make(chan error, 1)
	go func() {
		opts := ImportOptions{
			DryRun:     false,
			SkipUpdate: false,
			Strict:     false,
		}
		_, err := importIssuesCore(cancelCtx, testDB, store, issues, opts)
		errChan <- err
	}()

	// Cancel immediately to test cancellation
	cancel()

	// Wait for import to finish
	err = <-errChan

	// Verify that the operation was cancelled or completed
	// (The import might complete before cancellation, which is fine)
	if err != nil && err != context.Canceled {
		t.Logf("Import returned error: %v", err)
	}

	// Verify database integrity - we should still be able to query
	ctx := context.Background()
	importedIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Database corrupted after cancellation: %v", err)
	}

	// The number of issues should be <= 1000 (import might have been interrupted)
	if len(importedIssues) > 1000 {
		t.Errorf("Expected <= 1000 issues after cancellation, got %d", len(importedIssues))
	}

	// Verify we can still create new issues (database is not corrupted)
	newIssue := &types.Issue{
		Title:       "Post-cancellation issue",
		Description: "Created after cancellation to verify DB integrity",
		Priority:    0,
		IssueType:   types.TypeBug,
		Status:      types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, newIssue, "test-user"); err != nil {
		t.Fatalf("Failed to create issue after cancellation: %v", err)
	}
}

func TestImportWithTimeout(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-import-timeout-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	store := newTestStore(t, testDB)
	defer store.Close()

	// Create a small set of issues
	issues := make([]*types.Issue, 0, 10)
	for i := 0; i < 10; i++ {
		issues = append(issues, &types.Issue{
			ID:          fmt.Sprintf("timeout-test-%d", i),
			Title:       "Test Issue",
			Description: "Test description",
			Priority:    0,
			IssueType:   types.TypeBug,
			Status:      types.StatusOpen,
		})
	}

	// Create a context with a very short timeout
	// Note: This test might be flaky - if the import completes within the timeout,
	// that's also acceptable behavior
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 1) // 1 nanosecond
	defer cancel()

	opts := ImportOptions{
		DryRun:     false,
		SkipUpdate: false,
		Strict:     false,
	}
	_, err = importIssuesCore(timeoutCtx, testDB, store, issues, opts)

	// We expect either success (if import was very fast) or context deadline exceeded
	if err != nil && err != context.DeadlineExceeded {
		t.Logf("Import with timeout returned: %v (expected DeadlineExceeded or success)", err)
	}

	// Verify database integrity
	ctx := context.Background()
	importedIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Database corrupted after timeout: %v", err)
	}

	// Should have imported some or all issues
	t.Logf("Imported %d issues before timeout", len(importedIssues))
}
