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

// TestMessagingSuite consolidates message lifecycle, dependency types, and thread tests.
// All subtests share one DB since they create unique IDs and don't conflict.
func TestMessagingSuite(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()
	now := time.Now()

	// === Lifecycle data ===
	lifecycleMsg := &types.Issue{
		ID: "msg-lifecycle", Title: "Build failed on main",
		Description: "CI pipeline reports failure on commit abc123",
		Status:      types.StatusOpen, Priority: 1, IssueType: "message",
		Sender: "ci-bot", Assignee: "dev-team", Ephemeral: true,
		CreatedAt: now, UpdatedAt: now,
	}

	// === SearchByType data ===
	searchTask := &types.Issue{
		ID: "msg-search-task", Title: "Regular task (search)",
		Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		CreatedAt: now, UpdatedAt: now,
	}
	searchMsg := &types.Issue{
		ID: "msg-search-msg", Title: "Agent notification (search)",
		Status: types.StatusOpen, Priority: 2, IssueType: "message",
		Sender: "coordinator", Assignee: "worker-1", Ephemeral: true,
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}

	// === Supersedes data ===
	v1 := &types.Issue{
		ID: "msg-v1", Title: "Design Doc v1", Description: "Initial design for feature X",
		Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask,
		CreatedAt: now, UpdatedAt: now,
	}
	v2 := &types.Issue{
		ID: "msg-v2", Title: "Design Doc v2", Description: "Revised design for feature X",
		Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		CreatedAt: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour),
	}

	// === Duplicates data ===
	canonical := &types.Issue{
		ID: "msg-canonical", Title: "Auth login fails with SSO",
		Description: "Users can't login via SSO",
		Status:      types.StatusOpen, Priority: 1, IssueType: types.TypeBug,
		CreatedAt: now, UpdatedAt: now,
	}
	dup := &types.Issue{
		ID: "msg-dup", Title: "SSO login broken",
		Description: "Single sign-on authentication not working",
		Status:      types.StatusOpen, Priority: 1, IssueType: types.TypeBug,
		CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute),
	}

	// === Thread data ===
	threadOrig := &types.Issue{
		ID: "msg-thread-orig", Title: "Sprint planning discussion",
		Description: "Let's plan the next sprint",
		Status:      types.StatusOpen, Priority: 2, IssueType: "message",
		Sender: "lead", Assignee: "team", Ephemeral: true,
		CreatedAt: now, UpdatedAt: now,
	}
	threadReply1 := &types.Issue{
		ID: "msg-thread-r1", Title: "Re: Sprint planning",
		Description: "I can take the auth refactor",
		Status:      types.StatusOpen, Priority: 2, IssueType: "message",
		Sender: "worker-1", Assignee: "lead", Ephemeral: true,
		CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute),
	}
	threadReply2 := &types.Issue{
		ID: "msg-thread-r2", Title: "Re: Sprint planning",
		Description: "I'll handle the database migration",
		Status:      types.StatusOpen, Priority: 2, IssueType: "message",
		Sender: "worker-2", Assignee: "lead", Ephemeral: true,
		CreatedAt: now.Add(2 * time.Minute), UpdatedAt: now.Add(2 * time.Minute),
	}

	// === Ephemeral cleanup data ===
	permTask := &types.Issue{
		ID: "msg-perm-task", Title: "Permanent task (eph)",
		Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask,
		CreatedAt: now, UpdatedAt: now,
	}
	closedEph := &types.Issue{
		ID: "msg-closed-eph", Title: "Old notification (eph)",
		Status: types.StatusClosed, Priority: 3, IssueType: "message",
		Sender: "bot", Ephemeral: true, CreatedAt: now, UpdatedAt: now,
	}
	openEph := &types.Issue{
		ID: "msg-open-eph", Title: "Unread notification (eph)",
		Status: types.StatusOpen, Priority: 2, IssueType: "message",
		Sender: "bot", Ephemeral: true, CreatedAt: now, UpdatedAt: now,
	}

	// === Sender preservation data ===
	senderMsg := &types.Issue{
		ID: "msg-sender", Title: "Status update",
		Status: types.StatusOpen, Priority: 3, IssueType: "message",
		Sender: "agent-alpha", Assignee: "agent-beta",
		CreatedAt: now, UpdatedAt: now,
	}

	// Bulk create all issues
	allIssues := []*types.Issue{
		lifecycleMsg, searchTask, searchMsg, v1, v2, canonical, dup,
		threadOrig, threadReply1, threadReply2,
		permTask, closedEph, openEph, senderMsg,
	}
	for _, issue := range allIssues {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}

	// Add supersedes dep
	if err := testStore.AddDependency(ctx, &types.Dependency{
		IssueID: v2.ID, DependsOnID: v1.ID, Type: types.DepSupersedes, CreatedAt: now.Add(time.Hour),
	}, "test"); err != nil {
		t.Fatalf("AddDep(supersedes): %v", err)
	}

	// Add duplicates dep
	if err := testStore.AddDependency(ctx, &types.Dependency{
		IssueID: dup.ID, DependsOnID: canonical.ID, Type: types.DepDuplicates, CreatedAt: now.Add(time.Minute),
	}, "test"); err != nil {
		t.Fatalf("AddDep(duplicates): %v", err)
	}
	// Close the duplicate
	if err := testStore.UpdateIssue(ctx, dup.ID, map[string]interface{}{"status": types.StatusClosed}, "test"); err != nil {
		t.Fatalf("Close dup: %v", err)
	}

	// Add thread reply deps
	for _, reply := range []*types.Issue{threadReply1, threadReply2} {
		if err := testStore.AddDependency(ctx, &types.Dependency{
			IssueID: reply.ID, DependsOnID: threadOrig.ID, Type: types.DepRepliesTo, CreatedAt: reply.CreatedAt,
		}, "test"); err != nil {
			t.Fatalf("AddDep(replies-to): %v", err)
		}
	}

	// === Tests ===

	t.Run("MessageLifecycle", func(t *testing.T) {
		got, err := testStore.GetIssue(ctx, lifecycleMsg.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got.IssueType != "message" {
			t.Errorf("IssueType = %q, want message", got.IssueType)
		}
		if got.Sender != "ci-bot" {
			t.Errorf("Sender = %q, want ci-bot", got.Sender)
		}
		if !got.Ephemeral {
			t.Error("Expected Ephemeral=true")
		}

		// Close (ack)
		if err := testStore.UpdateIssue(ctx, lifecycleMsg.ID, map[string]interface{}{
			"status": types.StatusClosed,
		}, "test"); err != nil {
			t.Fatalf("UpdateIssue (close): %v", err)
		}
		acked, err := testStore.GetIssue(ctx, lifecycleMsg.ID)
		if err != nil {
			t.Fatalf("GetIssue after ack: %v", err)
		}
		if acked.Status != types.StatusClosed {
			t.Errorf("Status after ack = %q, want closed", acked.Status)
		}
	})

	t.Run("SearchByType", func(t *testing.T) {
		msgType := types.IssueType("message")
		results, err := testStore.SearchIssues(ctx, "", types.IssueFilter{IssueType: &msgType})
		if err != nil {
			t.Fatalf("SearchIssues: %v", err)
		}
		// Verify our search message is in results
		found := false
		for _, r := range results {
			if r.ID == searchMsg.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected search message in type=message results")
		}
		// Verify task is NOT in results
		for _, r := range results {
			if r.ID == searchTask.ID {
				t.Error("Task should not appear in type=message results")
			}
		}
	})

	t.Run("SupersedesLink", func(t *testing.T) {
		deps, err := testStore.GetDependencyRecords(ctx, v2.ID)
		if err != nil {
			t.Fatalf("GetDependencyRecords: %v", err)
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

		// Supersedes should NOT block
		blocked, err := testStore.GetBlockedIssues(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetBlockedIssues: %v", err)
		}
		for _, b := range blocked {
			if b.ID == v2.ID {
				t.Error("v2 should NOT be blocked by supersedes link")
			}
		}
	})

	t.Run("DuplicatesLink", func(t *testing.T) {
		deps, err := testStore.GetDependencyRecords(ctx, dup.ID)
		if err != nil {
			t.Fatalf("GetDependencyRecords: %v", err)
		}
		found := false
		for _, d := range deps {
			if d.DependsOnID == canonical.ID && d.Type == types.DepDuplicates {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected dup to have duplicates link to canonical")
		}

		got, err := testStore.GetIssue(ctx, canonical.ID)
		if err != nil {
			t.Fatalf("GetIssue canonical: %v", err)
		}
		if got.Status != types.StatusOpen {
			t.Errorf("Canonical should still be open, got %s", got.Status)
		}
	})

	t.Run("ThreadMultipleReplies", func(t *testing.T) {
		dependents, err := testStore.GetDependents(ctx, threadOrig.ID)
		if err != nil {
			t.Fatalf("GetDependents: %v", err)
		}
		replyCount := 0
		for _, dep := range dependents {
			if dep.ID == threadReply1.ID || dep.ID == threadReply2.ID {
				replyCount++
			}
		}
		if replyCount != 2 {
			t.Errorf("Expected 2 replies, got %d", replyCount)
		}

		// replies-to should NOT block
		blocked, err := testStore.GetBlockedIssues(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetBlockedIssues: %v", err)
		}
		for _, b := range blocked {
			if b.ID == threadReply1.ID || b.ID == threadReply2.ID {
				t.Errorf("Reply %s should NOT be blocked by replies-to", b.ID)
			}
		}
	})

	t.Run("EphemeralCleanup", func(t *testing.T) {
		// Search for closed ephemeral issues
		statusClosed := types.StatusClosed
		ephTrue := true
		filter := types.IssueFilter{Status: &statusClosed, Ephemeral: &ephTrue}
		closedEphResults, err := testStore.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("SearchIssues: %v", err)
		}

		// Our closedEph should be in results
		found := false
		for _, r := range closedEphResults {
			if r.ID == closedEph.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected closed ephemeral message in results")
		}

		// Delete it
		result, err := testStore.DeleteIssues(ctx, []string{closedEph.ID}, false, true, false)
		if err != nil {
			t.Fatalf("DeleteIssues: %v", err)
		}
		if result.DeletedCount != 1 {
			t.Errorf("DeletedCount = %d, want 1", result.DeletedCount)
		}

		// Verify permanent task still exists
		if _, err := testStore.GetIssue(ctx, permTask.ID); err != nil {
			t.Errorf("Permanent task should survive cleanup: %v", err)
		}
		// Verify open ephemeral still exists
		if _, err := testStore.GetIssue(ctx, openEph.ID); err != nil {
			t.Errorf("Open ephemeral should survive cleanup: %v", err)
		}
	})

	t.Run("SenderPreservation", func(t *testing.T) {
		if err := testStore.UpdateIssue(ctx, senderMsg.ID, map[string]interface{}{
			"description": "Updated status details",
		}, "test"); err != nil {
			t.Fatalf("UpdateIssue: %v", err)
		}

		got, err := testStore.GetIssue(ctx, senderMsg.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got.Sender != "agent-alpha" {
			t.Errorf("Sender = %q after update, want agent-alpha", got.Sender)
		}
		if got.Description != "Updated status details" {
			t.Errorf("Description not updated, got %q", got.Description)
		}
	})
}

// TestFindMailDelegate tests the mail delegate resolution logic.
// Kept separate because it modifies global state (env vars, store).
func TestFindMailDelegate(t *testing.T) {
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
// Kept separate because it modifies global store and rootCtx.
func TestMailDelegateFromConfig(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	if err := testStore.SetConfig(ctx, "mail.delegate", "gt mail"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	origBeads := os.Getenv("BEADS_MAIL_DELEGATE")
	origBD := os.Getenv("BD_MAIL_DELEGATE")
	os.Unsetenv("BEADS_MAIL_DELEGATE")
	os.Unsetenv("BD_MAIL_DELEGATE")
	defer func() {
		os.Setenv("BEADS_MAIL_DELEGATE", origBeads)
		os.Setenv("BD_MAIL_DELEGATE", origBD)
	}()

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
