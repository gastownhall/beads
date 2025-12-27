//go:build integration
// +build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

// TestDaemonAutoImportAfterGitPull tests bd-09b5f2f5 fix
// Verifies that daemon automatically imports JSONL after git pull updates it
func TestDaemonAutoImportAfterGitPull(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temp directory with short name to avoid socket path length issues
	tempDir, err := os.MkdirTemp("", "bd-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)
	
	// Create "remote" repository
	remoteDir := filepath.Join(tempDir, "remote")
	if err := os.MkdirAll(remoteDir, 0750); err != nil {
		t.Fatalf("Failed to create remote dir: %v", err)
	}
	
	// Initialize remote git repo
	runGitCmd(t, remoteDir, "init", "--bare")
	
	// Create "clone1" repository (Agent A)
	clone1Dir := filepath.Join(tempDir, "clone1")
	runGitCmd(t, tempDir, "clone", remoteDir, clone1Dir)
	configureGit(t, clone1Dir)
	
	// Initialize beads in clone1
	clone1BeadsDir := filepath.Join(clone1Dir, ".beads")
	if err := os.MkdirAll(clone1BeadsDir, 0750); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}
	
	clone1DBPath := filepath.Join(clone1BeadsDir, "test.db")
	clone1Store := newTestStore(t, clone1DBPath)
	defer clone1Store.Close()
	
	ctx := context.Background()
	if err := clone1Store.SetMetadata(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}
	
	// Create an open issue in clone1
	issue := &types.Issue{
		Title:     "Test daemon auto-import",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := clone1Store.CreateIssue(ctx, issue, "agent-a"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}
	issueID := issue.ID
	
	// Export to JSONL
	jsonlPath := filepath.Join(clone1BeadsDir, "issues.jsonl")
	if err := exportIssuesToJSONL(ctx, clone1Store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}
	
	// Commit and push from clone1
	runGitCmd(t, clone1Dir, "add", ".beads")
	runGitCmd(t, clone1Dir, "commit", "-m", "Add test issue")
	runGitCmd(t, clone1Dir, "push", "origin", "master")
	
	// Create "clone2" repository (Agent B)
	clone2Dir := filepath.Join(tempDir, "clone2")
	runGitCmd(t, tempDir, "clone", remoteDir, clone2Dir)
	configureGit(t, clone2Dir)
	
	// Initialize empty database in clone2
	clone2BeadsDir := filepath.Join(clone2Dir, ".beads")
	clone2DBPath := filepath.Join(clone2BeadsDir, "test.db")
	clone2Store := newTestStore(t, clone2DBPath)
	defer clone2Store.Close()
	
	if err := clone2Store.SetMetadata(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}
	
	// Import initial JSONL in clone2
	clone2JSONLPath := filepath.Join(clone2BeadsDir, "issues.jsonl")
	if err := importJSONLToStore(ctx, clone2Store, clone2DBPath, clone2JSONLPath); err != nil {
		t.Fatalf("Failed to import: %v", err)
	}
	
	// Verify issue exists in clone2
	initialIssue, err := clone2Store.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}
	if initialIssue.Status != types.StatusOpen {
		t.Errorf("Expected status open, got %s", initialIssue.Status)
	}
	
	// NOW THE CRITICAL TEST: Agent A closes the issue and pushes
	t.Run("DaemonAutoImportsAfterGitPull", func(t *testing.T) {
		// Agent A closes the issue
		if err := clone1Store.CloseIssue(ctx, issueID, "Completed", "agent-a"); err != nil {
			t.Fatalf("Failed to close issue: %v", err)
		}
		
		// Agent A exports to JSONL
		if err := exportIssuesToJSONL(ctx, clone1Store, jsonlPath); err != nil {
			t.Fatalf("Failed to export after close: %v", err)
		}
		
		// Agent A commits and pushes
		runGitCmd(t, clone1Dir, "add", ".beads/issues.jsonl")
		runGitCmd(t, clone1Dir, "commit", "-m", "Close issue")
		runGitCmd(t, clone1Dir, "push", "origin", "master")
		
		// Agent B does git pull (updates JSONL on disk)
		runGitCmd(t, clone2Dir, "pull")
		
		// Wait for filesystem to settle after git operations
		// Windows has lower filesystem timestamp precision (typically 100ms)
		// and file I/O may be slower, so we need a longer delay
		if runtime.GOOS == "windows" {
			time.Sleep(500 * time.Millisecond)
		} else {
			time.Sleep(50 * time.Millisecond)
		}
		
		// Start daemon server in clone2
		socketPath := filepath.Join(clone2BeadsDir, "bd.sock")
		os.Remove(socketPath) // Ensure clean state
		
		server := rpc.NewServer(socketPath, clone2Store, clone2Dir, clone2DBPath)
		
		// Start server in background
		serverCtx, serverCancel := context.WithCancel(context.Background())
		defer serverCancel()
		
		go func() {
			if err := server.Start(serverCtx); err != nil {
				t.Logf("Server error: %v", err)
			}
		}()
		
		// Wait for server to be ready
		for i := 0; i < 50; i++ {
			time.Sleep(10 * time.Millisecond)
			if _, err := os.Stat(socketPath); err == nil {
				break
			}
		}
		
		// Simulate a daemon request (like "bd show <issue>")
		// The daemon should auto-import the updated JSONL before responding
		client, err := rpc.TryConnect(socketPath)
		if err != nil {
			t.Fatalf("Failed to connect to daemon: %v", err)
		}
		if client == nil {
			t.Fatal("Client is nil")
		}
		defer client.Close()
		
		client.SetDatabasePath(clone2DBPath) // Route to correct database
		
		// Make a request that triggers auto-import check
		resp, err := client.Execute("show", map[string]string{"id": issueID})
		if err != nil {
			t.Fatalf("Failed to get issue from daemon: %v", err)
		}
		
		// Parse response
		var issue types.Issue
		issueJSON, err := json.Marshal(resp.Data)
		if err != nil {
			t.Fatalf("Failed to marshal response data: %v", err)
		}
		if err := json.Unmarshal(issueJSON, &issue); err != nil {
			t.Fatalf("Failed to unmarshal issue: %v", err)
		}
		
		status := issue.Status
		
		// CRITICAL ASSERTION: Daemon should return CLOSED status from JSONL
		// not stale OPEN status from SQLite
		if status != types.StatusClosed {
			t.Errorf("DAEMON AUTO-IMPORT FAILED: Expected status 'closed' but got '%s'", status)
			t.Errorf("This means daemon is serving stale SQLite data instead of auto-importing JSONL")
			
			// Double-check JSONL has correct status
			jsonlData, _ := os.ReadFile(clone2JSONLPath)
			t.Logf("JSONL content: %s", string(jsonlData))
			
			// Double-check what's in SQLite
			directIssue, _ := clone2Store.GetIssue(ctx, issueID)
			t.Logf("SQLite status: %s", directIssue.Status)
		}
	})
	
	// Additional test: Verify multiple rapid changes
	t.Run("DaemonHandlesRapidUpdates", func(t *testing.T) {
		// Agent A updates priority
		if err := clone1Store.UpdateIssue(ctx, issueID, map[string]interface{}{
			"priority": 0,
		}, "agent-a"); err != nil {
			t.Fatalf("Failed to update priority: %v", err)
		}
		
		if err := exportIssuesToJSONL(ctx, clone1Store, jsonlPath); err != nil {
			t.Fatalf("Failed to export: %v", err)
		}
		
		runGitCmd(t, clone1Dir, "add", ".beads/issues.jsonl")
		runGitCmd(t, clone1Dir, "commit", "-m", "Update priority")
		runGitCmd(t, clone1Dir, "push", "origin", "master")
		
		// Agent B pulls
		runGitCmd(t, clone2Dir, "pull")
		
		// Query via daemon - should see priority 0
		// (Execute forces auto-import synchronously)
		socketPath := filepath.Join(clone2BeadsDir, "bd.sock")
		client, err := rpc.TryConnect(socketPath)
		if err != nil {
			t.Fatalf("Failed to connect to daemon: %v", err)
		}
		defer client.Close()
		
		client.SetDatabasePath(clone2DBPath) // Route to correct database
		
		resp, err := client.Execute("show", map[string]string{"id": issueID})
		if err != nil {
			t.Fatalf("Failed to get issue from daemon: %v", err)
		}
		
		var issue types.Issue
		issueJSON, _ := json.Marshal(resp.Data)
		json.Unmarshal(issueJSON, &issue)
		
		if issue.Priority != 0 {
			t.Errorf("Expected priority 0 after auto-import, got %d", issue.Priority)
		}
	})
}

