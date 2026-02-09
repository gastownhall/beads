package rpc

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/memory"
)

// TestDepAdd_JSONOutput verifies that handleDepAdd returns JSON data in Response.Data.
// This test is expected to FAIL until the bug is fixed (GH#952 Issue 2).
func TestDepAdd_JSONOutput(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	// Create two test issues for the dependency relationship
	createArgs1 := &CreateArgs{
		Title:     "Issue that depends on another",
		IssueType: "task",
		Priority:  2,
	}
	resp1, err := client.Create(createArgs1)
	if err != nil {
		t.Fatalf("Failed to create first issue: %v", err)
	}
	var issue1 struct{ ID string }
	if err := json.Unmarshal(resp1.Data, &issue1); err != nil {
		t.Fatalf("Failed to unmarshal first issue: %v", err)
	}

	createArgs2 := &CreateArgs{
		Title:     "Issue being depended upon",
		IssueType: "task",
		Priority:  2,
	}
	resp2, err := client.Create(createArgs2)
	if err != nil {
		t.Fatalf("Failed to create second issue: %v", err)
	}
	var issue2 struct{ ID string }
	if err := json.Unmarshal(resp2.Data, &issue2); err != nil {
		t.Fatalf("Failed to unmarshal second issue: %v", err)
	}

	// Add dependency: issue1 depends on issue2
	depArgs := &DepAddArgs{
		FromID:  issue1.ID,
		ToID:    issue2.ID,
		DepType: "blocks",
	}
	resp, err := client.AddDependency(depArgs)
	if err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	// BUG: Response.Data is nil when it should contain JSON
	if resp.Data == nil {
		t.Errorf("resp.Data is nil; expected JSON output with {status, issue_id, depends_on_id, type}")
	}

	// Verify JSON structure matches expected format
	if resp.Data != nil {
		var result struct {
			Status      string `json:"status"`
			IssueID     string `json:"issue_id"`
			DependsOnID string `json:"depends_on_id"`
			Type        string `json:"type"`
		}
		if err := json.Unmarshal(resp.Data, &result); err != nil {
			t.Errorf("Failed to unmarshal response data: %v", err)
		}
		if result.Status != "added" {
			t.Errorf("Expected status='added', got %q", result.Status)
		}
		if result.IssueID != issue1.ID {
			t.Errorf("Expected issue_id=%q, got %q", issue1.ID, result.IssueID)
		}
		if result.DependsOnID != issue2.ID {
			t.Errorf("Expected depends_on_id=%q, got %q", issue2.ID, result.DependsOnID)
		}
		if result.Type != "blocks" {
			t.Errorf("Expected type='blocks', got %q", result.Type)
		}
	}

	// Silence unused variable warning
	_ = store
}

