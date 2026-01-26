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

	// CRITICAL (bd-2c5a): Verify we're using a temp directory to prevent production pollution
	if !strings.Contains(tmpDir, os.TempDir()) {
		t.Fatalf("PRODUCTION DATABASE POLLUTION RISK (bd-2c5a): tmpDir must be in system temp directory, got: %s", tmpDir)
	}

	// Create .beads subdirectory so findDatabaseForCwd finds THIS database, not project's
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	socketPath := filepath.Join(beadsDir, "bd.sock")

	// Ensure socket doesn't exist from previous failed test
	os.Remove(socketPath)

	store, err := sqlitestorage.New(context.Background(), dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create store: %v", err)
	}

	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Configure Gas Town custom types for test compatibility (bd-find4)
	if err := store.SetConfig(ctx, "types.custom", "molecule,gate,convoy,merge-request,slot,agent,role,rig,event,message"); err != nil {
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to set types.custom: %v", err)
	}

	server := NewServer(socketPath, store, tmpDir, dbPath)

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

	// Change to tmpDir so client's os.Getwd() finds the test database.
	t.Chdir(tmpDir)

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
		os.RemoveAll(tmpDir)
	}

	return server, client, cleanup
}

// setupTestServerIsolated creates an isolated test server in a temp directory
// with .beads structure, but allows the caller to customize server/client setup.
// Returns tmpDir, dbPath, socketPath, and cleanup function.
// Caller must change to tmpDir if needed and set client.dbPath manually.
//
//nolint:unparam // beadsDir is not used by callers but part of test isolation setup
func setupTestServerIsolated(t *testing.T) (tmpDir, beadsDir, dbPath, socketPath string, cleanup func()) {
	tmpDir, err := os.MkdirTemp("", "bd-rpc-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// CRITICAL (bd-2c5a): Verify we're using a temp directory to prevent production pollution
	if !strings.Contains(tmpDir, os.TempDir()) {
		t.Fatalf("PRODUCTION DATABASE POLLUTION RISK (bd-2c5a): tmpDir must be in system temp directory, got: %s", tmpDir)
	}

	// Create .beads subdirectory so findDatabaseForCwd finds THIS database, not project's
	beadsDir = filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
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
	notes := "Some important notes"
	design := "Design details"
	assignee := "alice"
	acceptance := "Acceptance criteria"

	updateArgs := &UpdateArgs{
		ID:                 issue.ID,
		Title:              &newTitle,
		Notes:              &notes,
		Design:             &design,
		Assignee:           &assignee,
		AcceptanceCriteria: &acceptance,
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
	if updatedIssue.Notes != notes {
		t.Errorf("Expected notes %s, got %s", notes, updatedIssue.Notes)
	}
	if updatedIssue.Design != design {
		t.Errorf("Expected design %s, got %s", design, updatedIssue.Design)
	}
	if updatedIssue.Assignee != assignee {
		t.Errorf("Expected assignee %s, got %s", assignee, updatedIssue.Assignee)
	}
	if updatedIssue.AcceptanceCriteria != acceptance {
		t.Errorf("Expected acceptance criteria %s, got %s", acceptance, updatedIssue.AcceptanceCriteria)
	}
}

func TestCloseIssue(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	createArgs := &CreateArgs{
		Title:     "Issue to Close",
		IssueType: "task",
		Priority:  2,
	}

	createResp, err := client.Create(createArgs)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	json.Unmarshal(createResp.Data, &issue)

	if issue.Status != "open" {
		t.Errorf("Expected status 'open', got %s", issue.Status)
	}

	closeArgs := &CloseArgs{
		ID:     issue.ID,
		Reason: "Test completion",
	}

	closeResp, err := client.CloseIssue(closeArgs)
	if err != nil {
		t.Fatalf("CloseIssue failed: %v", err)
	}

	if !closeResp.Success {
		t.Fatalf("Expected success, got error: %s", closeResp.Error)
	}

	var closedIssue types.Issue
	json.Unmarshal(closeResp.Data, &closedIssue)

	if closedIssue.Status != "closed" {
		t.Errorf("Expected status 'closed', got %s", closedIssue.Status)
	}

	if closedIssue.ClosedAt == nil {
		t.Error("Expected ClosedAt to be set, got nil")
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

	store, err := sqlitestorage.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	server := NewServer(socketPath, store, tmpDir, dbPath)

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
		go func(_ int) {
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
	os.MkdirAll(beadsDir1, 0750)
	dbPath1 := filepath.Join(beadsDir1, "db1.db")
	socketPath1 := filepath.Join(beadsDir1, "bd.sock")
	store1 := newTestStore(t, dbPath1)
	defer store1.Close()

	server1 := NewServer(socketPath1, store1, tmpDir1, dbPath1)
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	go server1.Start(ctx1)
	defer server1.Stop()
	time.Sleep(100 * time.Millisecond)

	// Setup second daemon (db2)
	beadsDir2 := filepath.Join(tmpDir2, ".beads")
	os.MkdirAll(beadsDir2, 0750)
	dbPath2 := filepath.Join(beadsDir2, "db2.db")
	socketPath2 := filepath.Join(beadsDir2, "bd.sock")
	store2 := newTestStore(t, dbPath2)
	defer store2.Close()

	server2 := NewServer(socketPath2, store2, tmpDir2, dbPath2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go server2.Start(ctx2)
	defer server2.Stop()
	time.Sleep(100 * time.Millisecond)

	// Test 1: Client with correct ExpectedDB should succeed
	// Change to tmpDir1 so cwd resolution doesn't find other databases.
	t.Chdir(tmpDir1)
	
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

func TestCreate_WithParent(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create parent issue
	parentArgs := &CreateArgs{
		Title:     "Parent Epic",
		IssueType: "epic",
		Priority:  1,
	}

	parentResp, err := client.Create(parentArgs)
	if err != nil {
		t.Fatalf("Create parent failed: %v", err)
	}

	var parent types.Issue
	if err := json.Unmarshal(parentResp.Data, &parent); err != nil {
		t.Fatalf("Failed to unmarshal parent: %v", err)
	}

	// Create child issue using --parent flag
	childArgs := &CreateArgs{
		Parent:    parent.ID,
		Title:     "Child Task",
		IssueType: "task",
		Priority:  1,
	}

	childResp, err := client.Create(childArgs)
	if err != nil {
		t.Fatalf("Create child failed: %v", err)
	}

	var child types.Issue
	if err := json.Unmarshal(childResp.Data, &child); err != nil {
		t.Fatalf("Failed to unmarshal child: %v", err)
	}

	// Verify hierarchical ID format (should be parent.1)
	expectedID := parent.ID + ".1"
	if child.ID != expectedID {
		t.Errorf("Expected child ID %s, got %s", expectedID, child.ID)
	}

	// Create second child
	child2Args := &CreateArgs{
		Parent:    parent.ID,
		Title:     "Second Child Task",
		IssueType: "task",
		Priority:  1,
	}

	child2Resp, err := client.Create(child2Args)
	if err != nil {
		t.Fatalf("Create second child failed: %v", err)
	}

	var child2 types.Issue
	if err := json.Unmarshal(child2Resp.Data, &child2); err != nil {
		t.Fatalf("Failed to unmarshal second child: %v", err)
	}

	// Verify second child has incremented ID (parent.2)
	expectedID2 := parent.ID + ".2"
	if child2.ID != expectedID2 {
		t.Errorf("Expected second child ID %s, got %s", expectedID2, child2.ID)
	}
}

func TestCreate_WithParentAndIDConflict(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create parent issue
	parentArgs := &CreateArgs{
		Title:     "Parent Epic",
		IssueType: "epic",
		Priority:  1,
	}

	parentResp, err := client.Create(parentArgs)
	if err != nil {
		t.Fatalf("Create parent failed: %v", err)
	}

	var parent types.Issue
	if err := json.Unmarshal(parentResp.Data, &parent); err != nil {
		t.Fatalf("Failed to unmarshal parent: %v", err)
	}

	// Try to create with both ID and Parent (should fail)
	conflictArgs := &CreateArgs{
		ID:        "bd-custom",
		Parent:    parent.ID,
		Title:     "Should Fail",
		IssueType: "task",
		Priority:  1,
	}

	resp, err := client.Create(conflictArgs)
	if err == nil && resp.Success {
		t.Fatal("Expected error when both ID and Parent are specified")
	}

	if !strings.Contains(resp.Error, "cannot specify both ID and Parent") {
		t.Errorf("Expected conflict error message, got: %s", resp.Error)
	}
}

func TestCreate_DiscoveredFromInheritsSourceRepo(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a parent issue
	parentArgs := &CreateArgs{
		Title:     "Parent issue",
		IssueType: "task",
		Priority:  1,
	}

	parentResp, err := client.Create(parentArgs)
	if err != nil {
		t.Fatalf("Failed to create parent: %v", err)
	}

	var parentIssue types.Issue
	if err := json.Unmarshal(parentResp.Data, &parentIssue); err != nil {
		t.Fatalf("Failed to unmarshal parent: %v", err)
	}

	// Create discovered issue with discovered-from dependency
	// The logic in handleCreate should check for discovered-from dependencies
	// and inherit the parent's source_repo
	discoveredArgs := &CreateArgs{
		Title:        "Discovered bug",
		IssueType:    "bug",
		Priority:     1,
		Dependencies: []string{"discovered-from:" + parentIssue.ID},
	}

	discoveredResp, err := client.Create(discoveredArgs)
	if err != nil {
		t.Fatalf("Failed to create discovered issue: %v", err)
	}

	var discoveredIssue types.Issue
	if err := json.Unmarshal(discoveredResp.Data, &discoveredIssue); err != nil {
		t.Fatalf("Failed to unmarshal discovered issue: %v", err)
	}

	// Verify the issue was created successfully
	if discoveredIssue.Title != "Discovered bug" {
		t.Errorf("Expected title 'Discovered bug', got %s", discoveredIssue.Title)
	}

	// Note: To fully test source_repo inheritance, we'd need to:
	// 1. Create a parent with custom source_repo (requires direct storage access)
	// 2. Verify the discovered issue inherited it
	// The logic is implemented in server_issues_epics.go handleCreate
	// and tested via the cmd/bd test which has direct storage access
}

func TestRPCCreateWithExternalRef(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create issue with external_ref via RPC
	createArgs := &CreateArgs{
		Title:       "Test issue with external ref",
		Description: "Testing external_ref in daemon mode",
		IssueType:   "bug",
		Priority:    1,
		ExternalRef: "github:303",
	}

	resp, err := client.Create(createArgs)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	if err := json.Unmarshal(resp.Data, &issue); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	// Verify external_ref was saved
	if issue.ExternalRef == nil {
		t.Fatal("Expected ExternalRef to be set, got nil")
	}
	if *issue.ExternalRef != "github:303" {
		t.Errorf("Expected ExternalRef='github:303', got '%s'", *issue.ExternalRef)
	}

	// Verify via Show operation
	showArgs := &ShowArgs{ID: issue.ID}
	resp, err = client.Show(showArgs)
	if err != nil {
		t.Fatalf("Show failed: %v", err)
	}

	var retrieved types.Issue
	if err := json.Unmarshal(resp.Data, &retrieved); err != nil {
		t.Fatalf("Failed to unmarshal show response: %v", err)
	}

	if retrieved.ExternalRef == nil {
		t.Fatal("Expected retrieved ExternalRef to be set, got nil")
	}
	if *retrieved.ExternalRef != "github:303" {
		t.Errorf("Expected retrieved ExternalRef='github:303', got '%s'", *retrieved.ExternalRef)
	}

	_ = server // Silence unused warning
}

func TestRPCUpdateWithExternalRef(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create issue without external_ref
	createArgs := &CreateArgs{
		Title:       "Test issue for update",
		Description: "Testing external_ref update in daemon mode",
		IssueType:   "task",
		Priority:    2,
	}

	resp, err := client.Create(createArgs)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	if err := json.Unmarshal(resp.Data, &issue); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	// Update with external_ref
	newRef := "jira-ABC-123"
	updateArgs := &UpdateArgs{
		ID:          issue.ID,
		ExternalRef: &newRef,
	}

	resp, err = client.Update(updateArgs)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	var updated types.Issue
	if err := json.Unmarshal(resp.Data, &updated); err != nil {
		t.Fatalf("Failed to unmarshal update response: %v", err)
	}

	// Verify external_ref was updated
	if updated.ExternalRef == nil {
		t.Fatal("Expected ExternalRef to be set after update, got nil")
	}
	if *updated.ExternalRef != "jira-ABC-123" {
		t.Errorf("Expected ExternalRef='jira-ABC-123', got '%s'", *updated.ExternalRef)
	}

	// Verify via Show operation
	showArgs := &ShowArgs{ID: issue.ID}
	resp, err = client.Show(showArgs)
	if err != nil {
		t.Fatalf("Show failed: %v", err)
	}

	var retrieved types.Issue
	if err := json.Unmarshal(resp.Data, &retrieved); err != nil {
		t.Fatalf("Failed to unmarshal show response: %v", err)
	}

	if retrieved.ExternalRef == nil {
		t.Fatal("Expected retrieved ExternalRef to be set, got nil")
	}
	if *retrieved.ExternalRef != "jira-ABC-123" {
		t.Errorf("Expected retrieved ExternalRef='jira-ABC-123', got '%s'", *retrieved.ExternalRef)
	}

	_ = server // Silence unused warning
}
