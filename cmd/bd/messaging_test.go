//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestMessageLifecycle tests the full lifecycle of a message-type issue:
// create → read (get) → close (ack).
func TestMessageLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	now := time.Now()
	msg := &types.Issue{
		Title:       "Build failed on main",
		Description: "CI pipeline reports failure on commit abc123",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   "message",
		Sender:      "ci-bot",
		Assignee:    "dev-team",
		Ephemeral:   true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Create
	if err := testStore.CreateIssue(ctx, msg, "test"); err != nil {
		t.Fatalf("CreateIssue (message) failed: %v", err)
	}
	if msg.ID == "" {
		t.Fatal("Expected message to have an ID after creation")
	}

	// Read back
	got, err := testStore.GetIssue(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetIssue(%s) failed: %v", msg.ID, err)
	}
	if got.IssueType != "message" {
		t.Errorf("IssueType = %q, want \"message\"", got.IssueType)
	}
	if got.Sender != "ci-bot" {
		t.Errorf("Sender = %q, want \"ci-bot\"", got.Sender)
	}
	if got.Assignee != "dev-team" {
		t.Errorf("Assignee = %q, want \"dev-team\"", got.Assignee)
	}
	if !got.Ephemeral {
		t.Error("Expected Ephemeral=true")
	}
	if got.Status != types.StatusOpen {
		t.Errorf("Status = %q, want \"open\" (unread)", got.Status)
	}

	// Close (ack) the message
	if err := testStore.UpdateIssue(ctx, msg.ID, map[string]interface{}{
		"status": types.StatusClosed,
	}, "test"); err != nil {
		t.Fatalf("UpdateIssue (close/ack) failed: %v", err)
	}

	acked, err := testStore.GetIssue(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetIssue after ack failed: %v", err)
	}
	if acked.Status != types.StatusClosed {
		t.Errorf("Status after ack = %q, want \"closed\"", acked.Status)
	}
}

// TestMessageSearchByType verifies messages can be filtered by type.
func TestMessageSearchByType(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	now := time.Now()

	// Create a regular task
	task := &types.Issue{
		Title:     "Regular task",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := testStore.CreateIssue(ctx, task, "test"); err != nil {
		t.Fatalf("CreateIssue (task) failed: %v", err)
	}

	// Create a message
	msg := &types.Issue{
		Title:     "Agent notification",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: "message",
		Sender:    "coordinator",
		Assignee:  "worker-1",
		Ephemeral: true,
		CreatedAt: now.Add(time.Second),
		UpdatedAt: now.Add(time.Second),
	}
	if err := testStore.CreateIssue(ctx, msg, "test"); err != nil {
		t.Fatalf("CreateIssue (message) failed: %v", err)
	}

	// Search for messages only
	msgType := types.IssueType("message")
	filter := types.IssueFilter{
		IssueType: &msgType,
	}
	results, err := testStore.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssues(type=message) failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(results))
	}
	if results[0].ID != msg.ID {
		t.Errorf("Expected message ID %s, got %s", msg.ID, results[0].ID)
	}
}