// TestDepRemove_JSONOutput verifies that handleDepRemove returns JSON data in Response.Data.
// This test is expected to FAIL until the bug is fixed (GH#952 Issue 2).
func TestDepRemove_JSONOutput(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	// Create two test issues
	createArgs1 := &CreateArgs{
		Title:     "Issue with dependency to remove",
		IssueType: "task",
		Priority:  2,
	}
	resp1, err := client.Create(createArgs1)
	if err != nil {
		t.Fatalf("Failed to create first issue: %v", err)
	}
	var issue1 struct{ ID string }
	if err := json.Unmarshal(resp1.Data, &issue1); err != nil {
		t.Fatalf("Failed to unmarshal first issue: %v", err)
	}

	createArgs2 := &CreateArgs{
		Title:     "Dependency target issue",
		IssueType: "task",
		Priority:  2,
	}
	resp2, err := client.Create(createArgs2)
	if err != nil {
		t.Fatalf("Failed to create second issue: %v", err)
	}
	var issue2 struct{ ID string }
	if err := json.Unmarshal(resp2.Data, &issue2); err != nil {
		t.Fatalf("Failed to unmarshal second issue: %v", err)
	}

	// First add a dependency so we can remove it
	addArgs := &DepAddArgs{
		FromID:  issue1.ID,
		ToID:    issue2.ID,
		DepType: "blocks",
	}
	_, err = client.AddDependency(addArgs)
	if err != nil {
		t.Fatalf("AddDependency (setup) failed: %v", err)
	}

	// Now remove the dependency
	removeArgs := &DepRemoveArgs{
		FromID: issue1.ID,
		ToID:   issue2.ID,
	}
	resp, err := client.RemoveDependency(removeArgs)
	if err != nil {
		t.Fatalf("RemoveDependency failed: %v", err)
	}

	// BUG: Response.Data is nil when it should contain JSON
	if resp.Data == nil {
		t.Errorf("resp.Data is nil; expected JSON output with {status, issue_id, depends_on_id}")
	}

	// Verify JSON structure matches expected format
	if resp.Data != nil {
		var result struct {
			Status      string `json:"status"`
			IssueID     string `json:"issue_id"`
			DependsOnID string `json:"depends_on_id"`
		}
		if err := json.Unmarshal(resp.Data, &result); err != nil {
			t.Errorf("Failed to unmarshal response data: %v", err)
		}
		if result.Status != "removed" {
			t.Errorf("Expected status='removed', got %q", result.Status)
		}
		if result.IssueID != issue1.ID {
			t.Errorf("Expected issue_id=%q, got %q", issue1.ID, result.IssueID)
		}
		if result.DependsOnID != issue2.ID {
			t.Errorf("Expected depends_on_id=%q, got %q", issue2.ID, result.DependsOnID)
		}
	}

	// Silence unused variable warning
	_ = store
}

// TestHandleSimpleStoreOp_MutationIssueID verifies that handleDepRemove,
// handleLabelAdd, and handleLabelRemove emit mutation events with the correct
// (non-empty) issueID. This was broken when the issueID was passed as a string
// parameter (evaluated before json.Unmarshal) instead of a closure.
func TestHandleSimpleStoreOp_MutationIssueID(t *testing.T) {
	tmpDir := t.TempDir()
	store := memory.New(filepath.Join(tmpDir, "test.jsonl"))
	server := NewServer(filepath.Join(tmpDir, "test.sock"), store, tmpDir, filepath.Join(tmpDir, "test.db"))

	// Create two test issues
	createJSON1, _ := json.Marshal(CreateArgs{Title: "Issue A", IssueType: "task", Priority: 2})
	resp1 := server.handleCreate(&Request{Operation: OpCreate, Args: createJSON1, Actor: "test"})
	if !resp1.Success {
		t.Fatalf("create issue A failed: %s", resp1.Error)
	}
	var issueA struct{ ID string }
	json.Unmarshal(resp1.Data, &issueA)

	createJSON2, _ := json.Marshal(CreateArgs{Title: "Issue B", IssueType: "task", Priority: 2})
	resp2 := server.handleCreate(&Request{Operation: OpCreate, Args: createJSON2, Actor: "test"})
	if !resp2.Success {
		t.Fatalf("create issue B failed: %s", resp2.Error)
	}
	var issueB struct{ ID string }
	json.Unmarshal(resp2.Data, &issueB)

	t.Run("handleDepRemove emits non-empty issueID", func(t *testing.T) {
		// Add dependency first
		addJSON, _ := json.Marshal(DepAddArgs{FromID: issueA.ID, ToID: issueB.ID, DepType: "blocks"})
		server.handleDepAdd(&Request{Operation: OpDepAdd, Args: addJSON, Actor: "test"})

		time.Sleep(10 * time.Millisecond)
		checkpoint := time.Now().UnixMilli()
		time.Sleep(10 * time.Millisecond)

		removeJSON, _ := json.Marshal(DepRemoveArgs{FromID: issueA.ID, ToID: issueB.ID})
		resp := server.handleDepRemove(&Request{Operation: OpDepRemove, Args: removeJSON, Actor: "test"})
		if !resp.Success {
			t.Fatalf("handleDepRemove failed: %s", resp.Error)
		}

		mutations := server.GetRecentMutations(checkpoint)
		found := false
		for _, m := range mutations {
			if m.Type == MutationUpdate && m.IssueID == issueA.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected mutation event with issueID=%q, got mutations: %+v", issueA.ID, mutations)
		}
	})

	t.Run("handleLabelAdd emits non-empty issueID", func(t *testing.T) {
		time.Sleep(10 * time.Millisecond)
		checkpoint := time.Now().UnixMilli()
		time.Sleep(10 * time.Millisecond)

		labelJSON, _ := json.Marshal(LabelAddArgs{ID: issueA.ID, Label: "test-label"})
		resp := server.handleLabelAdd(&Request{Operation: OpLabelAdd, Args: labelJSON, Actor: "test"})
		if !resp.Success {
			t.Fatalf("handleLabelAdd failed: %s", resp.Error)
		}

		mutations := server.GetRecentMutations(checkpoint)
		found := false
		for _, m := range mutations {
			if m.Type == MutationUpdate && m.IssueID == issueA.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected mutation event with issueID=%q, got mutations: %+v", issueA.ID, mutations)
		}
	})

	t.Run("handleLabelRemove emits non-empty issueID", func(t *testing.T) {
		time.Sleep(10 * time.Millisecond)
		checkpoint := time.Now().UnixMilli()
		time.Sleep(10 * time.Millisecond)

		labelJSON, _ := json.Marshal(LabelRemoveArgs{ID: issueA.ID, Label: "test-label"})
		resp := server.handleLabelRemove(&Request{Operation: OpLabelRemove, Args: labelJSON, Actor: "test"})
		if !resp.Success {
			t.Fatalf("handleLabelRemove failed: %s", resp.Error)
		}

		mutations := server.GetRecentMutations(checkpoint)
		found := false
		for _, m := range mutations {
			if m.Type == MutationUpdate && m.IssueID == issueA.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected mutation event with issueID=%q, got mutations: %+v", issueA.ID, mutations)
		}
	})
}

