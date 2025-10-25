package rpc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestSetTimeout(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()
	defer client.Close()

	client.SetTimeout(5 * time.Second)
	// No crash means success
}

func TestShow(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()
	defer client.Close()

	// Create issue
	createArgs := &CreateArgs{
		Title:     "Show Test",
		IssueType: "task",
		Priority:  1,
	}

	createResp, err := client.Create(createArgs)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	if err := json.Unmarshal(createResp.Data, &issue); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Show issue
	showArgs := &ShowArgs{ID: issue.ID}
	resp, err := client.Show(showArgs)
	if err != nil {
		t.Fatalf("Show failed: %v", err)
	}

	if !resp.Success {
		t.Errorf("Show failed: %s", resp.Error)
	}
}

func TestReady(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()
	defer client.Close()

	args := &ReadyArgs{Limit: 10}
	resp, err := client.Ready(args)
	if err != nil {
		t.Fatalf("Ready failed: %v", err)
	}

	if !resp.Success {
		t.Errorf("Ready failed: %s", resp.Error)
	}
}

func TestStats(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()
	defer client.Close()

	resp, err := client.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	if !resp.Success {
		t.Errorf("Stats failed: %s", resp.Error)
	}
}

func TestAddDependency(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()
	defer client.Close()

	// Create two issues
	issue1, err := client.Create(&CreateArgs{Title: "Issue 1", IssueType: "task", Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	var i1 types.Issue
	json.Unmarshal(issue1.Data, &i1)

	issue2, err := client.Create(&CreateArgs{Title: "Issue 2", IssueType: "task", Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	var i2 types.Issue
	json.Unmarshal(issue2.Data, &i2)

	// Add dependency
	args := &DepAddArgs{FromID: i1.ID, ToID: i2.ID, DepType: "blocks"}
	resp, err := client.AddDependency(args)
	if err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	if !resp.Success {
		t.Errorf("AddDependency failed: %s", resp.Error)
	}
}

func TestRemoveDependency(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()
	defer client.Close()

	// Create issues and add dependency
	issue1, _ := client.Create(&CreateArgs{Title: "Issue 1", IssueType: "task", Priority: 1})
	var i1 types.Issue
	json.Unmarshal(issue1.Data, &i1)

	issue2, _ := client.Create(&CreateArgs{Title: "Issue 2", IssueType: "task", Priority: 1})
	var i2 types.Issue
	json.Unmarshal(issue2.Data, &i2)

	client.AddDependency(&DepAddArgs{FromID: i1.ID, ToID: i2.ID, DepType: "blocks"})

	// Remove dependency
	args := &DepRemoveArgs{FromID: i1.ID, ToID: i2.ID}
	resp, err := client.RemoveDependency(args)
	if err != nil {
		t.Fatalf("RemoveDependency failed: %v", err)
	}

	if !resp.Success {
		t.Errorf("RemoveDependency failed: %s", resp.Error)
	}
}

func TestAddLabel(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()
	defer client.Close()

	// Create issue
	createResp, _ := client.Create(&CreateArgs{Title: "Label Test", IssueType: "task", Priority: 1})
	var issue types.Issue
	json.Unmarshal(createResp.Data, &issue)

	// Add label
	args := &LabelAddArgs{ID: issue.ID, Label: "test"}
	resp, err := client.AddLabel(args)
	if err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}

	if !resp.Success {
		t.Errorf("AddLabel failed: %s", resp.Error)
	}
}

func TestRemoveLabel(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()
	defer client.Close()

	// Create issue with label
	createArgs := &CreateArgs{
		Title:     "Label Test",
		IssueType: "task",
		Priority:  1,
		Labels:    []string{"test"},
	}
	createResp, _ := client.Create(createArgs)
	var issue types.Issue
	json.Unmarshal(createResp.Data, &issue)

	// Remove label
	args := &LabelRemoveArgs{ID: issue.ID, Label: "test"}
	resp, err := client.RemoveLabel(args)
	if err != nil {
		t.Fatalf("RemoveLabel failed: %v", err)
	}

	if !resp.Success {
		t.Errorf("RemoveLabel failed: %s", resp.Error)
	}
}

func TestBatch(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()
	defer client.Close()

	createArgs, _ := json.Marshal(CreateArgs{Title: "Batch 1", IssueType: "task", Priority: 1})
	args := &BatchArgs{
		Operations: []BatchOperation{
			{
				Operation: "create",
				Args:      createArgs,
			},
		},
	}

	resp, err := client.Batch(args)
	if err != nil {
		t.Fatalf("Batch failed: %v", err)
	}

	if !resp.Success {
		t.Errorf("Batch failed: %s", resp.Error)
	}
}

func TestEpicStatus(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()
	defer client.Close()

	// Create an epic with subtasks
	epicArgs := &CreateArgs{
		Title:       "Test Epic",
		Description: "Epic for testing status",
		IssueType:   "epic",
		Priority:    2,
	}
	epicResp, err := client.Create(epicArgs)
	if err != nil {
		t.Fatalf("Create epic failed: %v", err)
	}

	var epic types.Issue
	json.Unmarshal(epicResp.Data, &epic)

	// Create a subtask
	taskArgs := &CreateArgs{
		Title:     "Subtask",
		IssueType: "task",
		Priority:  2,
	}
	taskResp, err := client.Create(taskArgs)
	if err != nil {
		t.Fatalf("Create task failed: %v", err)
	}

	var task types.Issue
	json.Unmarshal(taskResp.Data, &task)

	// Link task to epic
	depArgs := &DepAddArgs{
		FromID:  task.ID,
		ToID:    epic.ID,
		DepType: "parent-child",
	}
	_, err = client.AddDependency(depArgs)
	if err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	// Test EpicStatus with eligibleOnly=false
	epicStatusArgs := &EpicStatusArgs{
		EligibleOnly: false,
	}
	resp, err := client.EpicStatus(epicStatusArgs)
	if err != nil {
		t.Fatalf("EpicStatus failed: %v", err)
	}

	if !resp.Success {
		t.Errorf("EpicStatus failed: %s", resp.Error)
	}

	var epicStatuses []*types.EpicStatus
	if err := json.Unmarshal(resp.Data, &epicStatuses); err != nil {
		t.Fatalf("Failed to unmarshal epic statuses: %v", err)
	}

	// Should find at least one epic
	if len(epicStatuses) == 0 {
		t.Error("Expected at least one epic in status")
	}

	// Test with eligibleOnly=true
	epicStatusArgs.EligibleOnly = true
	resp2, err := client.EpicStatus(epicStatusArgs)
	if err != nil {
		t.Fatalf("EpicStatus (eligible only) failed: %v", err)
	}

	if !resp2.Success {
		t.Errorf("EpicStatus (eligible only) failed: %s", resp2.Error)
	}
}
