package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlitestorage "github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestStressConcurrentAgents tests 4+ concurrent agents creating issues
func TestStressConcurrentAgents(t *testing.T) {
	server, _, cleanup := setupTestServer(t)
	defer cleanup()

	socketPath := server.socketPath
	numAgents := 8
	issuesPerAgent := 100

	var wg sync.WaitGroup
	errors := make(chan error, numAgents)
	successCount := int32(0)

	for i := 0; i < numAgents; i++ {
		wg.Add(1)
		go func(agentID int) {
			defer wg.Done()

			client, err := TryConnect(socketPath)
			if err != nil {
				errors <- fmt.Errorf("agent %d: failed to connect: %w", agentID, err)
				return
			}
			defer client.Close()

			for j := 0; j < issuesPerAgent; j++ {
				args := &CreateArgs{
					Title:       fmt.Sprintf("Agent %d Issue %d", agentID, j),
					Description: fmt.Sprintf("Created by agent %d", agentID),
					IssueType:   "task",
					Priority:    2,
				}

				if _, err := client.Create(args); err != nil {
					errors <- fmt.Errorf("agent %d issue %d: %w", agentID, j, err)
					return
				}
				atomic.AddInt32(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("Concurrent agent error: %v", err)
	}

	expectedCount := int32(numAgents * issuesPerAgent)
	if successCount != expectedCount {
		t.Errorf("Expected %d successful creates, got %d", expectedCount, successCount)
	}
}

// TestStressBatchOperations tests batch operations under load
func TestStressBatchOperations(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	createArgs1 := &CreateArgs{
		Title:     "Batch Issue 1",
		IssueType: "task",
		Priority:  1,
	}
	createArgs2 := &CreateArgs{
		Title:     "Batch Issue 2",
		IssueType: "task",
		Priority:  2,
	}

	createArgs1JSON, _ := json.Marshal(createArgs1)
	createArgs2JSON, _ := json.Marshal(createArgs2)

	batchArgs := &BatchArgs{
		Operations: []BatchOperation{
			{Operation: OpCreate, Args: createArgs1JSON},
			{Operation: OpCreate, Args: createArgs2JSON},
		},
	}

	resp, err := client.Batch(batchArgs)
	if err != nil {
		t.Fatalf("Batch failed: %v", err)
	}

	var batchResp BatchResponse
	if err := json.Unmarshal(resp.Data, &batchResp); err != nil {
		t.Fatalf("Failed to unmarshal batch response: %v", err)
	}

	if len(batchResp.Results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(batchResp.Results))
	}

	for i, result := range batchResp.Results {
		if !result.Success {
			t.Errorf("Operation %d failed: %s", i, result.Error)
		}
	}

	socketPath := server.socketPath
	numAgents := 4
	batchesPerAgent := 50

	var wg sync.WaitGroup
	errors := make(chan error, numAgents)

	for i := 0; i < numAgents; i++ {
		wg.Add(1)
		go func(agentID int) {
			defer wg.Done()

			client, err := TryConnect(socketPath)
			if err != nil {
				errors <- fmt.Errorf("agent %d: failed to connect: %w", agentID, err)
				return
			}
			defer client.Close()

			for j := 0; j < batchesPerAgent; j++ {
				createArgs1 := &CreateArgs{
					Title:     fmt.Sprintf("Agent %d Batch %d Issue 1", agentID, j),
					IssueType: "task",
					Priority:  1,
				}
				createArgs2 := &CreateArgs{
					Title:     fmt.Sprintf("Agent %d Batch %d Issue 2", agentID, j),
					IssueType: "bug",
					Priority:  0,
				}

				createArgs1JSON, _ := json.Marshal(createArgs1)
				createArgs2JSON, _ := json.Marshal(createArgs2)

				batchArgs := &BatchArgs{
					Operations: []BatchOperation{
						{Operation: OpCreate, Args: createArgs1JSON},
						{Operation: OpCreate, Args: createArgs2JSON},
					},
				}

				if _, err := client.Batch(batchArgs); err != nil {
					errors <- fmt.Errorf("agent %d batch %d: %w", agentID, j, err)
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("Batch stress error: %v", err)
	}
}

// TestStressMixedOperations tests concurrent mixed operations
func TestStressMixedOperations(t *testing.T) {
	server, _, cleanup := setupTestServer(t)
	defer cleanup()

	socketPath := server.socketPath
	numAgents := 6
	opsPerAgent := 50

	setupClient, err := TryConnect(socketPath)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer setupClient.Close()

	baseIssues := make([]string, 10)
	for i := 0; i < 10; i++ {
		args := &CreateArgs{
			Title:     fmt.Sprintf("Base Issue %d", i),
			IssueType: "task",
			Priority:  2,
		}
		resp, err := setupClient.Create(args)
		if err != nil {
			t.Fatalf("Failed to create base issue: %v", err)
		}
		var issue types.Issue
		json.Unmarshal(resp.Data, &issue)
		baseIssues[i] = issue.ID
	}

	var wg sync.WaitGroup
	errors := make(chan error, numAgents)

	for i := 0; i < numAgents; i++ {
		wg.Add(1)
		go func(agentID int) {
			defer wg.Done()

			client, err := TryConnect(socketPath)
			if err != nil {
				errors <- fmt.Errorf("agent %d: failed to connect: %w", agentID, err)
				return
			}
			defer client.Close()

			for j := 0; j < opsPerAgent; j++ {
				opType := j % 5

				switch opType {
				case 0:
					args := &CreateArgs{
						Title:     fmt.Sprintf("Agent %d New Issue %d", agentID, j),
						IssueType: "task",
						Priority:  2,
					}
					if _, err := client.Create(args); err != nil {
						errors <- fmt.Errorf("agent %d create: %w", agentID, err)
						return
					}

				case 1:
					issueID := baseIssues[j%len(baseIssues)]
					newTitle := fmt.Sprintf("Updated by agent %d", agentID)
					args := &UpdateArgs{
						ID:    issueID,
						Title: &newTitle,
					}
					if _, err := client.Update(args); err != nil {
						errors <- fmt.Errorf("agent %d update: %w", agentID, err)
						return
					}

				case 2:
					issueID := baseIssues[j%len(baseIssues)]
					args := &ShowArgs{ID: issueID}
					if _, err := client.Show(args); err != nil {
						errors <- fmt.Errorf("agent %d show: %w", agentID, err)
						return
					}

				case 3:
					args := &ListArgs{Limit: 10}
					if _, err := client.List(args); err != nil {
						errors <- fmt.Errorf("agent %d list: %w", agentID, err)
						return
					}

				case 4:
					args := &ReadyArgs{Limit: 5}
					if _, err := client.Ready(args); err != nil {
						errors <- fmt.Errorf("agent %d ready: %w", agentID, err)
						return
					}
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("Mixed operations error: %v", err)
	}
}

// TestStressTimeouts tests timeout handling
func TestStressTimeouts(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	client.SetTimeout(5 * time.Second)

	args := &CreateArgs{
		Title:     "Timeout Test",
		IssueType: "task",
		Priority:  2,
	}

	if _, err := client.Create(args); err != nil {
		t.Fatalf("Create with timeout failed: %v", err)
	}

	client.SetTimeout(1 * time.Nanosecond)
	if _, err := client.Create(args); err == nil {
		t.Error("Expected timeout error, got success")
	}
}

// TestStressNoUniqueConstraintViolations verifies no ID collisions
func TestStressNoUniqueConstraintViolations(t *testing.T) {
	// Save current directory and change to temp dir to prevent client from using project database
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	
	tmpDir, err := os.MkdirTemp("", "bd-stress-unique-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	
	// Change to temp dir so client.ExecuteWithCwd uses THIS directory, not project directory
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}
	defer os.Chdir(origWd)

	// Create .beads subdirectory so findDatabaseForCwd finds THIS database, not project's
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	socketPath := filepath.Join(beadsDir, "bd.sock")

	store, err := sqlitestorage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	server := NewServer(socketPath, store)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := server.Start(ctx); err != nil {
			t.Logf("Server error: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	defer func() {
		cancel()
		server.Stop()
		store.Close()
	}()

	numAgents := 10
	issuesPerAgent := 100

	var wg sync.WaitGroup
	errors := make(chan error, numAgents)
	issueIDs := make(chan string, numAgents*issuesPerAgent)

	for i := 0; i < numAgents; i++ {
		wg.Add(1)
		go func(agentID int) {
			defer wg.Done()

			client, err := TryConnect(socketPath)
			if err != nil {
				errors <- fmt.Errorf("agent %d: failed to connect: %w", agentID, err)
				return
			}
			defer client.Close()

			for j := 0; j < issuesPerAgent; j++ {
				args := &CreateArgs{
					Title:     fmt.Sprintf("Agent %d Issue %d", agentID, j),
					IssueType: "task",
					Priority:  2,
				}

				resp, err := client.Create(args)
				if err != nil {
					errors <- fmt.Errorf("agent %d issue %d: %w", agentID, j, err)
					return
				}

				var issue types.Issue
				if err := json.Unmarshal(resp.Data, &issue); err != nil {
					errors <- fmt.Errorf("agent %d unmarshal: %w", agentID, err)
					return
				}

				issueIDs <- issue.ID
			}
		}(i)
	}

	wg.Wait()
	close(errors)
	close(issueIDs)

	for err := range errors {
		t.Errorf("Unique constraint test error: %v", err)
	}

	idSet := make(map[string]bool)
	duplicates := []string{}

	for id := range issueIDs {
		if idSet[id] {
			duplicates = append(duplicates, id)
		}
		idSet[id] = true
	}

	if len(duplicates) > 0 {
		t.Errorf("Found %d duplicate IDs: %v", len(duplicates), duplicates)
	}

	expectedCount := numAgents * issuesPerAgent
	if len(idSet) != expectedCount {
		t.Errorf("Expected %d unique IDs, got %d", expectedCount, len(idSet))
	}
}
