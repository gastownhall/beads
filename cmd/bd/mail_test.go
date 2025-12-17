package main

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestMailSendAndInbox(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, tmpDir+"/.beads/beads.db")
	ctx := context.Background()

	// Set up global state
	oldStore := store
	oldRootCtx := rootCtx
	oldActor := actor
	store = testStore
	rootCtx = ctx
	actor = "test-user"
	defer func() {
		store = oldStore
		rootCtx = oldRootCtx
		actor = oldActor
	}()

	// Create a message (simulating mail send)
	now := time.Now()
	msg := &types.Issue{
		Title:       "Test Subject",
		Description: "Test message body",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Assignee:    "worker-1",
		Sender:      "manager",
		Ephemeral:   true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := testStore.CreateIssue(ctx, msg, actor); err != nil {
		t.Fatalf("Failed to create message: %v", err)
	}

	// Query inbox for worker-1
	messageType := types.TypeMessage
	openStatus := types.StatusOpen
	assignee := "worker-1"
	filter := types.IssueFilter{
		IssueType: &messageType,
		Status:    &openStatus,
		Assignee:  &assignee,
	}

	messages, err := testStore.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}

	if messages[0].Title != "Test Subject" {
		t.Errorf("Title = %q, want %q", messages[0].Title, "Test Subject")
	}
	if messages[0].Sender != "manager" {
		t.Errorf("Sender = %q, want %q", messages[0].Sender, "manager")
	}
	if !messages[0].Ephemeral {
		t.Error("Ephemeral should be true")
	}
}

func TestMailInboxEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, tmpDir+"/.beads/beads.db")
	ctx := context.Background()

	// Query inbox for non-existent user
	messageType := types.TypeMessage
	openStatus := types.StatusOpen
	assignee := "nobody"
	filter := types.IssueFilter{
		IssueType: &messageType,
		Status:    &openStatus,
		Assignee:  &assignee,
	}

	messages, err := testStore.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	if len(messages) != 0 {
		t.Errorf("Expected 0 messages, got %d", len(messages))
	}
}