// TestDaemonAutoImportDataCorruption tests the data corruption scenario
// where Agent B's daemon overwrites Agent A's changes with stale data
func TestDaemonAutoImportDataCorruption(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir, err := os.MkdirTemp("", "bd-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)
	
	// Setup remote and two clones
	remoteDir := filepath.Join(tempDir, "remote")
	os.MkdirAll(remoteDir, 0750)
	runGitCmd(t, remoteDir, "init", "--bare")
	
	clone1Dir := filepath.Join(tempDir, "clone1")
	runGitCmd(t, tempDir, "clone", remoteDir, clone1Dir)
	configureGit(t, clone1Dir)
	
	clone2Dir := filepath.Join(tempDir, "clone2")
	runGitCmd(t, tempDir, "clone", remoteDir, clone2Dir)
	configureGit(t, clone2Dir)
	
	// Initialize beads in both clones
	ctx := context.Background()
	
	// Clone1 setup
	clone1BeadsDir := filepath.Join(clone1Dir, ".beads")
	os.MkdirAll(clone1BeadsDir, 0750)
	clone1DBPath := filepath.Join(clone1BeadsDir, "test.db")
	clone1Store := newTestStore(t, clone1DBPath)
	defer clone1Store.Close()
	clone1Store.SetMetadata(ctx, "issue_prefix", "test")
	
	// Clone2 setup
	clone2BeadsDir := filepath.Join(clone2Dir, ".beads")
	os.MkdirAll(clone2BeadsDir, 0750)
	clone2DBPath := filepath.Join(clone2BeadsDir, "test.db")
	clone2Store := newTestStore(t, clone2DBPath)
	defer clone2Store.Close()
	clone2Store.SetMetadata(ctx, "issue_prefix", "test")
	
	// Agent A creates issue and pushes
	issue2 := &types.Issue{
		Title:     "Shared issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	clone1Store.CreateIssue(ctx, issue2, "agent-a")
	issueID := issue2.ID
	
	clone1JSONLPath := filepath.Join(clone1BeadsDir, "issues.jsonl")
	exportIssuesToJSONL(ctx, clone1Store, clone1JSONLPath)
	runGitCmd(t, clone1Dir, "add", ".beads")
	runGitCmd(t, clone1Dir, "commit", "-m", "Initial issue")
	runGitCmd(t, clone1Dir, "push", "origin", "master")
	
	// Agent B pulls and imports
	runGitCmd(t, clone2Dir, "pull")
	clone2JSONLPath := filepath.Join(clone2BeadsDir, "issues.jsonl")
	importJSONLToStore(ctx, clone2Store, clone2DBPath, clone2JSONLPath)
	
	// THE CORRUPTION SCENARIO:
	// 1. Agent A closes the issue and pushes
	clone1Store.CloseIssue(ctx, issueID, "Done", "agent-a")
	exportIssuesToJSONL(ctx, clone1Store, clone1JSONLPath)
	runGitCmd(t, clone1Dir, "add", ".beads/issues.jsonl")
	runGitCmd(t, clone1Dir, "commit", "-m", "Close issue")
	runGitCmd(t, clone1Dir, "push", "origin", "master")
	
	// 2. Agent B does git pull (JSONL updated on disk)
	runGitCmd(t, clone2Dir, "pull")
	
	// Wait for filesystem to settle after git operations
	time.Sleep(50 * time.Millisecond)
	
	// 3. Agent B daemon exports STALE data (if auto-import doesn't work)
	// This would overwrite Agent A's closure with old "open" status
	
	// Start daemon in clone2
	socketPath := filepath.Join(clone2BeadsDir, "bd.sock")
	os.Remove(socketPath)
	
	server := rpc.NewServer(socketPath, clone2Store, clone2Dir, clone2DBPath)
	
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()
	
	go func() {
		if err := server.Start(serverCtx); err != nil {
			t.Logf("Server error: %v", err)
		}
	}()
	
	// Wait for server
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
	}
	
	// Trigger daemon operation (should auto-import first)
	client, err := rpc.TryConnect(socketPath)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()
	
	client.SetDatabasePath(clone2DBPath)
	
	resp, err := client.Execute("show", map[string]string{"id": issueID})
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}
	
	var issue types.Issue
	issueJSON, _ := json.Marshal(resp.Data)
	json.Unmarshal(issueJSON, &issue)
	
	status := issue.Status
	
	// If daemon didn't auto-import, this would be "open" (stale)
	// With the fix, it should be "closed" (fresh from JSONL)
	if status != types.StatusClosed {
		t.Errorf("DATA CORRUPTION DETECTED: Daemon has stale status '%s' instead of 'closed'", status)
		t.Error("If daemon exports this stale data, it will overwrite Agent A's changes on next push")
	}
	
	// Now simulate daemon export (which happens on timer)
	// With auto-import working, this export should have fresh data
	exportIssuesToJSONL(ctx, clone2Store, clone2JSONLPath)
	
	// Read back JSONL to verify it has correct status
	data, _ := os.ReadFile(clone2JSONLPath)
	var exportedIssue types.Issue
	json.NewDecoder(bytes.NewReader(data)).Decode(&exportedIssue)
	
	if exportedIssue.Status != types.StatusClosed {
		t.Errorf("CORRUPTION: Exported JSONL has wrong status '%s', would overwrite remote", exportedIssue.Status)
	}
}
