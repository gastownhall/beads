package sqlite

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestClaimIssueBasic tests the basic claim functionality.
func TestClaimIssueBasic(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test Issue for Claim",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Verify issue starts with no assignee
	if issue.Assignee != "" {
		t.Fatalf("expected no assignee initially, got %s", issue.Assignee)
	}

	// Claim the issue
	if err := store.ClaimIssue(ctx, issue.ID, "claiming-agent"); err != nil {
		t.Fatalf("ClaimIssue failed: %v", err)
	}

	// Verify the issue was claimed
	claimed, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if claimed.Assignee != "claiming-agent" {
		t.Errorf("expected assignee 'claiming-agent', got %s", claimed.Assignee)
	}
	if claimed.Status != types.StatusInProgress {
		t.Errorf("expected status 'in_progress', got %s", claimed.Status)
	}
}

// TestClaimIssueAlreadyClaimed tests that claiming an already claimed issue fails.
func TestClaimIssueAlreadyClaimed(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test Issue for Double Claim",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// First claim should succeed
	if err := store.ClaimIssue(ctx, issue.ID, "first-claimer"); err != nil {
		t.Fatalf("first claim should succeed: %v", err)
	}

	// Second claim should fail
	err := store.ClaimIssue(ctx, issue.ID, "second-claimer")
	if err == nil {
		t.Fatal("expected second claim to fail, but it succeeded")
	}

	// Verify it's an ErrAlreadyClaimed error
	if !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Errorf("expected ErrAlreadyClaimed, got %v", err)
	}

	// Verify error message contains the original claimer
	if !strings.Contains(err.Error(), "first-claimer") {
		t.Errorf("expected error to contain 'first-claimer', got %s", err.Error())
	}

	// Verify the issue is still assigned to first claimer
	claimed, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if claimed.Assignee != "first-claimer" {
		t.Errorf("expected assignee 'first-claimer', got %s", claimed.Assignee)
	}
}

// TestClaimIssueNonexistent tests that claiming a nonexistent issue fails.
func TestClaimIssueNonexistent(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.ClaimIssue(ctx, "nonexistent-id", "claimer")
	if err == nil {
		t.Fatal("expected error when claiming nonexistent issue")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got %v", err)
	}
}

// TestClaimIssueConcurrent tests that concurrent claims are handled correctly.
// Only one of the concurrent claims should succeed, and the others should fail
// with ErrAlreadyClaimed.
func TestClaimIssueConcurrent(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test Issue for Concurrent Claim",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	const numClaimers = 10
	var wg sync.WaitGroup
	var successCount atomic.Int32
	var failureCount atomic.Int32
	var winner atomic.Value

	// Launch concurrent claims
	for i := 0; i < numClaimers; i++ {
		wg.Add(1)
		go func(claimerID int) {
			defer wg.Done()
			claimer := string(rune('A'+claimerID)) + "-agent"
			err := store.ClaimIssue(ctx, issue.ID, claimer)
			if err == nil {
				successCount.Add(1)
				winner.Store(claimer)
			} else {
				if errors.Is(err, storage.ErrAlreadyClaimed) {
					failureCount.Add(1)
				} else {
					t.Errorf("unexpected error for claimer %s: %v", claimer, err)
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify exactly one claim succeeded
	if successCount.Load() != 1 {
		t.Errorf("expected exactly 1 successful claim, got %d", successCount.Load())
	}

	// Verify the rest failed with ErrAlreadyClaimed
	if failureCount.Load() != numClaimers-1 {
		t.Errorf("expected %d failed claims, got %d", numClaimers-1, failureCount.Load())
	}

	// Verify the issue is assigned to the winner
	claimed, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	winnerName := winner.Load().(string)
	if claimed.Assignee != winnerName {
		t.Errorf("expected assignee '%s', got '%s'", winnerName, claimed.Assignee)
	}
	if claimed.Status != types.StatusInProgress {
		t.Errorf("expected status 'in_progress', got %s", claimed.Status)
	}
}

// TestClaimIssueConcurrentMultipleIssues tests concurrent claims on different issues.
// All claims should succeed since they're on different issues.
func TestClaimIssueConcurrentMultipleIssues(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	const numIssues = 10

	// Create multiple issues
	var issueIDs []string
	for i := 0; i < numIssues; i++ {
		issue := &types.Issue{
			Title:     "Test Issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
		issueIDs = append(issueIDs, issue.ID)
	}

	var wg sync.WaitGroup
	var successCount atomic.Int32

	// Launch concurrent claims, each on a different issue
	for i := 0; i < numIssues; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			claimer := string(rune('A'+idx)) + "-agent"
			err := store.ClaimIssue(ctx, issueIDs[idx], claimer)
			if err == nil {
				successCount.Add(1)
			} else {
				t.Errorf("claim failed for issue %d: %v", idx, err)
			}
		}(i)
	}

	wg.Wait()

	// All claims should succeed since they're on different issues
	if successCount.Load() != numIssues {
		t.Errorf("expected %d successful claims, got %d", numIssues, successCount.Load())
	}

	// Verify all issues are claimed
	for i, id := range issueIDs {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue failed: %v", err)
		}
		expectedAssignee := string(rune('A'+i)) + "-agent"
		if issue.Assignee != expectedAssignee {
			t.Errorf("issue %d: expected assignee '%s', got '%s'", i, expectedAssignee, issue.Assignee)
		}
		if issue.Status != types.StatusInProgress {
			t.Errorf("issue %d: expected status 'in_progress', got %s", i, issue.Status)
		}
	}
}

// TestClaimIssueWithExistingAssignee tests that an issue with an existing assignee
// cannot be claimed (must be cleared first).
func TestClaimIssueWithExistingAssignee(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create an issue with an assignee already set
	issue := &types.Issue{
		Title:     "Test Issue with Assignee",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Assignee:  "original-owner",
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Trying to claim should fail
	err := store.ClaimIssue(ctx, issue.ID, "new-claimer")
	if err == nil {
		t.Fatal("expected claim to fail for issue with existing assignee")
	}
	if !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Errorf("expected ErrAlreadyClaimed, got %v", err)
	}
	if !strings.Contains(err.Error(), "original-owner") {
		t.Errorf("expected error to contain 'original-owner', got %s", err.Error())
	}
}

// TestClaimIssueRecordsEvent tests that claiming an issue records a claim event.
func TestClaimIssueRecordsEvent(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test Issue for Claim Event",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Claim the issue
	if err := store.ClaimIssue(ctx, issue.ID, "claiming-agent"); err != nil {
		t.Fatalf("ClaimIssue failed: %v", err)
	}

	// Get events for the issue
	events, err := store.GetEvents(ctx, issue.ID, 10)
	if err != nil {
		t.Fatalf("GetEvents failed: %v", err)
	}

	// Find the claim event
	found := false
	for _, event := range events {
		if event.EventType == "claimed" && event.Actor == "claiming-agent" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a 'claimed' event to be recorded")
	}
}
