package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/types"
)

// TestHandleCreate_SetsCreatedBy verifies that CreatedBy is passed through RPC and stored (GH#748)
func TestHandleCreate_SetsCreatedBy(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	createArgs := CreateArgs{
		Title:     "Test CreatedBy Field",
		IssueType: "task",
		Priority:  2,
		CreatedBy: "test-actor",
	}
	createJSON, _ := json.Marshal(createArgs)
	createReq := &Request{
		Operation: OpCreate,
		Args:      createJSON,
		Actor:     "test-actor",
	}

	resp := server.handleCreate(createReq)
	if !resp.Success {
		t.Fatalf("create failed: %s", resp.Error)
	}

	var createdIssue types.Issue
	if err := json.Unmarshal(resp.Data, &createdIssue); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Verify CreatedBy was set in the response
	if createdIssue.CreatedBy != "test-actor" {
		t.Errorf("expected CreatedBy 'test-actor' in response, got %q", createdIssue.CreatedBy)
	}

	// Verify CreatedBy was persisted to storage
	storedIssue, err := store.GetIssue(context.Background(), createdIssue.ID)
	if err != nil {
		t.Fatalf("failed to get issue from storage: %v", err)
	}
	if storedIssue.CreatedBy != "test-actor" {
		t.Errorf("expected CreatedBy 'test-actor' in storage, got %q", storedIssue.CreatedBy)
	}
}

func TestEmitMutation(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Emit a mutation
	server.emitMutation(MutationCreate, "bd-123", "Test Issue", "")

	// Check that mutation was stored in buffer
	mutations := server.GetRecentMutations(0)
	if len(mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(mutations))
	}

	if mutations[0].Type != MutationCreate {
		t.Errorf("expected type %s, got %s", MutationCreate, mutations[0].Type)
	}

	if mutations[0].IssueID != "bd-123" {
		t.Errorf("expected issue ID bd-123, got %s", mutations[0].IssueID)
	}
}

func TestGetRecentMutations_EmptyBuffer(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	mutations := server.GetRecentMutations(0)
	if len(mutations) != 0 {
		t.Errorf("expected empty mutations, got %d", len(mutations))
	}
}

func TestGetRecentMutations_TimestampFiltering(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Emit mutations with delays
	server.emitMutation(MutationCreate, "bd-1", "Issue 1", "")
	time.Sleep(10 * time.Millisecond)

	checkpoint := time.Now().UnixMilli()
	time.Sleep(10 * time.Millisecond)

	server.emitMutation(MutationUpdate, "bd-2", "Issue 2", "")
	server.emitMutation(MutationUpdate, "bd-3", "Issue 3", "")

	// Get mutations after checkpoint
	mutations := server.GetRecentMutations(checkpoint)

	if len(mutations) != 2 {
		t.Fatalf("expected 2 mutations after checkpoint, got %d", len(mutations))
	}

	// Verify the mutations are bd-2 and bd-3
	ids := make(map[string]bool)
	for _, m := range mutations {
		ids[m.IssueID] = true
	}

	if !ids["bd-2"] || !ids["bd-3"] {
		t.Errorf("expected bd-2 and bd-3, got %v", ids)
	}

	if ids["bd-1"] {
		t.Errorf("bd-1 should be filtered out by timestamp")
	}
}

func TestGetRecentMutations_CircularBuffer(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Emit more than maxMutationBuffer (100) mutations
	for i := 0; i < 150; i++ {
		server.emitMutation(MutationCreate, "bd-"+string(rune(i)), "", "")
		time.Sleep(time.Millisecond) // Ensure different timestamps
	}

	// Buffer should only keep last 100
	mutations := server.GetRecentMutations(0)
	if len(mutations) != 100 {
		t.Errorf("expected 100 mutations (circular buffer limit), got %d", len(mutations))
	}

	// First mutation should be from iteration 50 (150-100)
	firstID := mutations[0].IssueID
	expectedFirstID := "bd-" + string(rune(50))
	if firstID != expectedFirstID {
		t.Errorf("expected first mutation to be %s (after circular buffer wraparound), got %s", expectedFirstID, firstID)
	}
}

