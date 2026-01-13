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

// TestCommentAddWithResolvedID verifies that the RPC layer works correctly
// when the CLI resolves short IDs before sending to the daemon (issue #1070).
//
// Note: The RPC server expects full IDs. Short ID resolution happens in the CLI
// (cmd/bd/comments.go) before calling the daemon, following the pattern used by
// update.go, label.go, and other commands. This test simulates that workflow.
func TestCommentAddWithResolvedID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow RPC test in short mode")
	}
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an issue
	createResp, err := client.Create(&CreateArgs{
		Title:     "Resolved ID comment test",
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
	shortID := fullID
	for i := len(fullID) - 1; i >= 0; i-- {
		if fullID[i] == '-' {
			shortID = fullID[i+1:]
			break
		}
	}
	t.Logf("Short ID: %s", shortID)

	// Simulate CLI behavior: resolve short ID first, then call RPC with full ID
	// In the real CLI, this is done via daemonClient.ResolveID()
	resolveResp, err := client.ResolveID(&ResolveIDArgs{ID: shortID})
	if err != nil {
		t.Fatalf("resolve ID failed: %v", err)
	}
	var resolvedID string
	if err := json.Unmarshal(resolveResp.Data, &resolvedID); err != nil {
		t.Fatalf("failed to decode resolved ID: %v", err)
	}
	t.Logf("Resolved ID: %s", resolvedID)

	if resolvedID != fullID {
		t.Fatalf("expected resolved ID %q, got %q", fullID, resolvedID)
	}

	// Now add comment using the RESOLVED (full) ID
	addResp, err := client.AddComment(&CommentAddArgs{
		ID:     resolvedID,
		Author: "tester",
		Text:   "comment via resolved ID",
	})
	if err != nil {
		t.Fatalf("add comment failed: %v", err)
	}

	var added types.Comment
	if err := json.Unmarshal(addResp.Data, &added); err != nil {
		t.Fatalf("failed to decode add comment response: %v", err)
	}

	if added.Text != "comment via resolved ID" {
		t.Errorf("expected comment text 'comment via resolved ID', got %q", added.Text)
	}
}

// TestCommentListWithResolvedID verifies that ListComments works when the CLI
// resolves short IDs before sending to the daemon (issue #1070).
func TestCommentListWithResolvedID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow RPC test in short mode")
	}
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an issue and add a comment
	createResp, err := client.Create(&CreateArgs{
		Title:     "Resolved ID list test",
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

	// Simulate CLI behavior: resolve short ID first
	resolveResp, err := client.ResolveID(&ResolveIDArgs{ID: shortID})
	if err != nil {
		t.Fatalf("resolve ID failed: %v", err)
	}
	var resolvedID string
	if err := json.Unmarshal(resolveResp.Data, &resolvedID); err != nil {
		t.Fatalf("failed to decode resolved ID: %v", err)
	}

	// List comments using the RESOLVED (full) ID
	listResp, err := client.ListComments(&CommentListArgs{ID: resolvedID})
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