func TestMailAck(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, tmpDir+"/.beads/beads.db")
	ctx := context.Background()

	// Create a message
	now := time.Now()
	msg := &types.Issue{
		Title:       "Ack Test",
		Description: "Test body",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Assignee:    "recipient",
		Sender:      "sender",
		Ephemeral:   true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := testStore.CreateIssue(ctx, msg, "test"); err != nil {
		t.Fatalf("Failed to create message: %v", err)
	}

	// Acknowledge (close) the message
	if err := testStore.CloseIssue(ctx, msg.ID, "acknowledged", "test"); err != nil {
		t.Fatalf("Failed to close message: %v", err)
	}

	// Verify it's closed
	updated, err := testStore.GetIssue(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if updated.Status != types.StatusClosed {
		t.Errorf("Status = %q, want %q", updated.Status, types.StatusClosed)
	}

	// Verify it no longer appears in inbox
	messageType := types.TypeMessage
	openStatus := types.StatusOpen
	assignee := "recipient"
	filter := types.IssueFilter{
		IssueType: &messageType,
		Status:    &openStatus,
		Assignee:  &assignee,
	}

	messages, err := testStore.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	if len(messages) != 0 {
		t.Errorf("Expected 0 messages in inbox after ack, got %d", len(messages))
	}
}

func TestMailReply(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, tmpDir+"/.beads/beads.db")
	ctx := context.Background()

	// Create original message
	now := time.Now()
	original := &types.Issue{
		Title:       "Original Subject",
		Description: "Original body",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Assignee:    "worker",
		Sender:      "manager",
		Ephemeral:   true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := testStore.CreateIssue(ctx, original, "test"); err != nil {
		t.Fatalf("Failed to create original message: %v", err)
	}

	// Create reply
	reply := &types.Issue{
		Title:       "Re: Original Subject",
		Description: "Reply body",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Assignee:    "manager", // Reply goes to original sender
		Sender:      "worker",
		Ephemeral:   true,
		RepliesTo:   original.ID, // Thread link
		CreatedAt:   now.Add(time.Minute),
		UpdatedAt:   now.Add(time.Minute),
	}

	if err := testStore.CreateIssue(ctx, reply, "test"); err != nil {
		t.Fatalf("Failed to create reply: %v", err)
	}

	// Verify reply has correct thread link
	savedReply, err := testStore.GetIssue(ctx, reply.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if savedReply.RepliesTo != original.ID {
		t.Errorf("RepliesTo = %q, want %q", savedReply.RepliesTo, original.ID)
	}
}

func TestMailPriority(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, tmpDir+"/.beads/beads.db")
	ctx := context.Background()

	// Create messages with different priorities
	now := time.Now()
	messages := []struct {
		title    string
		priority int
	}{
		{"Normal message", 2},
		{"Urgent message", 0},
		{"High priority", 1},
	}

	for i, m := range messages {
		msg := &types.Issue{
			Title:       m.title,
			Description: "Body",
			Status:      types.StatusOpen,
			Priority:    m.priority,
			IssueType:   types.TypeMessage,
			Assignee:    "inbox",
			Sender:      "sender",
			Ephemeral:   true,
			CreatedAt:   now.Add(time.Duration(i) * time.Minute),
			UpdatedAt:   now.Add(time.Duration(i) * time.Minute),
		}
		if err := testStore.CreateIssue(ctx, msg, "test"); err != nil {
			t.Fatalf("Failed to create message %d: %v", i, err)
		}
	}

	// Query all messages
	messageType := types.TypeMessage
	openStatus := types.StatusOpen
	assignee := "inbox"
	filter := types.IssueFilter{
		IssueType: &messageType,
		Status:    &openStatus,
		Assignee:  &assignee,
	}

	results, err := testStore.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("Expected 3 messages, got %d", len(results))
	}

	// Verify we can filter by priority
	urgentPriority := 0
	urgentFilter := types.IssueFilter{
		IssueType: &messageType,
		Status:    &openStatus,
		Assignee:  &assignee,
		Priority:  &urgentPriority,
	}

	urgent, err := testStore.SearchIssues(ctx, "", urgentFilter)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	if len(urgent) != 1 {
		t.Errorf("Expected 1 urgent message, got %d", len(urgent))
	}
}

func TestMailTypeValidation(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, tmpDir+"/.beads/beads.db")
	ctx := context.Background()

	// Create a regular issue (not a message)
	now := time.Now()
	task := &types.Issue{
		Title:     "Regular Task",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := testStore.CreateIssue(ctx, task, "test"); err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Query for messages should not return the task
	messageType := types.TypeMessage
	filter := types.IssueFilter{
		IssueType: &messageType,
	}

	messages, err := testStore.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	for _, m := range messages {
		if m.ID == task.ID {
			t.Errorf("Task %s should not appear in message query", task.ID)
		}
	}
}

func TestMailSenderField(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, tmpDir+"/.beads/beads.db")
	ctx := context.Background()

	// Create messages from different senders
	now := time.Now()
	senders := []string{"alice", "bob", "charlie"}

	for i, sender := range senders {
		msg := &types.Issue{
			Title:       "Message from " + sender,
			Description: "Body",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeMessage,
			Assignee:    "inbox",
			Sender:      sender,
			Ephemeral:   true,
			CreatedAt:   now.Add(time.Duration(i) * time.Minute),
			UpdatedAt:   now.Add(time.Duration(i) * time.Minute),
		}
		if err := testStore.CreateIssue(ctx, msg, "test"); err != nil {
			t.Fatalf("Failed to create message from %s: %v", sender, err)
		}
	}

	// Query all messages and verify sender
	messageType := types.TypeMessage
	filter := types.IssueFilter{
		IssueType: &messageType,
	}

	messages, err := testStore.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	senderSet := make(map[string]bool)
	for _, m := range messages {
		if m.Sender != "" {
			senderSet[m.Sender] = true
		}
	}

	for _, s := range senders {
		if !senderSet[s] {
			t.Errorf("Sender %q not found in messages", s)
		}
	}
}