func TestGetRecentMutations_ConcurrentAccess(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Simulate concurrent writes and reads
	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 50; i++ {
			server.emitMutation(MutationUpdate, "bd-write", "", "")
			time.Sleep(time.Millisecond)
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 50; i++ {
			_ = server.GetRecentMutations(0)
			time.Sleep(time.Millisecond)
		}
		done <- true
	}()

	// Wait for both to complete
	<-done
	<-done

	// Verify no race conditions (test will fail with -race flag if there are)
	mutations := server.GetRecentMutations(0)
	if len(mutations) == 0 {
		t.Error("expected some mutations after concurrent access")
	}
}

func TestHandleGetMutations(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Emit some mutations
	server.emitMutation(MutationCreate, "bd-1", "Issue 1", "")
	time.Sleep(10 * time.Millisecond)
	checkpoint := time.Now().UnixMilli()
	time.Sleep(10 * time.Millisecond)
	server.emitMutation(MutationUpdate, "bd-2", "Issue 2", "")

	// Create RPC request
	args := GetMutationsArgs{Since: checkpoint}
	argsJSON, _ := json.Marshal(args)

	req := &Request{
		Operation: OpGetMutations,
		Args:      argsJSON,
	}

	// Handle request
	resp := server.handleGetMutations(req)

	if !resp.Success {
		t.Fatalf("expected successful response, got error: %s", resp.Error)
	}

	// Parse response
	var mutations []MutationEvent
	if err := json.Unmarshal(resp.Data, &mutations); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if len(mutations) != 1 {
		t.Errorf("expected 1 mutation, got %d", len(mutations))
	}

	if len(mutations) > 0 && mutations[0].IssueID != "bd-2" {
		t.Errorf("expected bd-2, got %s", mutations[0].IssueID)
	}
}

func TestHandleGetMutations_InvalidArgs(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create RPC request with invalid JSON
	req := &Request{
		Operation: OpGetMutations,
		Args:      []byte("invalid json"),
	}

	// Handle request
	resp := server.handleGetMutations(req)

	if resp.Success {
		t.Error("expected error response for invalid args")
	}

	if resp.Error == "" {
		t.Error("expected error message")
	}
}

func TestMutationEventTypes(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Test all mutation types
	types := []string{
		MutationCreate,
		MutationUpdate,
		MutationDelete,
		MutationComment,
	}

	for _, mutationType := range types {
		server.emitMutation(mutationType, "bd-test", "", "")
	}

	mutations := server.GetRecentMutations(0)
	if len(mutations) != len(types) {
		t.Fatalf("expected %d mutations, got %d", len(types), len(mutations))
	}

	// Verify each type was stored correctly
	foundTypes := make(map[string]bool)
	for _, m := range mutations {
		foundTypes[m.Type] = true
	}

	for _, expectedType := range types {
		if !foundTypes[expectedType] {
			t.Errorf("expected mutation type %s not found", expectedType)
		}
	}
}

// TestEmitRichMutation verifies that rich mutation events include metadata fields
func TestEmitRichMutation(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Emit a rich status change event
	server.emitRichMutation(MutationEvent{
		Type:      MutationStatus,
		IssueID:   "bd-456",
		OldStatus: "open",
		NewStatus: "in_progress",
	})

	mutations := server.GetRecentMutations(0)
	if len(mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(mutations))
	}

	m := mutations[0]
	if m.Type != MutationStatus {
		t.Errorf("expected type %s, got %s", MutationStatus, m.Type)
	}
	if m.IssueID != "bd-456" {
		t.Errorf("expected issue ID bd-456, got %s", m.IssueID)
	}
	if m.OldStatus != "open" {
		t.Errorf("expected OldStatus 'open', got %s", m.OldStatus)
	}
	if m.NewStatus != "in_progress" {
		t.Errorf("expected NewStatus 'in_progress', got %s", m.NewStatus)
	}
	if m.Timestamp.IsZero() {
		t.Error("expected Timestamp to be set automatically")
	}
}

// TestEmitRichMutation_Bonded verifies bonded events include step count
func TestEmitRichMutation_Bonded(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Emit a bonded event with metadata
	server.emitRichMutation(MutationEvent{
		Type:      MutationBonded,
		IssueID:   "bd-789",
		ParentID:  "bd-parent",
		StepCount: 5,
	})

	mutations := server.GetRecentMutations(0)
	if len(mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(mutations))
	}

	m := mutations[0]
	if m.Type != MutationBonded {
		t.Errorf("expected type %s, got %s", MutationBonded, m.Type)
	}
	if m.ParentID != "bd-parent" {
		t.Errorf("expected ParentID 'bd-parent', got %s", m.ParentID)
	}
	if m.StepCount != 5 {
		t.Errorf("expected StepCount 5, got %d", m.StepCount)
	}
}

