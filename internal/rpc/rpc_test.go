package rpc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

	// Create .beads subdirectory so findDatabaseForCwd finds THIS database, not project's
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	socketPath := filepath.Join(beadsDir, "bd.sock")

	// Ensure socket doesn't exist from previous failed test
	os.Remove(socketPath)

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

	// Wait for server to be ready
	maxWait := 50
	for i := 0; i < maxWait; i++ {
		time.Sleep(10 * time.Millisecond)
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		if i == maxWait-1 {
			cancel()
			server.Stop()
			store.Close()
			os.RemoveAll(tmpDir)
			t.Fatalf("Server socket not created after waiting")
		}
	}

	// Change to tmpDir so client's os.Getwd() finds the test database
	originalWd, err := os.Getwd()
	if err != nil {
		cancel()
		server.Stop()
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to get working directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		cancel()
		server.Stop()
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to change directory: %v", err)
	}

	client, err := TryConnect(socketPath)
	if err != nil {
		cancel()
		server.Stop()
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to connect client: %v", err)
	}
	
	if client == nil {
		cancel()
		server.Stop()
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("Client is nil after connection")
	}
	
	// Set the client's dbPath to the test database so it doesn't route to the wrong DB
	client.dbPath = dbPath

	cleanup := func() {
		client.Close()
		cancel()
		server.Stop()
		store.Close()
		os.Chdir(originalWd) // Restore original working directory
		os.RemoveAll(tmpDir)
	}

	return server, client, cleanup
}

// setupTestServerIsolated creates an isolated test server in a temp directory
// with .beads structure, but allows the caller to customize server/client setup.
// Returns tmpDir, beadsDir, dbPath, socketPath, and cleanup function.
// Caller must change to tmpDir if needed and set client.dbPath manually.
func setupTestServerIsolated(t *testing.T) (tmpDir, beadsDir, dbPath, socketPath string, cleanup func()) {
	tmpDir, err := os.MkdirTemp("", "bd-rpc-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create .beads subdirectory so findDatabaseForCwd finds THIS database, not project's
	beadsDir = filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath = filepath.Join(beadsDir, "test.db")
	socketPath = filepath.Join(beadsDir, "bd.sock")

	// Ensure socket doesn't exist from previous failed test
	os.Remove(socketPath)

	cleanup = func() {
		os.RemoveAll(tmpDir)
	}

	return tmpDir, beadsDir, dbPath, socketPath, cleanup
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

	// Start server in goroutine
	started := make(chan error, 1)
	go func() {
		err := server.Start(ctx)
		if err != nil {
			started <- err
		}
	}()

	// Wait for socket to be created (with timeout)
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	socketReady := false
	for !socketReady {
		select {
		case err := <-started:
			t.Fatalf("Server failed to start: %v", err)
		case <-timeout:
			t.Fatal("Timeout waiting for socket creation")
		case <-ticker.C:
			if _, err := os.Stat(socketPath); err == nil {
				socketReady = true
			}
		}
	}

	if err := server.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatal("Socket file not cleaned up")
	}
}

func TestConcurrentRequests(t *testing.T) {
	server, _, cleanup := setupTestServer(t)
	defer cleanup()

	done := make(chan bool)
	errors := make(chan error, 5)

	for i := 0; i < 5; i++ {
		go func(n int) {
			client, err := TryConnect(server.socketPath)
			if err != nil {
				errors <- err
				done <- true
				return
			}
			defer client.Close()

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

func TestDatabaseHandshake(t *testing.T) {
	// Save original directory and change to a temp directory for test isolation
	origDir, _ := os.Getwd()
	
	// Create two separate databases and daemons
	tmpDir1, err := os.MkdirTemp("", "bd-test-db1-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir 1: %v", err)
	}
	defer os.RemoveAll(tmpDir1)

	tmpDir2, err := os.MkdirTemp("", "bd-test-db2-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir 2: %v", err)
	}
	defer os.RemoveAll(tmpDir2)

	// Setup first daemon (db1)
	beadsDir1 := filepath.Join(tmpDir1, ".beads")
	os.MkdirAll(beadsDir1, 0755)
	dbPath1 := filepath.Join(beadsDir1, "db1.db")
	socketPath1 := filepath.Join(beadsDir1, "bd.sock")
	store1, err := sqlitestorage.New(dbPath1)
	if err != nil {
		t.Fatalf("Failed to create store 1: %v", err)
	}
	defer store1.Close()

	server1 := NewServer(socketPath1, store1)
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	go server1.Start(ctx1)
	defer server1.Stop()
	time.Sleep(100 * time.Millisecond)

	// Setup second daemon (db2)
	beadsDir2 := filepath.Join(tmpDir2, ".beads")
	os.MkdirAll(beadsDir2, 0755)
	dbPath2 := filepath.Join(beadsDir2, "db2.db")
	socketPath2 := filepath.Join(beadsDir2, "bd.sock")
	store2, err := sqlitestorage.New(dbPath2)
	if err != nil {
		t.Fatalf("Failed to create store 2: %v", err)
	}
	defer store2.Close()

	server2 := NewServer(socketPath2, store2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go server2.Start(ctx2)
	defer server2.Stop()
	time.Sleep(100 * time.Millisecond)

	// Test 1: Client with correct ExpectedDB should succeed
	// Change to tmpDir1 so cwd resolution doesn't find other databases
	os.Chdir(tmpDir1)
	defer os.Chdir(origDir)
	
	client1, err := TryConnect(socketPath1)
	if err != nil {
		t.Fatalf("Failed to connect to server 1: %v", err)
	}
	if client1 == nil {
		t.Fatal("client1 is nil")
	}
	defer client1.Close()

	client1.SetDatabasePath(dbPath1)

	args := &CreateArgs{
		Title:     "Test Issue",
		IssueType: "task",
		Priority:  2,
	}
	_, err = client1.Create(args)
	if err != nil {
		t.Errorf("Create with correct database should succeed: %v", err)
	}

	// Test 2: Client with wrong ExpectedDB should fail
	client2, err := TryConnect(socketPath1) // Connect to server1
	if err != nil {
		t.Fatalf("Failed to connect to server 1: %v", err)
	}
	defer client2.Close()

	// But set ExpectedDB to db2 (mismatch!)
	client2.SetDatabasePath(dbPath2)

	_, err = client2.Create(args)
	if err == nil {
		t.Error("Create with wrong database should fail")
	} else if !strings.Contains(err.Error(), "database mismatch:") {
		t.Errorf("Expected 'database mismatch' error, got: %v", err)
	}

	// Test 3: Client without ExpectedDB should succeed (backward compat)
	client3, err := TryConnect(socketPath1)
	if err != nil {
		t.Fatalf("Failed to connect to server 1: %v", err)
	}
	defer client3.Close()

	// Don't set database path (old client behavior)
	_, err = client3.Create(args)
	if err != nil {
		t.Errorf("Create without ExpectedDB should succeed (backward compat): %v", err)
	}
}
