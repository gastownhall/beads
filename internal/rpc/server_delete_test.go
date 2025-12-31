package rpc

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/types"
)

// TestHandleDelete_DryRun verifies that dry-run mode returns what would be deleted
// without actually deleting the issues
func TestHandleDelete_DryRun(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create test issues
	issueIDs := createTestIssues(t, server, 3)

	// Request dry-run deletion
	deleteArgs := DeleteArgs{
		IDs:    issueIDs,
		DryRun: true,
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	resp := server.handleDelete(deleteReq)
	if !resp.Success {
		t.Fatalf("dry-run delete failed: %s", resp.Error)
	}

	// Parse response
	var result map[string]interface{}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Verify dry-run flag in response
	if dryRun, ok := result["dry_run"].(bool); !ok || !dryRun {
		t.Error("expected dry_run: true in response")
	}

	// Verify issue count
	if count, ok := result["issue_count"].(float64); !ok || int(count) != 3 {
		t.Errorf("expected issue_count: 3, got %v", result["issue_count"])
	}

	// Verify issues are still present (not actually deleted)
	ctx := context.Background()
	for _, id := range issueIDs {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Errorf("issue %s should still exist after dry-run, got error: %v", id, err)
		}
		if issue == nil {
			t.Errorf("issue %s should still exist after dry-run, but was deleted", id)
		}
	}
}