func TestMutationTimestamps(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	before := time.Now()
	server.emitMutation(MutationCreate, "bd-123", "Test Issue", "")
	after := time.Now()

	mutations := server.GetRecentMutations(0)
	if len(mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(mutations))
	}

	timestamp := mutations[0].Timestamp
	if timestamp.Before(before) || timestamp.After(after) {
		t.Errorf("mutation timestamp %v is outside expected range [%v, %v]", timestamp, before, after)
	}
}

func TestEmitMutation_NonBlocking(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Don't consume from mutationChan to test non-blocking behavior
	// Fill the buffer (default size is 512 from BEADS_MUTATION_BUFFER or default)
	for i := 0; i < 600; i++ {
		// This should not block even when channel is full
		server.emitMutation(MutationCreate, "bd-test", "", "")
	}

	// Verify mutations were still stored in recent buffer
	mutations := server.GetRecentMutations(0)
	if len(mutations) == 0 {
		t.Error("expected mutations in recent buffer even when channel is full")
	}

	// Verify buffer is capped at 100 (maxMutationBuffer)
	if len(mutations) > 100 {
		t.Errorf("expected at most 100 mutations in buffer, got %d", len(mutations))
	}
}

// TestHandleClose_EmitsStatusMutation verifies that close operations emit MutationStatus events
// with old/new status metadata (bd-313v fix)
func TestHandleClose_EmitsStatusMutation(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create an issue first
	createArgs := CreateArgs{
		Title:     "Test Issue for Close",
		IssueType: "bug",
		Priority:  1,
	}
	createJSON, _ := json.Marshal(createArgs)
	createReq := &Request{
		Operation: OpCreate,
		Args:      createJSON,
		Actor:     "test-user",
	}

	createResp := server.handleCreate(createReq)
	if !createResp.Success {
		t.Fatalf("failed to create test issue: %s", createResp.Error)
	}

	var createdIssue map[string]interface{}
	if err := json.Unmarshal(createResp.Data, &createdIssue); err != nil {
		t.Fatalf("failed to parse created issue: %v", err)
	}
	issueID := createdIssue["id"].(string)

	// Clear mutation buffer
	time.Sleep(10 * time.Millisecond)
	checkpoint := time.Now().UnixMilli()
	time.Sleep(10 * time.Millisecond)

	// Close the issue
	closeArgs := CloseArgs{
		ID:     issueID,
		Reason: "test complete",
	}
	closeJSON, _ := json.Marshal(closeArgs)
	closeReq := &Request{
		Operation: OpClose,
		Args:      closeJSON,
		Actor:     "test-user",
	}

	closeResp := server.handleClose(closeReq)
	if !closeResp.Success {
		t.Fatalf("close operation failed: %s", closeResp.Error)
	}

	// Verify MutationStatus event was emitted with correct metadata
	mutations := server.GetRecentMutations(checkpoint)
	var statusMutation *MutationEvent
	for _, m := range mutations {
		if m.Type == MutationStatus && m.IssueID == issueID {
			statusMutation = &m
			break
		}
	}

	if statusMutation == nil {
		t.Fatalf("expected MutationStatus event for issue %s, but none found in mutations: %+v", issueID, mutations)
	}

	if statusMutation.OldStatus != "open" {
		t.Errorf("expected OldStatus 'open', got %s", statusMutation.OldStatus)
	}
	if statusMutation.NewStatus != "closed" {
		t.Errorf("expected NewStatus 'closed', got %s", statusMutation.NewStatus)
	}
}

