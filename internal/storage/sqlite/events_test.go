package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

const testUserAlice = "alice"

func TestAddComment(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add a comment
	err = store.AddComment(ctx, issue.ID, testUserAlice, "This is a test comment")
	if err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}

	// Get events to verify comment was added
	events, err := store.GetEvents(ctx, issue.ID, 0)
	if err != nil {
		t.Fatalf("GetEvents failed: %v", err)
	}

	// Should have 2 events: created and commented
	if len(events) < 2 {
		t.Fatalf("Expected at least 2 events, got %d", len(events))
	}

	// Find the comment event (most recent should be first due to DESC order)
	var commentEvent *types.Event
	for _, event := range events {
		if event.EventType == types.EventCommented {
			commentEvent = event
			break
		}
	}

	if commentEvent == nil {
		t.Fatal("Comment event not found")
	}

	if commentEvent.Actor != testUserAlice {
		t.Errorf("Expected actor 'alice', got '%s'", commentEvent.Actor)
	}

	if commentEvent.Comment == nil || *commentEvent.Comment != "This is a test comment" {
		t.Errorf("Expected comment 'This is a test comment', got %v", commentEvent.Comment)
	}
}

func TestAddMultipleComments(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add multiple comments
	comments := []struct {
		actor   string
		comment string
	}{
		{"alice", "First comment"},
		{"bob", "Second comment"},
		{"charlie", "Third comment"},
	}

	for _, c := range comments {
		err = store.AddComment(ctx, issue.ID, c.actor, c.comment)
		if err != nil {
			t.Fatalf("AddComment failed: %v", err)
		}
	}

	// Get events
	events, err := store.GetEvents(ctx, issue.ID, 0)
	if err != nil {
		t.Fatalf("GetEvents failed: %v", err)
	}

	// Count comment events
	commentCount := 0
	var commentEvents []*types.Event
	for _, event := range events {
		if event.EventType == types.EventCommented {
			commentCount++
			commentEvents = append(commentEvents, event)
		}
	}

	if commentCount != 3 {
		t.Fatalf("Expected 3 comment events, got %d", commentCount)
	}

	// Verify we can find all three comments
	foundComments := make(map[string]bool)
	for _, event := range commentEvents {
		if event.Comment != nil {
			foundComments[*event.Comment] = true
		}
	}

	expectedComments := []string{"First comment", "Second comment", "Third comment"}
	for _, expected := range expectedComments {
		if !foundComments[expected] {
			t.Errorf("Expected to find comment '%s'", expected)
		}
	}
}

func TestGetEventsWithLimit(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Add 5 comments
	for i := 0; i < 5; i++ {
		err = store.AddComment(ctx, issue.ID, "alice", "Comment")
		if err != nil {
			t.Fatalf("AddComment failed: %v", err)
		}
	}

	// Get events with limit
	events, err := store.GetEvents(ctx, issue.ID, 3)
	if err != nil {
		t.Fatalf("GetEvents failed: %v", err)
	}

	if len(events) != 3 {
		t.Errorf("Expected 3 events with limit, got %d", len(events))
	}
}

func TestGetEventsEmpty(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Get events for non-existent issue
	events, err := store.GetEvents(ctx, "bd-999", 0)
	if err != nil {
		t.Fatalf("GetEvents failed: %v", err)
	}

	if len(events) != 0 {
		t.Errorf("Expected 0 events for non-existent issue, got %d", len(events))
	}
}

func TestAddCommentMarksDirty(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Clear dirty issues
	err = store.ClearDirtyIssuesByID(ctx, []string{issue.ID})
	if err != nil {
		t.Fatalf("ClearDirtyIssuesByID failed: %v", err)
	}

	// Add comment - should mark issue dirty
	err = store.AddComment(ctx, issue.ID, "alice", "Test comment")
	if err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}

	// Verify issue is dirty
	dirtyIssues, err := store.GetDirtyIssues(ctx)
	if err != nil {
		t.Fatalf("GetDirtyIssues failed: %v", err)
	}

	if len(dirtyIssues) != 1 || dirtyIssues[0] != issue.ID {
		t.Error("Expected issue to be marked dirty after adding comment")
	}
}

func TestAddCommentUpdatesTimestamp(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	originalUpdatedAt := issue.UpdatedAt

	// Sleep briefly to ensure timestamp difference on systems with low time resolution (e.g., Windows)
	// This prevents flaky test failures when both operations complete in the same millisecond
	time.Sleep(2 * time.Millisecond)

	// Add comment
	err = store.AddComment(ctx, issue.ID, "alice", "Test comment")
	if err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}

	// Get issue again and verify updated_at changed
	updatedIssue, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if !updatedIssue.UpdatedAt.After(originalUpdatedAt) {
		t.Error("Expected updated_at to be updated after adding comment")
	}
}

