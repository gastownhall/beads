//go:build cgo

package dolt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestServerModeConnection tests connecting to DoltStore via server mode
func TestServerModeConnection(t *testing.T) {
	// Skip if dolt is not available
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping server mode test")
	}

	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "dolt-server-mode-test-*")
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

	// Start server on non-standard ports
	server := NewServer(ServerConfig{
		DataDir:        tmpDir,
		SQLPort:        13307,
		RemotesAPIPort: 18081,
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
		ServerHost: "127.0.0.1",
		ServerPort: 13307,
	})
	if err != nil {
		t.Fatalf("failed to create server mode store: %v", err)
	}
	defer store.Close()

	// Set issue prefix (required for creating issues)
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Verify we can perform basic operations
	// Create an issue
	issue := &types.Issue{
		Title:       "Test issue in server mode",
		Description: "Original description",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if issue.ID == "" {
		t.Fatal("expected issue ID to be generated")
	}
	t.Logf("Created issue: %s", issue.ID)

	// Read it back
	readIssue, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if readIssue.Title != issue.Title {
		t.Errorf("title mismatch: expected %q, got %q", issue.Title, readIssue.Title)
	}

	// Update it
	updates := map[string]interface{}{
		"description": "Updated description",
		"priority":    1,
	}
	if err := store.UpdateIssue(ctx, issue.ID, updates, "test"); err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	// Verify update
	updatedIssue, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get updated issue: %v", err)
	}
	if updatedIssue.Description != "Updated description" {
		t.Errorf("expected description 'Updated description', got %q", updatedIssue.Description)
	}
	if updatedIssue.Priority != 1 {
		t.Errorf("expected priority 1, got %d", updatedIssue.Priority)
	}

	t.Logf("Server mode connection test passed: created and updated issue %s", issue.ID)
}

// TestServerModeComments verifies that comments added via AddIssueComment are
// visible to a subsequent GetIssueComments call when the server runs with
// --no-auto-commit. Without an explicit BEGIN/COMMIT around the INSERT, Dolt
// rolls back the uncommitted implicit transaction when the connection closes,
// causing silent data loss (write returns no error but read returns empty).
func TestServerModeComments(t *testing.T) {
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping server mode comment test")
	}

	tmpDir, err := os.MkdirTemp("", "dolt-server-comments-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.Command("dolt", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init dolt repo: %v", err)
	}

	server := NewServer(ServerConfig{
		DataDir:        tmpDir,
		SQLPort:        13308,
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

	store, err := New(ctx, &Config{
		Path:       tmpDir,
		Database:   "beads",
		ServerHost: "127.0.0.1",
		ServerPort: 13308,
	})
	if err != nil {
		t.Fatalf("failed to create server mode store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	issue := &types.Issue{
		Title:     "Issue for comment persistence test",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Add comments — previously these silently dropped writes because execContext
	// did not commit the implicit transaction before returning the connection to
	// the pool.
	if _, err := store.AddIssueComment(ctx, issue.ID, "alice", "first comment"); err != nil {
		t.Fatalf("AddIssueComment failed: %v", err)
	}
	if _, err := store.AddIssueComment(ctx, issue.ID, "bob", "second comment"); err != nil {
		t.Fatalf("AddIssueComment (second) failed: %v", err)
	}

	comments, err := store.GetIssueComments(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssueComments failed: %v", err)
	}
	if len(comments) != 2 {
		t.Errorf("expected 2 comments, got %d (writes were likely not committed)", len(comments))
	}
	if len(comments) > 0 && comments[0].Text != "first comment" {
		t.Errorf("expected 'first comment', got %q", comments[0].Text)
	}
	if len(comments) > 1 && comments[1].Text != "second comment" {
		t.Errorf("expected 'second comment', got %q", comments[1].Text)
	}
	t.Logf("Comment persistence test passed: %d comments on issue %s", len(comments), issue.ID)
}