// TestHandleUpdate_EmitsStatusMutationOnStatusChange verifies that status updates emit MutationStatus
func TestHandleUpdate_EmitsStatusMutationOnStatusChange(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create an issue first
	createArgs := CreateArgs{
		Title:     "Test Issue for Status Update",
		IssueType: "task",
		Priority:  2,
	}
	createJSON, _ := json.Marshal(createArgs)
	createReq := &Request{
		Operation: OpCreate,
		Args:      createJSON,
		Actor:     "test-user",
	}

	createResp := server.handleCreate(createReq)
	if !createResp.Success {
		t.Fatalf("failed to create test issue: %s", createResp.Error)
	}

	var createdIssue map[string]interface{}
	if err := json.Unmarshal(createResp.Data, &createdIssue); err != nil {
		t.Fatalf("failed to parse created issue: %v", err)
	}
	issueID := createdIssue["id"].(string)

	// Clear mutation buffer
	time.Sleep(10 * time.Millisecond)
	checkpoint := time.Now().UnixMilli()
	time.Sleep(10 * time.Millisecond)

	// Update status to in_progress
	status := "in_progress"
	updateArgs := UpdateArgs{
		ID:     issueID,
		Status: &status,
	}
	updateJSON, _ := json.Marshal(updateArgs)
	updateReq := &Request{
		Operation: OpUpdate,
		Args:      updateJSON,
		Actor:     "test-user",
	}

	updateResp := server.handleUpdate(updateReq)
	if !updateResp.Success {
		t.Fatalf("update operation failed: %s", updateResp.Error)
	}

	// Verify MutationStatus event was emitted
	mutations := server.GetRecentMutations(checkpoint)
	var statusMutation *MutationEvent
	for _, m := range mutations {
		if m.Type == MutationStatus && m.IssueID == issueID {
			statusMutation = &m
			break
		}
	}

	if statusMutation == nil {
		t.Fatalf("expected MutationStatus event, but none found in mutations: %+v", mutations)
	}

	if statusMutation.OldStatus != "open" {
		t.Errorf("expected OldStatus 'open', got %s", statusMutation.OldStatus)
	}
	if statusMutation.NewStatus != "in_progress" {
		t.Errorf("expected NewStatus 'in_progress', got %s", statusMutation.NewStatus)
	}
}

// TestHandleUpdate_EmitsUpdateMutationForNonStatusChanges verifies non-status updates emit MutationUpdate
func TestHandleUpdate_EmitsUpdateMutationForNonStatusChanges(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create an issue first
	createArgs := CreateArgs{
		Title:     "Test Issue for Non-Status Update",
		IssueType: "task",
		Priority:  2,
	}
	createJSON, _ := json.Marshal(createArgs)
	createReq := &Request{
		Operation: OpCreate,
		Args:      createJSON,
		Actor:     "test-user",
	}

	createResp := server.handleCreate(createReq)
	if !createResp.Success {
		t.Fatalf("failed to create test issue: %s", createResp.Error)
	}

	var createdIssue map[string]interface{}
	if err := json.Unmarshal(createResp.Data, &createdIssue); err != nil {
		t.Fatalf("failed to parse created issue: %v", err)
	}
	issueID := createdIssue["id"].(string)

	// Clear mutation buffer
	time.Sleep(10 * time.Millisecond)
	checkpoint := time.Now().UnixMilli()
	time.Sleep(10 * time.Millisecond)

	// Update title (not status)
	newTitle := "Updated Title"
	updateArgs := UpdateArgs{
		ID:    issueID,
		Title: &newTitle,
	}
	updateJSON, _ := json.Marshal(updateArgs)
	updateReq := &Request{
		Operation: OpUpdate,
		Args:      updateJSON,
		Actor:     "test-user",
	}

	updateResp := server.handleUpdate(updateReq)
	if !updateResp.Success {
		t.Fatalf("update operation failed: %s", updateResp.Error)
	}

	// Verify MutationUpdate event was emitted (not MutationStatus)
	mutations := server.GetRecentMutations(checkpoint)
	var updateMutation *MutationEvent
	for _, m := range mutations {
		if m.IssueID == issueID {
			updateMutation = &m
			break
		}
	}

	if updateMutation == nil {
		t.Fatal("expected mutation event, but none found")
	}

	if updateMutation.Type != MutationUpdate {
		t.Errorf("expected MutationUpdate type, got %s", updateMutation.Type)
	}
}

