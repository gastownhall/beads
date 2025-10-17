package rpc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqlitestorage "github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func setupTestServer(t *testing.T) (*Server, *Client, func()) {
	tmpDir, err := os.MkdirTemp("", "bd-rpc-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	socketPath := filepath.Join(tmpDir, "bd.sock")

	store, err := sqlitestorage.New(dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create store: %v", err)
	}

	server := NewServer(socketPath, store)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := server.Start(ctx); err != nil && err.Error() != "accept unix "+socketPath+": use of closed network connection" {
			t.Logf("Server error: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	client, err := TryConnect(socketPath)
	if err != nil {
		cancel()
		server.Stop()
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to connect client: %v", err)
	}

	cleanup := func() {
		client.Close()
		cancel()
		server.Stop()
		store.Close()
		os.RemoveAll(tmpDir)
	}

	return server, client, cleanup
}

func TestPing(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	if err := client.Ping(); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
}

func TestCreateIssue(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	args := &CreateArgs{
		Title:       "Test Issue",
		Description: "Test description",
		IssueType:   "task",
		Priority:    2,
	}

	resp, err := client.Create(args)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Expected success, got error: %s", resp.Error)
	}

	var issue types.Issue
	if err := json.Unmarshal(resp.Data, &issue); err != nil {
		t.Fatalf("Failed to unmarshal issue: %v", err)
	}

	if issue.Title != args.Title {
		t.Errorf("Expected title %s, got %s", args.Title, issue.Title)
	}
	if issue.Priority != args.Priority {
		t.Errorf("Expected priority %d, got %d", args.Priority, issue.Priority)
	}
}

func TestUpdateIssue(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	createArgs := &CreateArgs{
		Title:     "Original Title",
		IssueType: "task",
		Priority:  2,
	}

	createResp, err := client.Create(createArgs)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	json.Unmarshal(createResp.Data, &issue)

	newTitle := "Updated Title"
	updateArgs := &UpdateArgs{
		ID:    issue.ID,
		Title: &newTitle,
	}

	updateResp, err := client.Update(updateArgs)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	var updatedIssue types.Issue
	json.Unmarshal(updateResp.Data, &updatedIssue)

	if updatedIssue.Title != newTitle {
		t.Errorf("Expected title %s, got %s", newTitle, updatedIssue.Title)
	}
}

func TestListIssues(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	for i := 0; i < 3; i++ {
		args := &CreateArgs{
			Title:     "Test Issue",
			IssueType: "task",
			Priority:  2,
		}
		if _, err := client.Create(args); err != nil {
			t.Fatalf("Create failed: %v", err)
		}
	}

	listArgs := &ListArgs{
		Limit: 10,
	}

	resp, err := client.List(listArgs)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	var issues []types.Issue
	if err := json.Unmarshal(resp.Data, &issues); err != nil {
		t.Fatalf("Failed to unmarshal issues: %v", err)
	}

	if len(issues) != 3 {
		t.Errorf("Expected 3 issues, got %d", len(issues))
	}
}

func TestSocketCleanup(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-rpc-cleanup-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	socketPath := filepath.Join(tmpDir, "bd.sock")

	store, err := sqlitestorage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	server := NewServer(socketPath, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		t.Fatal("Socket file not created")
	}

	if err := server.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatal("Socket file not cleaned up")
	}
}

func TestConcurrentRequests(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	done := make(chan bool)
	errors := make(chan error, 5)

	for i := 0; i < 5; i++ {
		go func(n int) {
			args := &CreateArgs{
				Title:     "Concurrent Issue",
				IssueType: "task",
				Priority:  2,
			}

			if _, err := client.Create(args); err != nil {
				errors <- err
			}
			done <- true
		}(i)
	}

	for i := 0; i < 5; i++ {
		<-done
	}

	close(errors)
	for err := range errors {
		if err != nil {
			t.Errorf("Concurrent request failed: %v", err)
		}
	}
}
