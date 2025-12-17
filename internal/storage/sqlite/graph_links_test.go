package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestRelatesTo(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create two issues
	issue1 := &types.Issue{
		Title:     "Issue 1",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	issue2 := &types.Issue{
		Title:     "Issue 2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("Failed to create issue1: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("Failed to create issue2: %v", err)
	}

	// Add relates_to link (bidirectional)
	relatesTo1, _ := json.Marshal([]string{issue2.ID})
	if err := store.UpdateIssue(ctx, issue1.ID, map[string]interface{}{
		"relates_to": string(relatesTo1),
	}, "test"); err != nil {
		t.Fatalf("Failed to update issue1 relates_to: %v", err)
	}

	relatesTo2, _ := json.Marshal([]string{issue1.ID})
	if err := store.UpdateIssue(ctx, issue2.ID, map[string]interface{}{
		"relates_to": string(relatesTo2),
	}, "test"); err != nil {
		t.Fatalf("Failed to update issue2 relates_to: %v", err)
	}

	// Verify links
	updated1, err := store.GetIssue(ctx, issue1.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if len(updated1.RelatesTo) != 1 || updated1.RelatesTo[0] != issue2.ID {
		t.Errorf("issue1.RelatesTo = %v, want [%s]", updated1.RelatesTo, issue2.ID)
	}

	updated2, err := store.GetIssue(ctx, issue2.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if len(updated2.RelatesTo) != 1 || updated2.RelatesTo[0] != issue1.ID {
		t.Errorf("issue2.RelatesTo = %v, want [%s]", updated2.RelatesTo, issue1.ID)
	}
}

func TestRelatesTo_MultipleLinks(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create three issues
	issues := make([]*types.Issue, 3)
	for i := range issues {
		issues[i] = &types.Issue{
			Title:     "Issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := store.CreateIssue(ctx, issues[i], "test"); err != nil {
			t.Fatalf("Failed to create issue %d: %v", i, err)
		}
	}

	// Link issue0 to both issue1 and issue2
	relatesTo, _ := json.Marshal([]string{issues[1].ID, issues[2].ID})
	if err := store.UpdateIssue(ctx, issues[0].ID, map[string]interface{}{
		"relates_to": string(relatesTo),
	}, "test"); err != nil {
		t.Fatalf("Failed to update relates_to: %v", err)
	}

	// Verify
	updated, err := store.GetIssue(ctx, issues[0].ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if len(updated.RelatesTo) != 2 {
		t.Errorf("RelatesTo has %d links, want 2", len(updated.RelatesTo))
	}
}

func TestDuplicateOf(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create canonical and duplicate issues
	canonical := &types.Issue{
		Title:     "Canonical Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeBug,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	duplicate := &types.Issue{
		Title:     "Duplicate Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeBug,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, canonical, "test"); err != nil {
		t.Fatalf("Failed to create canonical: %v", err)
	}
	if err := store.CreateIssue(ctx, duplicate, "test"); err != nil {
		t.Fatalf("Failed to create duplicate: %v", err)
	}

	// Mark as duplicate and close
	if err := store.UpdateIssue(ctx, duplicate.ID, map[string]interface{}{
		"duplicate_of": canonical.ID,
		"status":       string(types.StatusClosed),
	}, "test"); err != nil {
		t.Fatalf("Failed to mark as duplicate: %v", err)
	}

	// Verify
	updated, err := store.GetIssue(ctx, duplicate.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if updated.DuplicateOf != canonical.ID {
		t.Errorf("DuplicateOf = %q, want %q", updated.DuplicateOf, canonical.ID)
	}
	if updated.Status != types.StatusClosed {
		t.Errorf("Status = %q, want %q", updated.Status, types.StatusClosed)
	}
}

func TestSupersededBy(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create old and new versions
	oldVersion := &types.Issue{
		Title:     "Design Doc v1",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	newVersion := &types.Issue{
		Title:     "Design Doc v2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, oldVersion, "test"); err != nil {
		t.Fatalf("Failed to create old version: %v", err)
	}
	if err := store.CreateIssue(ctx, newVersion, "test"); err != nil {
		t.Fatalf("Failed to create new version: %v", err)
	}

	// Mark old as superseded
	if err := store.UpdateIssue(ctx, oldVersion.ID, map[string]interface{}{
		"superseded_by": newVersion.ID,
		"status":        string(types.StatusClosed),
	}, "test"); err != nil {
		t.Fatalf("Failed to mark as superseded: %v", err)
	}

	// Verify
	updated, err := store.GetIssue(ctx, oldVersion.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if updated.SupersededBy != newVersion.ID {
		t.Errorf("SupersededBy = %q, want %q", updated.SupersededBy, newVersion.ID)
	}
	if updated.Status != types.StatusClosed {
		t.Errorf("Status = %q, want %q", updated.Status, types.StatusClosed)
	}
}

func TestRepliesTo(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create original message and reply
	original := &types.Issue{
		Title:       "Original Message",
		Description: "Original content",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Sender:      "alice",
		Assignee:    "bob",
		Ephemeral:   true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	reply := &types.Issue{
		Title:       "Re: Original Message",
		Description: "Reply content",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Sender:      "bob",
		Assignee:    "alice",
		Ephemeral:   true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := store.CreateIssue(ctx, original, "test"); err != nil {
		t.Fatalf("Failed to create original: %v", err)
	}

	// Set replies_to before creation
	reply.RepliesTo = original.ID
	if err := store.CreateIssue(ctx, reply, "test"); err != nil {
		t.Fatalf("Failed to create reply: %v", err)
	}

	// Verify thread link
	savedReply, err := store.GetIssue(ctx, reply.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if savedReply.RepliesTo != original.ID {
		t.Errorf("RepliesTo = %q, want %q", savedReply.RepliesTo, original.ID)
	}
}

func TestRepliesTo_Chain(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create a chain of replies
	messages := make([]*types.Issue, 3)
	var prevID string

	for i := range messages {
		messages[i] = &types.Issue{
			Title:     "Message",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeMessage,
			Sender:    "user",
			Assignee:  "inbox",
			Ephemeral: true,
			RepliesTo: prevID,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := store.CreateIssue(ctx, messages[i], "test"); err != nil {
			t.Fatalf("Failed to create message %d: %v", i, err)
		}
		prevID = messages[i].ID
	}

	// Verify chain
	for i := 1; i < len(messages); i++ {
		saved, err := store.GetIssue(ctx, messages[i].ID)
		if err != nil {
			t.Fatalf("GetIssue failed for message %d: %v", i, err)
		}
		if saved.RepliesTo != messages[i-1].ID {
			t.Errorf("Message %d: RepliesTo = %q, want %q", i, saved.RepliesTo, messages[i-1].ID)
		}
	}
}

func TestEphemeralField(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create ephemeral issue
	ephemeral := &types.Issue{
		Title:     "Ephemeral Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeMessage,
		Ephemeral: true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Create non-ephemeral issue
	permanent := &types.Issue{
		Title:     "Permanent Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: false,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, ephemeral, "test"); err != nil {
		t.Fatalf("Failed to create ephemeral: %v", err)
	}
	if err := store.CreateIssue(ctx, permanent, "test"); err != nil {
		t.Fatalf("Failed to create permanent: %v", err)
	}

	// Verify ephemeral flag
	savedEphemeral, err := store.GetIssue(ctx, ephemeral.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if !savedEphemeral.Ephemeral {
		t.Error("Ephemeral issue should have Ephemeral=true")
	}

	savedPermanent, err := store.GetIssue(ctx, permanent.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if savedPermanent.Ephemeral {
		t.Error("Permanent issue should have Ephemeral=false")
	}
}

func TestEphemeralFilter(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create mix of ephemeral and non-ephemeral issues
	for i := 0; i < 3; i++ {
		ephemeral := &types.Issue{
			Title:     "Ephemeral",
			Status:    types.StatusClosed, // Closed for cleanup test
			Priority:  2,
			IssueType: types.TypeMessage,
			Ephemeral: true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := store.CreateIssue(ctx, ephemeral, "test"); err != nil {
			t.Fatalf("Failed to create ephemeral %d: %v", i, err)
		}
	}

	for i := 0; i < 2; i++ {
		permanent := &types.Issue{
			Title:     "Permanent",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: false,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := store.CreateIssue(ctx, permanent, "test"); err != nil {
			t.Fatalf("Failed to create permanent %d: %v", i, err)
		}
	}

	// Filter for ephemeral only
	ephemeralTrue := true
	closedStatus := types.StatusClosed
	ephemeralFilter := types.IssueFilter{
		Status:    &closedStatus,
		Ephemeral: &ephemeralTrue,
	}

	ephemeralIssues, err := store.SearchIssues(ctx, "", ephemeralFilter)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(ephemeralIssues) != 3 {
		t.Errorf("Expected 3 ephemeral issues, got %d", len(ephemeralIssues))
	}

	// Filter for non-ephemeral only
	ephemeralFalse := false
	nonEphemeralFilter := types.IssueFilter{
		Status:    &closedStatus,
		Ephemeral: &ephemeralFalse,
	}

	permanentIssues, err := store.SearchIssues(ctx, "", nonEphemeralFilter)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(permanentIssues) != 2 {
		t.Errorf("Expected 2 non-ephemeral issues, got %d", len(permanentIssues))
	}
}

func TestSenderField(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create issue with sender
	msg := &types.Issue{
		Title:     "Message",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeMessage,
		Sender:    "alice@example.com",
		Assignee:  "bob@example.com",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, msg, "test"); err != nil {
		t.Fatalf("Failed to create message: %v", err)
	}

	// Verify sender is preserved
	saved, err := store.GetIssue(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if saved.Sender != "alice@example.com" {
		t.Errorf("Sender = %q, want %q", saved.Sender, "alice@example.com")
	}
}

func TestMessageType(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create a message type issue
	msg := &types.Issue{
		Title:     "Test Message",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeMessage,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, msg, "test"); err != nil {
		t.Fatalf("Failed to create message: %v", err)
	}

	// Verify type is preserved
	saved, err := store.GetIssue(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if saved.IssueType != types.TypeMessage {
		t.Errorf("IssueType = %q, want %q", saved.IssueType, types.TypeMessage)
	}

	// Filter by message type
	messageType := types.TypeMessage
	filter := types.IssueFilter{
		IssueType: &messageType,
	}

	messages, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(messages))
	}
}
