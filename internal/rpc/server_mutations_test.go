package rpc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/memory"
)

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