// TestDepTree_HandlerReturnsTree verifies that handleDepTree returns a dependency tree via RPC.
func TestDepTree_HandlerReturnsTree(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	// Create two issues and a dependency between them
	resp1, err := client.Create(&CreateArgs{Title: "Root issue", IssueType: "task", Priority: 2})
	if err != nil {
		t.Fatalf("Failed to create first issue: %v", err)
	}
	var issue1 struct{ ID string }
	if err := json.Unmarshal(resp1.Data, &issue1); err != nil {
		t.Fatalf("Failed to unmarshal first issue: %v", err)
	}

	resp2, err := client.Create(&CreateArgs{Title: "Dependency issue", IssueType: "task", Priority: 2})
	if err != nil {
		t.Fatalf("Failed to create second issue: %v", err)
	}
	var issue2 struct{ ID string }
	if err := json.Unmarshal(resp2.Data, &issue2); err != nil {
		t.Fatalf("Failed to unmarshal second issue: %v", err)
	}

	// issue1 depends on issue2
	_, err = client.AddDependency(&DepAddArgs{FromID: issue1.ID, ToID: issue2.ID, DepType: "blocks"})
	if err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	// Get dependency tree via RPC
	treeResp, err := client.GetDependencyTree(&DepTreeArgs{ID: issue1.ID, MaxDepth: 10})
	if err != nil {
		t.Fatalf("GetDependencyTree RPC failed: %v", err)
	}
	if !treeResp.Success {
		t.Fatalf("GetDependencyTree returned error: %s", treeResp.Error)
	}
	if treeResp.Data == nil {
		t.Fatal("GetDependencyTree returned nil data")
	}

	// Verify we got a non-empty tree
	var nodes []json.RawMessage
	if err := json.Unmarshal(treeResp.Data, &nodes); err != nil {
		t.Fatalf("Failed to unmarshal tree data: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("Expected non-empty dependency tree")
	}
}