func TestEventTypesInHistory(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Perform various operations that create events
	err = store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"priority": 2,
	}, "test-user")
	if err != nil {
		t.Fatalf("UpdateIssue failed: %v", err)
	}

	err = store.AddComment(ctx, issue.ID, "alice", "A comment")
	if err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}

	err = store.AddLabel(ctx, issue.ID, "bug", "test-user")
	if err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}

	err = store.CloseIssue(ctx, issue.ID, "Done", "test-user", "")
	if err != nil {
		t.Fatalf("CloseIssue failed: %v", err)
	}

	// Get events
	events, err := store.GetEvents(ctx, issue.ID, 0)
	if err != nil {
		t.Fatalf("GetEvents failed: %v", err)
	}

	// Should have multiple event types
	eventTypes := make(map[types.EventType]bool)
	for _, event := range events {
		eventTypes[event.EventType] = true
	}

	// Verify we have different event types
	if !eventTypes[types.EventCreated] {
		t.Error("Expected EventCreated in history")
	}
	if !eventTypes[types.EventUpdated] {
		t.Error("Expected EventUpdated in history")
	}
	if !eventTypes[types.EventCommented] {
		t.Error("Expected EventCommented in history")
	}
	if !eventTypes[types.EventLabelAdded] {
		t.Error("Expected EventLabelAdded in history")
	}
	if !eventTypes[types.EventClosed] {
		t.Error("Expected EventClosed in history")
	}
}

func TestGetAllEventsSinceReturnsAllColumns(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue (prerequisite for events due to FK constraint)
	issue := &types.Issue{
		Title:     "Test issue for events",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Insert an event directly with ALL fields populated.
	// No single code path populates old_value, new_value, AND comment together,
	// so we use direct SQL to ensure GetAllEventsSince reads every column.
	oldVal := "old-status"
	newVal := "new-status"
	commentText := "status change reason"
	createdAt := time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC)

	_, err = store.db.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, issue.ID, types.EventStatusChanged, "alice", oldVal, newVal, commentText, createdAt)
	if err != nil {
		t.Fatalf("Direct INSERT INTO events failed: %v", err)
	}

	// Call GetAllEventsSince with sinceID=0 to get all events
	events, err := store.GetAllEventsSince(ctx, 0)
	if err != nil {
		t.Fatalf("GetAllEventsSince failed: %v", err)
	}

	// Find the event we inserted (it should be the status_changed event)
	var found *types.Event
	for _, e := range events {
		if e.EventType == types.EventStatusChanged {
			found = e
			break
		}
	}

	if found == nil {
		t.Fatal("Expected to find the status_changed event in GetAllEventsSince results")
	}

	// Verify ALL fields are correctly populated
	if found.ID == 0 {
		t.Error("Expected non-zero ID")
	}
	if found.IssueID != issue.ID {
		t.Errorf("Expected IssueID %q, got %q", issue.ID, found.IssueID)
	}
	if found.EventType != types.EventStatusChanged {
		t.Errorf("Expected EventType %q, got %q", types.EventStatusChanged, found.EventType)
	}
	if found.Actor != "alice" {
		t.Errorf("Expected Actor %q, got %q", "alice", found.Actor)
	}
	if found.OldValue == nil {
		t.Fatal("Expected OldValue to be non-nil")
	}
	if *found.OldValue != oldVal {
		t.Errorf("Expected OldValue %q, got %q", oldVal, *found.OldValue)
	}
	if found.NewValue == nil {
		t.Fatal("Expected NewValue to be non-nil")
	}
	if *found.NewValue != newVal {
		t.Errorf("Expected NewValue %q, got %q", newVal, *found.NewValue)
	}
	if found.Comment == nil {
		t.Fatal("Expected Comment to be non-nil")
	}
	if *found.Comment != commentText {
		t.Errorf("Expected Comment %q, got %q", commentText, *found.Comment)
	}
	if found.CreatedAt.IsZero() {
		t.Error("Expected non-zero CreatedAt")
	}
	// Compare time with second precision (SQLite may not store sub-second precision)
	if found.CreatedAt.Unix() != createdAt.Unix() {
		t.Errorf("Expected CreatedAt %v, got %v", createdAt, found.CreatedAt)
	}
}

func TestAddCommentNotFound(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	nonExistentID := "bd-999"

	err := store.AddComment(ctx, nonExistentID, "alice", "This should fail cleanly")
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	expectedError := "issue bd-999 not found"
	if !strings.Contains(err.Error(), expectedError) {
		t.Errorf("Expected error to contain %q, got %q", expectedError, err.Error())
	}
}