// TestHandleDelete_EmitsMutation verifies that delete operations emit mutation events
// This is a regression test for the issue where delete operations bypass the daemon
// and don't trigger auto-sync. The delete RPC handler should emit MutationDelete events.
func TestHandleDelete_EmitsMutation(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create an issue first
	createArgs := CreateArgs{
		Title:     "Test Issue for Deletion",
		IssueType: "bug",
		Priority:  1,
	}
	createJSON, _ := json.Marshal(createArgs)
	createReq := &Request{
		Operation: OpCreate,
		Args:      createJSON,
		Actor:     "test-user",
	}

	createResp := server.handleCreate(createReq)
	if !createResp.Success {
		t.Fatalf("failed to create test issue: %s", createResp.Error)
	}

	// Parse the created issue to get its ID
	var createdIssue map[string]interface{}
	if err := json.Unmarshal(createResp.Data, &createdIssue); err != nil {
		t.Fatalf("failed to parse created issue: %v", err)
	}
	issueID := createdIssue["id"].(string)

	// Clear mutation buffer to isolate delete event
	_ = server.GetRecentMutations(time.Now().UnixMilli())

	// Now delete the issue via RPC
	deleteArgs := DeleteArgs{
		IDs:    []string{issueID},
		Force:  true,
		Reason: "test deletion",
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	deleteResp := server.handleDelete(deleteReq)
	if !deleteResp.Success {
		t.Fatalf("delete operation failed: %s", deleteResp.Error)
	}

	// Verify mutation event was emitted
	mutations := server.GetRecentMutations(0)
	if len(mutations) == 0 {
		t.Fatal("expected delete mutation event, but no mutations were emitted")
	}

	// Find the delete mutation
	var deleteMutation *MutationEvent
	for _, m := range mutations {
		if m.Type == MutationDelete && m.IssueID == issueID {
			deleteMutation = &m
			break
		}
	}

	if deleteMutation == nil {
		t.Errorf("expected MutationDelete event for issue %s, but none found in mutations: %+v", issueID, mutations)
	}
}

// TestHandleDelete_BatchEmitsMutations verifies batch delete emits mutation for each issue
func TestHandleDelete_BatchEmitsMutations(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create multiple issues
	issueIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		createArgs := CreateArgs{
			Title:     "Test Issue " + string(rune('A'+i)),
			IssueType: "bug",
			Priority:  1,
		}
		createJSON, _ := json.Marshal(createArgs)
		createReq := &Request{
			Operation: OpCreate,
			Args:      createJSON,
			Actor:     "test-user",
		}

		createResp := server.handleCreate(createReq)
		if !createResp.Success {
			t.Fatalf("failed to create test issue %d: %s", i, createResp.Error)
		}

		var createdIssue map[string]interface{}
		if err := json.Unmarshal(createResp.Data, &createdIssue); err != nil {
			t.Fatalf("failed to parse created issue %d: %v", i, err)
		}
		issueIDs[i] = createdIssue["id"].(string)
	}

	// Clear mutation buffer
	_ = server.GetRecentMutations(time.Now().UnixMilli())

	// Batch delete all issues
	deleteArgs := DeleteArgs{
		IDs:    issueIDs,
		Force:  true,
		Reason: "batch test deletion",
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	deleteResp := server.handleDelete(deleteReq)
	if !deleteResp.Success {
		t.Fatalf("batch delete operation failed: %s", deleteResp.Error)
	}

	// Verify mutation events were emitted for each deleted issue
	mutations := server.GetRecentMutations(0)
	deleteMutations := 0
	deletedIDs := make(map[string]bool)

	for _, m := range mutations {
		if m.Type == MutationDelete {
			deleteMutations++
			deletedIDs[m.IssueID] = true
		}
	}

	if deleteMutations != len(issueIDs) {
		t.Errorf("expected %d delete mutations, got %d", len(issueIDs), deleteMutations)
	}

	// Verify all issue IDs have corresponding mutations
	for _, id := range issueIDs {
		if !deletedIDs[id] {
			t.Errorf("no delete mutation found for issue %s", id)
		}
	}
}

// TestHandleDelete_ErrorEmptyIDs verifies error when no issue IDs provided
func TestHandleDelete_ErrorEmptyIDs(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Try to delete with empty IDs
	deleteArgs := DeleteArgs{
		IDs:   []string{},
		Force: true,
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	deleteResp := server.handleDelete(deleteReq)
	if deleteResp.Success {
		t.Error("expected error for empty IDs, but got success")
	}

	if deleteResp.Error == "" {
		t.Error("expected error message for empty IDs")
	}

	// Verify error message mentions missing IDs
	if deleteResp.Error != "no issue IDs provided for deletion" {
		t.Errorf("unexpected error message: %s", deleteResp.Error)
	}
}

// TestHandleDelete_ErrorIssueNotFound verifies error when issue doesn't exist
func TestHandleDelete_ErrorIssueNotFound(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Try to delete non-existent issue
	deleteArgs := DeleteArgs{
		IDs:   []string{"bd-nonexistent-12345"},
		Force: true,
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	deleteResp := server.handleDelete(deleteReq)

	// Parse response to check for errors
	var result map[string]interface{}
	if deleteResp.Success {
		if err := json.Unmarshal(deleteResp.Data, &result); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		// Check for partial success with errors
		if errors, ok := result["errors"].([]interface{}); ok && len(errors) > 0 {
			// This is expected - the response includes errors for not found issues
			found := false
			for _, e := range errors {
				if errStr, ok := e.(string); ok {
					if errStr == "bd-nonexistent-12345: not found" {
						found = true
						break
					}
				}
			}
			if !found {
				t.Errorf("expected 'not found' error, got: %v", errors)
			}
		}
	} else {
		// Complete failure is also acceptable
		if deleteResp.Error == "" {
			t.Error("expected error message")
		}
	}
}

// TestHandleDelete_ErrorCannotDeleteTemplate verifies that templates cannot be deleted
func TestHandleDelete_ErrorCannotDeleteTemplate(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create a template issue directly in memory store
	ctx := server.reqCtx(&Request{})
	template := &types.Issue{
		ID:          "bd-template-test",
		Title:       "Template Issue",
		Description: "This is a template",
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Priority:    2,
		IsTemplate:  true,
	}
	if err := store.CreateIssue(ctx, template, "test"); err != nil {
		t.Fatalf("failed to create template: %v", err)
	}

	// Try to delete the template
	deleteArgs := DeleteArgs{
		IDs:   []string{"bd-template-test"},
		Force: true,
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	deleteResp := server.handleDelete(deleteReq)

	// Parse response
	var result map[string]interface{}
	if deleteResp.Success {
		if err := json.Unmarshal(deleteResp.Data, &result); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		// Check for errors
		if errors, ok := result["errors"].([]interface{}); ok && len(errors) > 0 {
			found := false
			for _, e := range errors {
				if errStr, ok := e.(string); ok {
					if errStr == "bd-template-test: cannot delete template (templates are read-only)" {
						found = true
						break
					}
				}
			}
			if !found {
				t.Errorf("expected template deletion error, got: %v", errors)
			}
		} else {
			t.Error("expected errors in response for template deletion")
		}
	} else {
		// Complete failure with appropriate error is also acceptable
		if deleteResp.Error == "" {
			t.Error("expected error message")
		}
	}

	// Verify template still exists
	showArgs := ShowArgs{ID: "bd-template-test"}
	showJSON, _ := json.Marshal(showArgs)
	showReq := &Request{
		Operation: OpShow,
		Args:      showJSON,
	}
	showResp := server.handleShow(showReq)
	if !showResp.Success {
		t.Errorf("template should still exist after failed delete: %s", showResp.Error)
	}
}

// TestHandleDelete_InvalidArgs verifies error for malformed request
func TestHandleDelete_InvalidArgs(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Send invalid JSON
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      []byte("invalid json"),
		Actor:     "test-user",
	}

	deleteResp := server.handleDelete(deleteReq)
	if deleteResp.Success {
		t.Error("expected error for invalid args")
	}

	if deleteResp.Error == "" {
		t.Error("expected error message for invalid args")
	}
}

// TestHandleDelete_ReasonField verifies that the reason field is passed through
func TestHandleDelete_ReasonField(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create test issue
	createArgs := CreateArgs{
		Title:     "Issue with Reason",
		IssueType: "task",
		Priority:  2,
	}
	createJSON, _ := json.Marshal(createArgs)
	createReq := &Request{
		Operation: OpCreate,
		Args:      createJSON,
		Actor:     "test-user",
	}

	createResp := server.handleCreate(createReq)
	if !createResp.Success {
		t.Fatalf("failed to create test issue: %s", createResp.Error)
	}

	var createdIssue map[string]interface{}
	if err := json.Unmarshal(createResp.Data, &createdIssue); err != nil {
		t.Fatalf("failed to parse created issue: %v", err)
	}
	issueID := createdIssue["id"].(string)

	// Delete with reason
	deleteArgs := DeleteArgs{
		IDs:    []string{issueID},
		Force:  true,
		Reason: "no longer needed",
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	deleteResp := server.handleDelete(deleteReq)
	if !deleteResp.Success {
		t.Fatalf("delete with reason failed: %s", deleteResp.Error)
	}

	// Parse response
	var result map[string]interface{}
	if err := json.Unmarshal(deleteResp.Data, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if deletedCount, ok := result["deleted_count"].(float64); !ok || int(deletedCount) != 1 {
		t.Errorf("expected deleted_count=1, got %v", result["deleted_count"])
	}
}

// TestHandleDelete_CascadeAndForceFlags documents current behavior of cascade/force flags
// Note: At daemon level, these flags are accepted but cascade is not fully implemented
// The CLI handles cascade logic before calling the daemon
func TestHandleDelete_CascadeAndForceFlags(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create test issue
	createArgs := CreateArgs{
		Title:     "Issue with Flags",
		IssueType: "task",
		Priority:  2,
	}
	createJSON, _ := json.Marshal(createArgs)
	createReq := &Request{
		Operation: OpCreate,
		Args:      createJSON,
		Actor:     "test-user",
	}

	createResp := server.handleCreate(createReq)
	if !createResp.Success {
		t.Fatalf("failed to create test issue: %s", createResp.Error)
	}

	var createdIssue map[string]interface{}
	if err := json.Unmarshal(createResp.Data, &createdIssue); err != nil {
		t.Fatalf("failed to parse created issue: %v", err)
	}
	issueID := createdIssue["id"].(string)

	// Delete with cascade and force flags
	deleteArgs := DeleteArgs{
		IDs:     []string{issueID},
		Force:   true,
		Cascade: true,
	}
	deleteJSON, _ := json.Marshal(deleteArgs)
	deleteReq := &Request{
		Operation: OpDelete,
		Args:      deleteJSON,
		Actor:     "test-user",
	}

	deleteResp := server.handleDelete(deleteReq)
	if !deleteResp.Success {
		t.Fatalf("delete with flags failed: %s", deleteResp.Error)
	}

	// Verify successful deletion
	var result map[string]interface{}
	if err := json.Unmarshal(deleteResp.Data, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if deletedCount, ok := result["deleted_count"].(float64); !ok || int(deletedCount) != 1 {
		t.Errorf("expected deleted_count=1, got %v", result["deleted_count"])
	}
}

// TestHandleUpdate_ClaimFlag verifies atomic claim operation (gt-il2p7)
func TestHandleUpdate_ClaimFlag(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create an issue first
	createArgs := CreateArgs{
		Title:     "Test Issue for Claim",
		IssueType: "task",
		Priority:  2,
	}
	createJSON, _ := json.Marshal(createArgs)
	createReq := &Request{
		Operation: OpCreate,
		Args:      createJSON,
		Actor:     "test-user",
	}

	createResp := server.handleCreate(createReq)
	if !createResp.Success {
		t.Fatalf("failed to create test issue: %s", createResp.Error)
	}

	var createdIssue types.Issue
	if err := json.Unmarshal(createResp.Data, &createdIssue); err != nil {
		t.Fatalf("failed to parse created issue: %v", err)
	}
	issueID := createdIssue.ID

	// Verify issue starts with no assignee
	if createdIssue.Assignee != "" {
		t.Fatalf("expected no assignee initially, got %s", createdIssue.Assignee)
	}

	// Claim the issue
	updateArgs := UpdateArgs{
		ID:    issueID,
		Claim: true,
	}
	updateJSON, _ := json.Marshal(updateArgs)
	updateReq := &Request{
		Operation: OpUpdate,
		Args:      updateJSON,
		Actor:     "claiming-agent",
	}

	updateResp := server.handleUpdate(updateReq)
	if !updateResp.Success {
		t.Fatalf("claim operation failed: %s", updateResp.Error)
	}

	// Verify issue was claimed
	var updatedIssue types.Issue
	if err := json.Unmarshal(updateResp.Data, &updatedIssue); err != nil {
		t.Fatalf("failed to parse updated issue: %v", err)
	}

	if updatedIssue.Assignee != "claiming-agent" {
		t.Errorf("expected assignee 'claiming-agent', got %s", updatedIssue.Assignee)
	}
	if updatedIssue.Status != "in_progress" {
		t.Errorf("expected status 'in_progress', got %s", updatedIssue.Status)
	}
}

// TestHandleUpdate_ClaimFlag_AlreadyClaimed verifies double-claim returns error
func TestHandleUpdate_ClaimFlag_AlreadyClaimed(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create an issue first
	createArgs := CreateArgs{
		Title:     "Test Issue for Double Claim",
		IssueType: "task",
		Priority:  2,
	}
	createJSON, _ := json.Marshal(createArgs)
	createReq := &Request{
		Operation: OpCreate,
		Args:      createJSON,
		Actor:     "test-user",
	}

	createResp := server.handleCreate(createReq)
	if !createResp.Success {
		t.Fatalf("failed to create test issue: %s", createResp.Error)
	}

	var createdIssue types.Issue
	if err := json.Unmarshal(createResp.Data, &createdIssue); err != nil {
		t.Fatalf("failed to parse created issue: %v", err)
	}
	issueID := createdIssue.ID

	// First claim should succeed
	updateArgs := UpdateArgs{
		ID:    issueID,
		Claim: true,
	}
	updateJSON, _ := json.Marshal(updateArgs)
	updateReq := &Request{
		Operation: OpUpdate,
		Args:      updateJSON,
		Actor:     "first-claimer",
	}

	updateResp := server.handleUpdate(updateReq)
	if !updateResp.Success {
		t.Fatalf("first claim should succeed: %s", updateResp.Error)
	}

	// Second claim should fail
	updateArgs2 := UpdateArgs{
		ID:    issueID,
		Claim: true,
	}
	updateJSON2, _ := json.Marshal(updateArgs2)
	updateReq2 := &Request{
		Operation: OpUpdate,
		Args:      updateJSON2,
		Actor:     "second-claimer",
	}

	updateResp2 := server.handleUpdate(updateReq2)
	if updateResp2.Success {
		t.Error("expected second claim to fail, but it succeeded")
	}

	// Verify error message
	expectedError := "already claimed by first-claimer"
	if updateResp2.Error != expectedError {
		t.Errorf("expected error %q, got %q", expectedError, updateResp2.Error)
	}
}

// TestHandleUpdate_ClaimFlag_WithOtherUpdates verifies claim can combine with other updates
func TestHandleUpdate_ClaimFlag_WithOtherUpdates(t *testing.T) {
	store := memory.New("/tmp/test.jsonl")
	server := NewServer("/tmp/test.sock", store, "/tmp", "/tmp/test.db")

	// Create an issue first
	createArgs := CreateArgs{
		Title:     "Test Issue for Claim with Updates",
		IssueType: "task",
		Priority:  2,
	}
	createJSON, _ := json.Marshal(createArgs)
	createReq := &Request{
		Operation: OpCreate,
		Args:      createJSON,
		Actor:     "test-user",
	}

	createResp := server.handleCreate(createReq)
	if !createResp.Success {
		t.Fatalf("failed to create test issue: %s", createResp.Error)
	}

	var createdIssue types.Issue
	if err := json.Unmarshal(createResp.Data, &createdIssue); err != nil {
		t.Fatalf("failed to parse created issue: %v", err)
	}
	issueID := createdIssue.ID

	// Claim and update priority at the same time
	priority := 0 // High priority
	updateArgs := UpdateArgs{
		ID:       issueID,
		Claim:    true,
		Priority: &priority,
	}
	updateJSON, _ := json.Marshal(updateArgs)
	updateReq := &Request{
		Operation: OpUpdate,
		Args:      updateJSON,
		Actor:     "claiming-agent",
	}

	updateResp := server.handleUpdate(updateReq)
	if !updateResp.Success {
		t.Fatalf("claim with updates failed: %s", updateResp.Error)
	}

	// Verify all updates were applied
	ctx := context.Background()
	issue, err := store.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}

	if issue.Assignee != "claiming-agent" {
		t.Errorf("expected assignee 'claiming-agent', got %s", issue.Assignee)
	}
	if issue.Status != "in_progress" {
		t.Errorf("expected status 'in_progress', got %s", issue.Status)
	}
	if issue.Priority != 0 {
		t.Errorf("expected priority 0, got %d", issue.Priority)
	}
}