// TestEphemeralMessageCleanup verifies ephemeral messages can be cleaned up
// while non-ephemeral issues are preserved.
func TestEphemeralMessageCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	now := time.Now()

	// Create a non-ephemeral task (should survive cleanup)
	task := &types.Issue{
		Title:     "Permanent task",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := testStore.CreateIssue(ctx, task, "test"); err != nil {
		t.Fatalf("CreateIssue (task) failed: %v", err)
	}

	// Create a closed ephemeral message (eligible for cleanup)
	msg := &types.Issue{
		Title:     "Old notification",
		Status:    types.StatusClosed,
		Priority:  3,
		IssueType: "message",
		Sender:    "bot",
		Ephemeral: true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := testStore.CreateIssue(ctx, msg, "test"); err != nil {
		t.Fatalf("CreateIssue (ephemeral message) failed: %v", err)
	}

	// Create an open ephemeral message (should not be cleaned up)
	openMsg := &types.Issue{
		Title:     "Unread notification",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: "message",
		Sender:    "bot",
		Ephemeral: true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := testStore.CreateIssue(ctx, openMsg, "test"); err != nil {
		t.Fatalf("CreateIssue (open ephemeral message) failed: %v", err)
	}

	// Search for closed ephemeral issues (what cleanup --ephemeral does)
	statusClosed := types.StatusClosed
	ephTrue := true
	filter := types.IssueFilter{
		Status:    &statusClosed,
		Ephemeral: &ephTrue,
	}
	closedEphemeral, err := testStore.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssues(closed+ephemeral) failed: %v", err)
	}

	if len(closedEphemeral) != 1 {
		t.Fatalf("Expected 1 closed ephemeral issue, got %d", len(closedEphemeral))
	}
	if closedEphemeral[0].ID != msg.ID {
		t.Errorf("Expected closed ephemeral %s, got %s", msg.ID, closedEphemeral[0].ID)
	}

	// Delete the closed ephemeral message
	result, err := testStore.DeleteIssues(ctx, []string{msg.ID}, false, true, false)
	if err != nil {
		t.Fatalf("DeleteIssues failed: %v", err)
	}
	if result.DeletedCount != 1 {
		t.Errorf("DeletedCount = %d, want 1", result.DeletedCount)
	}

	// Verify the permanent task still exists
	_, err = testStore.GetIssue(ctx, task.ID)
	if err != nil {
		t.Errorf("Permanent task should still exist after cleanup: %v", err)
	}

	// Verify the open ephemeral message still exists
	_, err = testStore.GetIssue(ctx, openMsg.ID)
	if err != nil {
		t.Errorf("Open ephemeral message should still exist after cleanup: %v", err)
	}
}

// TestSupersedesLink tests the supersedes dependency type for version chains.
func TestSupersedesLink(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	now := time.Now()

	// Create v1 design doc
	v1 := &types.Issue{
		Title:       "Design Doc v1",
		Description: "Initial design for feature X",
		Status:      types.StatusClosed,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := testStore.CreateIssue(ctx, v1, "test"); err != nil {
		t.Fatalf("CreateIssue (v1) failed: %v", err)
	}

	// Create v2 that supersedes v1
	v2 := &types.Issue{
		Title:       "Design Doc v2",
		Description: "Revised design for feature X",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   now.Add(time.Hour),
		UpdatedAt:   now.Add(time.Hour),
	}
	if err := testStore.CreateIssue(ctx, v2, "test"); err != nil {
		t.Fatalf("CreateIssue (v2) failed: %v", err)
	}

	// v2 supersedes v1
	dep := &types.Dependency{
		IssueID:     v2.ID,
		DependsOnID: v1.ID,
		Type:        types.DepSupersedes,
		CreatedAt:   now.Add(time.Hour),
	}
	if err := testStore.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency (supersedes) failed: %v", err)
	}

	// Verify the supersedes link via dependency records
	deps, err := testStore.GetDependencyRecords(ctx, v2.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords failed: %v", err)
	}

	found := false
	for _, d := range deps {
		if d.DependsOnID == v1.ID && d.Type == types.DepSupersedes {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected v2 to have supersedes link to v1")
	}

	// Supersedes should NOT block (it's informational)
	blocked, err := testStore.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues failed: %v", err)
	}
	for _, b := range blocked {
		if b.ID == v2.ID {
			t.Error("v2 should NOT be blocked by supersedes link to v1")
		}
	}
}

// TestDuplicatesDepLink tests the duplicates dependency type.
func TestDuplicatesDepLink(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	now := time.Now()

	// Create canonical issue
	canonical := &types.Issue{
		Title:       "Auth login fails with SSO",
		Description: "Users can't login via SSO",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := testStore.CreateIssue(ctx, canonical, "test"); err != nil {
		t.Fatalf("CreateIssue (canonical) failed: %v", err)
	}

	// Create duplicate
	dup := &types.Issue{
		Title:       "SSO login broken",
		Description: "Single sign-on authentication not working",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   now.Add(time.Minute),
		UpdatedAt:   now.Add(time.Minute),
	}
	if err := testStore.CreateIssue(ctx, dup, "test"); err != nil {
		t.Fatalf("CreateIssue (duplicate) failed: %v", err)
	}

	// Mark dup as duplicate of canonical
	dep := &types.Dependency{
		IssueID:     dup.ID,
		DependsOnID: canonical.ID,
		Type:        types.DepDuplicates,
		CreatedAt:   now.Add(time.Minute),
	}
	if err := testStore.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency (duplicates) failed: %v", err)
	}

	// Close the duplicate
	if err := testStore.UpdateIssue(ctx, dup.ID, map[string]interface{}{
		"status": types.StatusClosed,
	}, "test"); err != nil {
		t.Fatalf("UpdateIssue (close dup) failed: %v", err)
	}

	// Verify the link exists
	deps, err := testStore.GetDependencyRecords(ctx, dup.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords failed: %v", err)
	}

	found := false
	for _, d := range deps {
		if d.DependsOnID == canonical.ID && d.Type == types.DepDuplicates {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected duplicate to have duplicates link to canonical")
	}

	// Verify canonical is still open
	got, err := testStore.GetIssue(ctx, canonical.ID)
	if err != nil {
		t.Fatalf("GetIssue (canonical) failed: %v", err)
	}
	if got.Status != types.StatusOpen {
		t.Errorf("Canonical should still be open, got %s", got.Status)
	}
}

// TestMessageThreadWithMultipleReplies tests a message thread with branching replies.
func TestMessageThreadWithMultipleReplies(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	now := time.Now()

	// Original message
	original := &types.Issue{
		Title:       "Sprint planning discussion",
		Description: "Let's plan the next sprint",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   "message",
		Sender:      "lead",
		Assignee:    "team",
		Ephemeral:   true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := testStore.CreateIssue(ctx, original, "test"); err != nil {
		t.Fatalf("CreateIssue (original) failed: %v", err)
	}

	// Reply from worker-1
	reply1 := &types.Issue{
		Title:       "Re: Sprint planning discussion",
		Description: "I can take the auth refactor",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   "message",
		Sender:      "worker-1",
		Assignee:    "lead",
		Ephemeral:   true,
		CreatedAt:   now.Add(time.Minute),
		UpdatedAt:   now.Add(time.Minute),
	}
	if err := testStore.CreateIssue(ctx, reply1, "test"); err != nil {
		t.Fatalf("CreateIssue (reply1) failed: %v", err)
	}

	// Reply from worker-2 (also to original, not to reply1)
	reply2 := &types.Issue{
		Title:       "Re: Sprint planning discussion",
		Description: "I'll handle the database migration",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   "message",
		Sender:      "worker-2",
		Assignee:    "lead",
		Ephemeral:   true,
		CreatedAt:   now.Add(2 * time.Minute),
		UpdatedAt:   now.Add(2 * time.Minute),
	}
	if err := testStore.CreateIssue(ctx, reply2, "test"); err != nil {
		t.Fatalf("CreateIssue (reply2) failed: %v", err)
	}

	// Add replies-to deps
	for _, reply := range []*types.Issue{reply1, reply2} {
		dep := &types.Dependency{
			IssueID:     reply.ID,
			DependsOnID: original.ID,
			Type:        types.DepRepliesTo,
			CreatedAt:   reply.CreatedAt,
		}
		if err := testStore.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency (replies-to) failed: %v", err)
		}
	}

	// Verify original has 2 replies via dependents
	dependents, err := testStore.GetDependents(ctx, original.ID)
	if err != nil {
		t.Fatalf("GetDependents failed: %v", err)
	}

	replyCount := 0
	for _, dep := range dependents {
		if dep.ID == reply1.ID || dep.ID == reply2.ID {
			replyCount++
		}
	}
	if replyCount != 2 {
		t.Errorf("Expected 2 replies to original, got %d (total dependents: %d)", replyCount, len(dependents))
	}

	// Verify replies-to does NOT block
	blocked, err := testStore.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues failed: %v", err)
	}
	for _, b := range blocked {
		if b.ID == reply1.ID || b.ID == reply2.ID {
			t.Errorf("Reply %s should NOT be blocked by replies-to dependency", b.ID)
		}
	}
}

// TestFindMailDelegate tests the mail delegate resolution logic.
func TestFindMailDelegate(t *testing.T) {
	// Save and restore environment
	origBeads := os.Getenv("BEADS_MAIL_DELEGATE")
	origBD := os.Getenv("BD_MAIL_DELEGATE")
	defer func() {
		os.Setenv("BEADS_MAIL_DELEGATE", origBeads)
		os.Setenv("BD_MAIL_DELEGATE", origBD)
	}()

	t.Run("BEADS_MAIL_DELEGATE takes priority", func(t *testing.T) {
		os.Setenv("BEADS_MAIL_DELEGATE", "gt mail")
		os.Setenv("BD_MAIL_DELEGATE", "other mail")
		defer func() {
			os.Unsetenv("BEADS_MAIL_DELEGATE")
			os.Unsetenv("BD_MAIL_DELEGATE")
		}()

		got := findMailDelegate()
		if got != "gt mail" {
			t.Errorf("findMailDelegate() = %q, want \"gt mail\"", got)
		}
	})

	t.Run("BD_MAIL_DELEGATE fallback", func(t *testing.T) {
		os.Unsetenv("BEADS_MAIL_DELEGATE")
		os.Setenv("BD_MAIL_DELEGATE", "custom mail")
		defer os.Unsetenv("BD_MAIL_DELEGATE")

		got := findMailDelegate()
		if got != "custom mail" {
			t.Errorf("findMailDelegate() = %q, want \"custom mail\"", got)
		}
	})

	t.Run("no delegate returns empty", func(t *testing.T) {
		os.Unsetenv("BEADS_MAIL_DELEGATE")
		os.Unsetenv("BD_MAIL_DELEGATE")

		// Temporarily clear the global store so config lookup is skipped
		oldStore := store
		store = nil
		defer func() { store = oldStore }()

		got := findMailDelegate()
		if got != "" {
			t.Errorf("findMailDelegate() = %q, want empty string", got)
		}
	})
}

// TestMailDelegateFromConfig tests mail delegate resolution from store config.
func TestMailDelegateFromConfig(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	// Set mail.delegate in config
	if err := testStore.SetConfig(ctx, "mail.delegate", "gt mail"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// Clear env vars
	origBeads := os.Getenv("BEADS_MAIL_DELEGATE")
	origBD := os.Getenv("BD_MAIL_DELEGATE")
	os.Unsetenv("BEADS_MAIL_DELEGATE")
	os.Unsetenv("BD_MAIL_DELEGATE")
	defer func() {
		os.Setenv("BEADS_MAIL_DELEGATE", origBeads)
		os.Setenv("BD_MAIL_DELEGATE", origBD)
	}()

	// Set global store so findMailDelegate can query config
	oldStore := store
	oldCtx := rootCtx
	store = testStore
	rootCtx = ctx
	defer func() {
		store = oldStore
		rootCtx = oldCtx
	}()

	got := findMailDelegate()
	if got != "gt mail" {
		t.Errorf("findMailDelegate() = %q, want \"gt mail\"", got)
	}
}

// TestMessageSenderPreservation verifies that the sender field is preserved
// through create and update operations.
func TestMessageSenderPreservation(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	now := time.Now()
	msg := &types.Issue{
		Title:     "Status update",
		Status:    types.StatusOpen,
		Priority:  3,
		IssueType: "message",
		Sender:    "agent-alpha",
		Assignee:  "agent-beta",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := testStore.CreateIssue(ctx, msg, "test"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Update the description (should not lose sender)
	if err := testStore.UpdateIssue(ctx, msg.ID, map[string]interface{}{
		"description": "Updated status details",
	}, "test"); err != nil {
		t.Fatalf("UpdateIssue failed: %v", err)
	}

	got, err := testStore.GetIssue(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if got.Sender != "agent-alpha" {
		t.Errorf("Sender = %q after update, want \"agent-alpha\"", got.Sender)
	}
	if got.Description != "Updated status details" {
		t.Errorf("Description not updated, got %q", got.Description)
	}
}
