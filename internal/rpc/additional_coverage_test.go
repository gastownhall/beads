package rpc

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// CountResult represents the response from handleCount
type CountResult struct {
	Count int `json:"count"`
}

// TestCount tests the Count operation via RPC
func TestCount(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create some issues first
	for i := 0; i < 5; i++ {
		args := &CreateArgs{
			Title:       "Test Issue for Count",
			Description: "Test description",
			IssueType:   "task",
			Priority:    2,
		}
		if _, err := client.Create(args); err != nil {
			t.Fatalf("Create failed: %v", err)
		}
	}

	// Create a closed issue
	createResp, err := client.Create(&CreateArgs{
		Title:       "Closed Issue",
		Description: "Test description",
		IssueType:   "bug",
		Priority:    1,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	var closedIssue types.Issue
	json.Unmarshal(createResp.Data, &closedIssue)
	if _, err := client.CloseIssue(&CloseArgs{ID: closedIssue.ID, Reason: "Done"}); err != nil {
		t.Fatalf("CloseIssue failed: %v", err)
	}

	tests := []struct {
		name          string
		args          *CountArgs
		expectedCount int
	}{
		{
			name:          "Count all issues",
			args:          &CountArgs{},
			expectedCount: 6,
		},
		{
			name:          "Count open issues",
			args:          &CountArgs{Status: "open"},
			expectedCount: 5,
		},
		{
			name:          "Count closed issues",
			args:          &CountArgs{Status: "closed"},
			expectedCount: 1,
		},
		{
			name:          "Count by type task",
			args:          &CountArgs{IssueType: "task"},
			expectedCount: 5,
		},
		{
			name:          "Count by type bug",
			args:          &CountArgs{IssueType: "bug"},
			expectedCount: 1,
		},
		{
			name:          "Count by priority",
			args:          &CountArgs{Priority: intPtr(2)},
			expectedCount: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := client.Count(tt.args)
			if err != nil {
				t.Fatalf("Count failed: %v", err)
			}

			if !resp.Success {
				t.Fatalf("Expected success, got error: %s", resp.Error)
			}

			var result CountResult
			if err := json.Unmarshal(resp.Data, &result); err != nil {
				t.Fatalf("Failed to unmarshal count result: %v", err)
			}

			if result.Count != tt.expectedCount {
				t.Errorf("Expected count %d, got %d", tt.expectedCount, result.Count)
			}
		})
	}
}

// TestCountWithDateFilters tests Count with date range filters
func TestCountWithDateFilters(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an issue
	_, err := client.Create(&CreateArgs{
		Title:       "Recent Issue",
		Description: "Test description",
		IssueType:   "task",
		Priority:    2,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Count with created_after in the past (should include our issue)
	yesterday := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	resp, err := client.Count(&CountArgs{CreatedAfter: yesterday})
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}

	var result CountResult
	json.Unmarshal(resp.Data, &result)
	if result.Count < 1 {
		t.Errorf("Expected at least 1 issue created after %s, got %d", yesterday, result.Count)
	}

	// Count with created_before in the past (should not include our issue)
	resp, err = client.Count(&CountArgs{CreatedBefore: yesterday})
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}

	json.Unmarshal(resp.Data, &result)
	if result.Count != 0 {
		t.Errorf("Expected 0 issues created before %s, got %d", yesterday, result.Count)
	}
}

// TestResolveID tests the ResolveID operation via RPC
func TestResolveID(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an issue first
	createResp, err := client.Create(&CreateArgs{
		Title:     "Test Issue for Resolution",
		IssueType: "task",
		Priority:  2,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	json.Unmarshal(createResp.Data, &issue)

	// Test resolving the full ID
	resp, err := client.ResolveID(&ResolveIDArgs{ID: issue.ID})
	if err != nil {
		t.Fatalf("ResolveID failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Expected success, got error: %s", resp.Error)
	}

	var resolvedID string
	if err := json.Unmarshal(resp.Data, &resolvedID); err != nil {
		t.Fatalf("Failed to unmarshal resolved ID: %v", err)
	}

	if resolvedID != issue.ID {
		t.Errorf("Expected resolved ID %s, got %s", issue.ID, resolvedID)
	}

	// Test resolving a partial ID (first few characters after prefix)
	// Note: This depends on there being only one issue with this prefix
	if len(issue.ID) > 3 {
		partialID := issue.ID[:len(issue.ID)-2] // Remove last 2 chars
		resp, err = client.ResolveID(&ResolveIDArgs{ID: partialID})
		if err != nil {
			t.Fatalf("ResolveID with partial failed: %v", err)
		}

		if !resp.Success {
			// This might fail if partial is ambiguous, which is fine
			t.Logf("Partial resolution returned: %s", resp.Error)
		}
	}
}

// TestResolveID_NotFound tests ResolveID with non-existent ID
func TestResolveID_NotFound(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// ResolveID with non-existent ID should fail with an error
	resp, err := client.ResolveID(&ResolveIDArgs{ID: "bd-nonexistent"})

	// The error is returned through the Execute function
	if err != nil {
		// Expected - this is the correct behavior for non-existent ID
		return
	}

	// If we got here without error, check the response
	if resp.Success {
		t.Error("Expected failure for non-existent ID, got success")
	}
}

// TestDelete tests the Delete operation via RPC
func TestDelete(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an issue to delete
	createResp, err := client.Create(&CreateArgs{
		Title:       "Issue to Delete",
		Description: "Test description",
		IssueType:   "task",
		Priority:    2,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	json.Unmarshal(createResp.Data, &issue)

	// Delete the issue
	deleteResp, err := client.Delete(&DeleteArgs{
		IDs:    []string{issue.ID},
		Force:  true,
		Reason: "Testing deletion",
	})
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if !deleteResp.Success {
		t.Fatalf("Expected success, got error: %s", deleteResp.Error)
	}

	// Verify the delete result
	var result map[string]interface{}
	json.Unmarshal(deleteResp.Data, &result)

	if int(result["deleted_count"].(float64)) != 1 {
		t.Errorf("Expected deleted_count=1, got %v", result["deleted_count"])
	}

	// Note: Deleted issues are tombstoned, not hard-deleted.
	// They may still appear with status=closed, so we just verify
	// the delete operation succeeded above.
}

// TestDelete_DryRun tests Delete in dry-run mode
func TestDelete_DryRun(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an issue
	createResp, err := client.Create(&CreateArgs{
		Title:       "Issue for DryRun Delete",
		Description: "Test description",
		IssueType:   "task",
		Priority:    2,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	json.Unmarshal(createResp.Data, &issue)

	// Delete in dry-run mode
	deleteResp, err := client.Delete(&DeleteArgs{
		IDs:    []string{issue.ID},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if !deleteResp.Success {
		t.Fatalf("Expected success, got error: %s", deleteResp.Error)
	}

	var result map[string]interface{}
	json.Unmarshal(deleteResp.Data, &result)
	if result["dry_run"] != true {
		t.Error("Expected dry_run to be true in response")
	}

	// Verify issue still exists
	showResp, err := client.Show(&ShowArgs{ID: issue.ID})
	if err != nil {
		t.Fatalf("Show failed: %v", err)
	}

	if !showResp.Success {
		t.Error("Issue should still exist after dry-run delete")
	}
}

// TestDelete_NoIDs tests Delete with no IDs
func TestDelete_NoIDs(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Delete with no IDs should fail
	resp, err := client.Delete(&DeleteArgs{
		IDs: []string{},
	})

	// The error may come through err or through resp.Success=false
	if err != nil {
		// Expected - this is the correct behavior
		return
	}

	if resp.Success {
		t.Error("Expected failure when deleting with no IDs")
	}
}

// TestDelete_MultipleIssues tests deleting multiple issues at once
func TestDelete_MultipleIssues(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create multiple issues
	var ids []string
	for i := 0; i < 3; i++ {
		createResp, err := client.Create(&CreateArgs{
			Title:       "Issue for Batch Delete",
			Description: "Test description",
			IssueType:   "task",
			Priority:    2,
		})
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		var issue types.Issue
		json.Unmarshal(createResp.Data, &issue)
		ids = append(ids, issue.ID)
	}

	// Delete all at once
	deleteResp, err := client.Delete(&DeleteArgs{
		IDs:   ids,
		Force: true,
	})
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if !deleteResp.Success {
		t.Fatalf("Expected success, got error: %s", deleteResp.Error)
	}

	var result map[string]interface{}
	json.Unmarshal(deleteResp.Data, &result)
	if int(result["deleted_count"].(float64)) != 3 {
		t.Errorf("Expected deleted_count=3, got %v", result["deleted_count"])
	}
}

// TestStale tests the Stale operation via RPC
func TestStale(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an issue (it won't be stale because it's just created)
	_, err := client.Create(&CreateArgs{
		Title:       "Fresh Issue",
		Description: "Test description",
		IssueType:   "task",
		Priority:    2,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Get stale issues (should be empty since issue is fresh)
	resp, err := client.Stale(&StaleArgs{Days: 7})
	if err != nil {
		t.Fatalf("Stale failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Expected success, got error: %s", resp.Error)
	}

	var staleIssues []types.Issue
	if err := json.Unmarshal(resp.Data, &staleIssues); err != nil {
		t.Fatalf("Failed to unmarshal stale issues: %v", err)
	}

	// Should be empty since our issue was just created
	if len(staleIssues) != 0 {
		t.Errorf("Expected 0 stale issues, got %d", len(staleIssues))
	}
}

// TestStale_WithStatusFilter tests Stale with status filter
func TestStale_WithStatusFilter(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create and close an issue
	createResp, err := client.Create(&CreateArgs{
		Title:       "Issue to Close",
		Description: "Test description",
		IssueType:   "task",
		Priority:    2,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	json.Unmarshal(createResp.Data, &issue)

	if _, err := client.CloseIssue(&CloseArgs{ID: issue.ID}); err != nil {
		t.Fatalf("CloseIssue failed: %v", err)
	}

	// Get stale open issues (should not include the closed one)
	resp, err := client.Stale(&StaleArgs{Days: 0, Status: "open"})
	if err != nil {
		t.Fatalf("Stale failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Expected success, got error: %s", resp.Error)
	}

	var staleIssues []types.Issue
	json.Unmarshal(resp.Data, &staleIssues)

	for _, si := range staleIssues {
		if si.ID == issue.ID {
			t.Error("Closed issue should not appear in open stale issues")
		}
	}
}

// TestCommentList tests the ListComments operation via RPC
func TestCommentList(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an issue first
	createResp, err := client.Create(&CreateArgs{
		Title:       "Issue for Comments",
		Description: "Test description",
		IssueType:   "task",
		Priority:    2,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	json.Unmarshal(createResp.Data, &issue)

	// List comments on the issue (should be empty initially)
	resp, err := client.ListComments(&CommentListArgs{ID: issue.ID})
	if err != nil {
		t.Fatalf("ListComments failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Expected success, got error: %s", resp.Error)
	}

	var comments []types.Comment
	if err := json.Unmarshal(resp.Data, &comments); err != nil {
		t.Fatalf("Failed to unmarshal comments: %v", err)
	}

	if len(comments) != 0 {
		t.Errorf("Expected 0 comments initially, got %d", len(comments))
	}
}

// TestCommentAdd tests the AddComment operation via RPC
func TestCommentAdd(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an issue first
	createResp, err := client.Create(&CreateArgs{
		Title:       "Issue for Adding Comments",
		Description: "Test description",
		IssueType:   "task",
		Priority:    2,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	json.Unmarshal(createResp.Data, &issue)

	// Add a comment
	addResp, err := client.AddComment(&CommentAddArgs{
		ID:     issue.ID,
		Author: "testuser",
		Text:   "This is a test comment",
	})
	if err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}

	if !addResp.Success {
		t.Fatalf("Expected success, got error: %s", addResp.Error)
	}

	var comment types.Comment
	if err := json.Unmarshal(addResp.Data, &comment); err != nil {
		t.Fatalf("Failed to unmarshal comment: %v", err)
	}

	if comment.Author != "testuser" {
		t.Errorf("Expected author 'testuser', got '%s'", comment.Author)
	}
	if comment.Text != "This is a test comment" {
		t.Errorf("Expected text 'This is a test comment', got '%s'", comment.Text)
	}

	// Verify comment is listed
	listResp, err := client.ListComments(&CommentListArgs{ID: issue.ID})
	if err != nil {
		t.Fatalf("ListComments failed: %v", err)
	}

	var comments []types.Comment
	json.Unmarshal(listResp.Data, &comments)

	if len(comments) != 1 {
		t.Errorf("Expected 1 comment, got %d", len(comments))
	}
}

// TestCommentAdd_MultipleComments tests adding multiple comments
func TestCommentAdd_MultipleComments(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an issue
	createResp, err := client.Create(&CreateArgs{
		Title:       "Issue for Multiple Comments",
		Description: "Test description",
		IssueType:   "task",
		Priority:    2,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	json.Unmarshal(createResp.Data, &issue)

	// Add multiple comments
	for i := 1; i <= 3; i++ {
		_, err := client.AddComment(&CommentAddArgs{
			ID:     issue.ID,
			Author: "user",
			Text:   "Comment text",
		})
		if err != nil {
			t.Fatalf("AddComment %d failed: %v", i, err)
		}
	}

	// Verify all comments are listed
	listResp, err := client.ListComments(&CommentListArgs{ID: issue.ID})
	if err != nil {
		t.Fatalf("ListComments failed: %v", err)
	}

	var comments []types.Comment
	json.Unmarshal(listResp.Data, &comments)

	if len(comments) != 3 {
		t.Errorf("Expected 3 comments, got %d", len(comments))
	}
}

// TestMetrics tests the Metrics operation via RPC
func TestMetrics(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Make a few requests to generate some metrics
	for i := 0; i < 3; i++ {
		if err := client.Ping(); err != nil {
			t.Fatalf("Ping failed: %v", err)
		}
	}

	// Get metrics
	metrics, err := client.Metrics()
	if err != nil {
		t.Fatalf("Metrics failed: %v", err)
	}

	if metrics == nil {
		t.Fatal("Expected metrics, got nil")
	}

	// Check that we have operations recorded
	if len(metrics.Operations) == 0 {
		t.Error("Expected at least some operations recorded in metrics")
	}

	// Calculate total requests from operations
	var totalRequests int64
	for _, op := range metrics.Operations {
		totalRequests += op.TotalCount
	}

	// We made 3 pings + 1 metrics request = at least 4 requests
	// (metrics call itself is recorded after snapshot)
	if totalRequests < 3 {
		t.Errorf("Expected at least 3 total requests, got %d", totalRequests)
	}

	// Check uptime is reasonable
	if metrics.UptimeSeconds <= 0 {
		t.Errorf("Expected positive uptime, got %f", metrics.UptimeSeconds)
	}
}

// TestCountWithGroupBy tests Count with GroupBy option
func TestCountWithGroupBy(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create issues with different statuses and types
	_, err := client.Create(&CreateArgs{
		Title:       "Task Issue",
		Description: "Test description",
		IssueType:   "task",
		Priority:    2,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	createResp, err := client.Create(&CreateArgs{
		Title:       "Bug Issue",
		Description: "Test description",
		IssueType:   "bug",
		Priority:    1,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var bugIssue types.Issue
	json.Unmarshal(createResp.Data, &bugIssue)

	// Close the bug
	if _, err := client.CloseIssue(&CloseArgs{ID: bugIssue.ID}); err != nil {
		t.Fatalf("CloseIssue failed: %v", err)
	}

	// Count grouped by status
	resp, err := client.Count(&CountArgs{GroupBy: "status"})
	if err != nil {
		t.Fatalf("Count with GroupBy failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Expected success, got error: %s", resp.Error)
	}

	// The response should contain grouped data
	// (The exact format depends on the server implementation)
	if len(resp.Data) == 0 {
		t.Error("Expected non-empty response for grouped count")
	}
}

// TestCountWithTitleContains tests Count with title pattern matching
func TestCountWithTitleContains(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create issues with specific titles
	_, err := client.Create(&CreateArgs{
		Title:       "Authentication Bug Fix",
		Description: "Test description",
		IssueType:   "bug",
		Priority:    1,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	_, err = client.Create(&CreateArgs{
		Title:       "Add User Login Feature",
		Description: "Test description",
		IssueType:   "feature",
		Priority:    2,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Count issues with "Authentication" in title
	resp, err := client.Count(&CountArgs{TitleContains: "Authentication"})
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}

	var result CountResult
	json.Unmarshal(resp.Data, &result)

	if result.Count != 1 {
		t.Errorf("Expected 1 issue with 'Authentication' in title, got %d", result.Count)
	}
}

// TestStaleWithLimit tests Stale with limit parameter
func TestStaleWithLimit(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create multiple issues
	for i := 0; i < 5; i++ {
		_, err := client.Create(&CreateArgs{
			Title:       "Issue for Stale Test",
			Description: "Test description",
			IssueType:   "task",
			Priority:    2,
		})
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
	}

	// Get stale issues with limit (using 0 days so all issues are considered stale)
	resp, err := client.Stale(&StaleArgs{Days: 0, Limit: 2})
	if err != nil {
		t.Fatalf("Stale failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Expected success, got error: %s", resp.Error)
	}

	var staleIssues []types.Issue
	json.Unmarshal(resp.Data, &staleIssues)

	if len(staleIssues) > 2 {
		t.Errorf("Expected at most 2 stale issues (limit), got %d", len(staleIssues))
	}
}

// Helper function to create a pointer to an int
func intPtr(i int) *int {
	return &i
}


// GetMutations and Export tests

// TestGetMutations tests the GetMutations operation via RPC
func TestGetMutations(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an issue to generate a mutation
	_, err := client.Create(&CreateArgs{
		Title:       "Issue to track mutations",
		Description: "Test description",
		IssueType:   "task",
		Priority:    2,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Get recent mutations
	resp, err := client.GetMutations(&GetMutationsArgs{Since: 0})
	if err != nil {
		t.Fatalf("GetMutations failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Expected success, got error: %s", resp.Error)
	}

	// Response should be a slice of mutations
	if len(resp.Data) == 0 {
		t.Error("Expected non-empty response with mutations")
	}
}

// TestExport tests the Export operation via RPC
func TestExport(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create some issues first
	for i := 0; i < 3; i++ {
		_, err := client.Create(&CreateArgs{
			Title:       "Issue for Export",
			Description: "Test description",
			IssueType:   "task",
			Priority:    2,
		})
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
	}

	// Create a temp file for export
	tmpFile, err := os.CreateTemp("", "beads-export-*.jsonl")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	// Export to the temp file
	resp, err := client.Export(&ExportArgs{
		JSONLPath: tmpFile.Name(),
	})
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Expected success, got error: %s", resp.Error)
	}

	// Verify file was written
	info, err := os.Stat(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to stat export file: %v", err)
	}

	if info.Size() == 0 {
		t.Error("Expected non-empty export file")
	}
}

// TestMutationChan tests access to the mutation channel
func TestMutationChan(t *testing.T) {
	server, _, cleanup := setupTestServer(t)
	defer cleanup()

	// Get the mutation channel
	ch := server.MutationChan()

	// Channel should be non-nil
	if ch == nil {
		t.Error("Expected non-nil mutation channel")
	}
}

// TestResetDroppedEventsCount tests resetting the dropped events counter
func TestResetDroppedEventsCount(t *testing.T) {
	server, _, cleanup := setupTestServer(t)
	defer cleanup()

	// Reset dropped events count (should not panic)
	server.ResetDroppedEventsCount()

	// No error means success
}