// TestHandleDelete_InvalidIssueID verifies error handling for non-existent issue IDs
func TestHandleDelete_InvalidIssueID(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Try to delete non-existent issue
	deleteArgs := DeleteArgs{
		IDs: []string{"bd-nonexistent"},
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	resp := server.handleDelete(deleteReq)

	// Should fail since all deletes failed
	if resp.Success {
		t.Error("expected failure for non-existent issue ID")
	}

	if resp.Error == "" {
		t.Error("expected error message for failed deletion")
	}
}

// TestHandleDelete_PartialSuccess verifies behavior when some IDs are valid and others aren't
func TestHandleDelete_PartialSuccess(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create one valid issue
	validIDs := createTestIssues(t, server, 1)
	validID := validIDs[0]

	// Mix valid and invalid IDs
	deleteArgs := DeleteArgs{
		IDs: []string{validID, "bd-fake1", "bd-fake2"},
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	resp := server.handleDelete(deleteReq)

	// Should succeed (partial success)
	if !resp.Success {
		t.Errorf("expected partial success, got error: %s", resp.Error)
	}

	// Parse response
	var result map[string]interface{}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Verify counts
	if deleted, ok := result["deleted_count"].(float64); !ok || int(deleted) != 1 {
		t.Errorf("expected deleted_count: 1, got %v", result["deleted_count"])
	}

	if total, ok := result["total_count"].(float64); !ok || int(total) != 3 {
		t.Errorf("expected total_count: 3, got %v", result["total_count"])
	}

	// Verify errors array exists
	if errors, ok := result["errors"].([]interface{}); !ok || len(errors) != 2 {
		t.Errorf("expected 2 errors in response, got %v", result["errors"])
	}

	// Verify partial_success flag
	if partial, ok := result["partial_success"].(bool); !ok || !partial {
		t.Error("expected partial_success: true in response")
	}
}

// TestHandleDelete_NoIDs verifies error when no issue IDs are provided
func TestHandleDelete_NoIDs(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Try to delete with empty IDs array
	deleteArgs := DeleteArgs{
		IDs: []string{},
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	resp := server.handleDelete(deleteReq)

	if resp.Success {
		t.Error("expected failure when no IDs provided")
	}

	if resp.Error != "no issue IDs provided for deletion" {
		t.Errorf("unexpected error message: %s", resp.Error)
	}
}

// TestHandleDelete_StorageNotAvailable verifies error when storage is nil
func TestHandleDelete_StorageNotAvailable(t *testing.T) {
	// Create server without storage
	server := NewServer("/tmp/test.sock", nil, "/tmp", "/tmp/test.db")

	deleteArgs := DeleteArgs{
		IDs: []string{"bd-123"},
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	resp := server.handleDelete(deleteReq)

	if resp.Success {
		t.Error("expected failure when storage not available")
	}

	if resp.Error == "" {
		t.Error("expected error message about storage not available")
	}
}

// TestHandleDelete_InvalidJSON verifies error handling for malformed JSON args
func TestHandleDelete_InvalidJSON(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	deleteReq := &Request{
		Operation: OpDelete,
		Args:      []byte("not valid json"),
		Actor:     "test-user",
	}

	resp := server.handleDelete(deleteReq)

	if resp.Success {
		t.Error("expected failure for invalid JSON")
	}

	if resp.Error == "" {
		t.Error("expected error message for invalid JSON")
	}
}

// TestHandleDelete_ResponseStructure verifies the response format for successful deletion
func TestHandleDelete_ResponseStructure(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create test issues
	issueIDs := createTestIssues(t, server, 2)

	// Delete issues
	deleteArgs := DeleteArgs{
		IDs:    issueIDs,
		Reason: "testing response structure",
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	resp := server.handleDelete(deleteReq)
	if !resp.Success {
		t.Fatalf("delete failed: %s", resp.Error)
	}

	// Parse response
	var result map[string]interface{}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Verify required fields
	if _, ok := result["deleted_count"]; !ok {
		t.Error("response missing 'deleted_count' field")
	}

	if _, ok := result["total_count"]; !ok {
		t.Error("response missing 'total_count' field")
	}

	// Verify counts match
	deleted := result["deleted_count"].(float64)
	total := result["total_count"].(float64)

	if int(deleted) != 2 {
		t.Errorf("expected deleted_count: 2, got %d", int(deleted))
	}

	if int(total) != 2 {
		t.Errorf("expected total_count: 2, got %d", int(total))
	}

	// Should not have errors field when all succeed
	if _, ok := result["errors"]; ok {
		t.Error("should not have 'errors' field when all deletions succeed")
	}

	// Should not have partial_success when all succeed
	if _, ok := result["partial_success"]; ok {
		t.Error("should not have 'partial_success' field when all deletions succeed")
	}
}

// TestHandleDelete_WithReason verifies deletion with a reason
func TestHandleDelete_WithReason(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create test issue
	issueIDs := createTestIssues(t, server, 1)

	// Delete with reason
	deleteArgs := DeleteArgs{
		IDs:    issueIDs,
		Reason: "test deletion with reason",
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	resp := server.handleDelete(deleteReq)
	if !resp.Success {
		t.Fatalf("delete with reason failed: %s", resp.Error)
	}

	// Verify issue was converted to tombstone (now that MemoryStorage supports CreateTombstone)
	ctx := context.Background()
	issue, _ := store.GetIssue(ctx, issueIDs[0])
	if issue == nil {
		t.Error("issue should exist as tombstone")
	} else if issue.Status != types.StatusTombstone {
		t.Errorf("issue should be tombstone, got status=%s", issue.Status)
	} else if issue.DeleteReason != "test deletion with reason" {
		t.Errorf("expected DeleteReason='test deletion with reason', got '%s'", issue.DeleteReason)
	}
}

// TestHandleDelete_WithTombstone tests delete handler with SQLite storage that supports tombstones
func TestHandleDelete_WithTombstone(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store := newTestStore(t, dbPath)
	defer store.Close()

	server := NewServer("/tmp/test.sock", store, "/tmp", dbPath)

	// Create a test issue using the SQLite store
	ctx := context.Background()
	createArgs := CreateArgs{
		Title:     "Issue for tombstone test",
		IssueType: "task",
		Priority:  1,
	}
	createJSON, _ := json.Marshal(createArgs)
	createReq := &Request{
		Operation: OpCreate,
		Args:      createJSON,
		Actor:     "test-user",
	}

	createResp := server.handleCreate(createReq)
	if !createResp.Success {
		t.Fatalf("failed to create test issue: %s", createResp.Error)
	}

	var createdIssue map[string]interface{}
	if err := json.Unmarshal(createResp.Data, &createdIssue); err != nil {
		t.Fatalf("failed to parse created issue: %v", err)
	}
	issueID := createdIssue["id"].(string)

	// Delete the issue (should create tombstone)
	deleteArgs := DeleteArgs{
		IDs:    []string{issueID},
		Reason: "tombstone test",
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	deleteResp := server.handleDelete(deleteReq)
	if !deleteResp.Success {
		t.Fatalf("delete failed: %s", deleteResp.Error)
	}

	// Verify issue was tombstoned (still exists but with tombstone status)
	issue, err := store.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("failed to get tombstoned issue: %v", err)
	}
	if issue == nil {
		t.Fatal("tombstoned issue should still exist in database")
	}
	if issue.Status != "tombstone" {
		t.Errorf("expected status=tombstone, got %s", issue.Status)
	}
	if issue.DeletedAt == nil {
		t.Error("DeletedAt should be set for tombstoned issue")
	}
	if issue.DeleteReason != "tombstone test" {
		t.Errorf("expected DeleteReason='tombstone test', got %q", issue.DeleteReason)
	}
}

// TestHandleDelete_AllFail verifies behavior when all deletions fail
func TestHandleDelete_AllFail(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Try to delete multiple non-existent issues
	deleteArgs := DeleteArgs{
		IDs: []string{"bd-fake1", "bd-fake2", "bd-fake3"},
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	resp := server.handleDelete(deleteReq)

	// Should fail since all deletes failed
	if resp.Success {
		t.Error("expected failure when all deletions fail")
	}

	if resp.Error == "" {
		t.Error("expected error message when all deletions fail")
	}
}

// TestHandleDelete_DryRunPreservesData verifies dry-run doesn't modify anything
func TestHandleDelete_DryRunPreservesData(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create test issues
	issueIDs := createTestIssues(t, server, 3)

	// Get issues before dry-run
	ctx := context.Background()
	beforeIssues := make(map[string]string)
	for _, id := range issueIDs {
		issue, _ := store.GetIssue(ctx, id)
		if issue != nil {
			beforeIssues[id] = issue.Title
		}
	}

	// Do dry-run deletion multiple times
	for i := 0; i < 3; i++ {
		deleteArgs := DeleteArgs{
			IDs:    issueIDs,
			DryRun: true,
		}
		deleteJSON, _ := json.Marshal(deleteArgs)
		deleteReq := &Request{
			Operation: OpDelete,
			Args:      deleteJSON,
			Actor:     "test-user",
		}

		resp := server.handleDelete(deleteReq)
		if !resp.Success {
			t.Fatalf("dry-run %d failed: %s", i, resp.Error)
		}
	}

	// Verify all issues still exist with same data
	for id, title := range beforeIssues {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Errorf("issue %s disappeared after dry-runs: %v", id, err)
			continue
		}
		if issue == nil {
			t.Errorf("issue %s was deleted by dry-run", id)
			continue
		}
		if issue.Title != title {
			t.Errorf("issue %s title changed: expected %q, got %q", id, title, issue.Title)
		}
	}
}

// createTestIssues is a helper to create test issues and return their IDs
func createTestIssues(t *testing.T, server *Server, count int) []string {
	t.Helper()
	ids := make([]string, count)

	for i := 0; i < count; i++ {
		createArgs := CreateArgs{
			Title:     "Test Issue for Delete",
			IssueType: "task",
			Priority:  1,
		}
		createJSON, _ := json.Marshal(createArgs)
		createReq := &Request{
			Operation: OpCreate,
			Args:      createJSON,
			Actor:     "test-user",
		}

		createResp := server.handleCreate(createReq)
		if !createResp.Success {
			t.Fatalf("failed to create test issue %d: %s", i, createResp.Error)
		}

		var createdIssue map[string]interface{}
		if err := json.Unmarshal(createResp.Data, &createdIssue); err != nil {
			t.Fatalf("failed to parse created issue %d: %v", i, err)
		}
		ids[i] = createdIssue["id"].(string)
	}

	return ids
}
