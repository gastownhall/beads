//go:build integration
// +build integration

package rpc

import (
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestCommentOperationsViaRPC(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow RPC test in short mode")
	}
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	createResp, err := client.Create(&CreateArgs{
		Title:     "Comment test",
		IssueType: "task",
		Priority:  2,
	})
	if err != nil {
		t.Fatalf("create issue failed: %v", err)
	}

	var created types.Issue
	if err := json.Unmarshal(createResp.Data, &created); err != nil {
		t.Fatalf("failed to decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected issue ID to be set")
	}

	addResp, err := client.AddComment(&CommentAddArgs{
		ID:     created.ID,
		Author: "tester",
		Text:   "first comment",
	})
	if err != nil {
		t.Fatalf("add comment failed: %v", err)
	}

	var added types.Comment
	if err := json.Unmarshal(addResp.Data, &added); err != nil {
		t.Fatalf("failed to decode add comment response: %v", err)
	}

	if added.Text != "first comment" {
		t.Fatalf("expected comment text 'first comment', got %q", added.Text)
	}

	listResp, err := client.ListComments(&CommentListArgs{ID: created.ID})
	if err != nil {
		t.Fatalf("list comments failed: %v", err)
	}

	var comments []*types.Comment
	if err := json.Unmarshal(listResp.Data, &comments); err != nil {
		t.Fatalf("failed to decode comment list: %v", err)
	}

	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Text != "first comment" {
		t.Fatalf("expected comment text 'first comment', got %q", comments[0].Text)
	}
}

// TestCommentAddWithShortID tests that AddComment accepts short IDs (issue #1070).
// Currently, the RPC layer requires full IDs. The fix should resolve short IDs
// to full IDs before performing operations.
//
// This test is expected to FAIL until the bug is fixed.
func TestCommentAddWithShortID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow RPC test in short mode")
	}
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an issue
	createResp, err := client.Create(&CreateArgs{
		Title:     "Short ID comment test",
		IssueType: "task",
		Priority:  2,
	})
	if err != nil {
		t.Fatalf("create issue failed: %v", err)
	}

	var created types.Issue
	if err := json.Unmarshal(createResp.Data, &created); err != nil {
		t.Fatalf("failed to decode create response: %v", err)
	}

	fullID := created.ID
	t.Logf("Created issue with full ID: %s", fullID)

	// Extract the short ID (hash portion after the prefix)
	// For IDs like "test-abc123", the short ID is "abc123"
	shortID := fullID
	for i := len(fullID) - 1; i >= 0; i-- {
		if fullID[i] == '-' {
			shortID = fullID[i+1:]
			break
		}
	}
	t.Logf("Using short ID: %s", shortID)

	// Try to add a comment using the SHORT ID (not full ID)
	// This should work once the bug is fixed
	addResp, err := client.AddComment(&CommentAddArgs{
		ID:     shortID, // Using short ID instead of full ID
		Author: "tester",
		Text:   "comment via short ID",
	})
	if err != nil {
		t.Fatalf("add comment with short ID failed: %v (this is the bug - short IDs should work)", err)
	}

	var added types.Comment
	if err := json.Unmarshal(addResp.Data, &added); err != nil {
		t.Fatalf("failed to decode add comment response: %v", err)
	}

	if added.Text != "comment via short ID" {
		t.Errorf("expected comment text 'comment via short ID', got %q", added.Text)
	}

	// Verify by listing comments using the full ID
	listResp, err := client.ListComments(&CommentListArgs{ID: fullID})
	if err != nil {
		t.Fatalf("list comments failed: %v", err)
	}

	var comments []*types.Comment
	if err := json.Unmarshal(listResp.Data, &comments); err != nil {
		t.Fatalf("failed to decode comment list: %v", err)
	}

	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// TestCommentListWithShortID tests that ListComments accepts short IDs (issue #1070).
// This test is expected to FAIL until the bug is fixed.
func TestCommentListWithShortID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow RPC test in short mode")
	}
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an issue and add a comment
	createResp, err := client.Create(&CreateArgs{
		Title:     "Short ID list test",
		IssueType: "task",
		Priority:  2,
	})
	if err != nil {
		t.Fatalf("create issue failed: %v", err)
	}

	var created types.Issue
	if err := json.Unmarshal(createResp.Data, &created); err != nil {
		t.Fatalf("failed to decode create response: %v", err)
	}

	fullID := created.ID

	// Add a comment using full ID
	_, err = client.AddComment(&CommentAddArgs{
		ID:     fullID,
		Author: "tester",
		Text:   "test comment for list",
	})
	if err != nil {
		t.Fatalf("add comment failed: %v", err)
	}

	// Extract short ID
	shortID := fullID
	for i := len(fullID) - 1; i >= 0; i-- {
		if fullID[i] == '-' {
			shortID = fullID[i+1:]
			break
		}
	}
	t.Logf("Full ID: %s, Short ID: %s", fullID, shortID)

	// Try to list comments using the SHORT ID
	// This should work once the bug is fixed
	listResp, err := client.ListComments(&CommentListArgs{ID: shortID})
	if err != nil {
		t.Fatalf("list comments with short ID failed: %v (this is the bug - short IDs should work)", err)
	}

	var comments []*types.Comment
	if err := json.Unmarshal(listResp.Data, &comments); err != nil {
		t.Fatalf("failed to decode comment list: %v", err)
	}

	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d (short ID resolution may have failed)", len(comments))
	}
}
