package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestThreadTraversal tests the findRepliesTo() and findReplies() functions
// that were added in Decision 004 Phase 4 to support message thread navigation.
func TestThreadTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	// Create a 3-message thread chain: original → reply1 → reply2
	now := time.Now()

	original := &types.Issue{
		Title:       "Original Message",
		Description: "This is the original message",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Assignee:    "worker",
		Sender:      "manager",
		Ephemeral:        true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := testStore.CreateIssue(ctx, original, "test"); err != nil {
		t.Fatalf("Failed to create original message: %v", err)
	}

	reply1 := &types.Issue{
		Title:       "Re: Original Message",
		Description: "This is reply 1",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Assignee:    "manager",
		Sender:      "worker",
		Ephemeral:        true,
		CreatedAt:   now.Add(time.Minute),
		UpdatedAt:   now.Add(time.Minute),
	}
	if err := testStore.CreateIssue(ctx, reply1, "test"); err != nil {
		t.Fatalf("Failed to create reply1: %v", err)
	}

	reply2 := &types.Issue{
		Title:       "Re: Re: Original Message",
		Description: "This is reply 2",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Assignee:    "worker",
		Sender:      "manager",
		Ephemeral:        true,
		CreatedAt:   now.Add(2 * time.Minute),
		UpdatedAt:   now.Add(2 * time.Minute),
	}
	if err := testStore.CreateIssue(ctx, reply2, "test"); err != nil {
		t.Fatalf("Failed to create reply2: %v", err)
	}

	// Add replies-to dependencies to form the thread chain
	// reply1 replies to original
	dep1 := &types.Dependency{
		IssueID:     reply1.ID,
		DependsOnID: original.ID,
		Type:        types.DepRepliesTo,
		CreatedAt:   now.Add(time.Minute),
	}
	if err := testStore.AddDependency(ctx, dep1, "test"); err != nil {
		t.Fatalf("Failed to add reply1 -> original dependency: %v", err)
	}

	// reply2 replies to reply1
	dep2 := &types.Dependency{
		IssueID:     reply2.ID,
		DependsOnID: reply1.ID,
		Type:        types.DepRepliesTo,
		CreatedAt:   now.Add(2 * time.Minute),
	}
	if err := testStore.AddDependency(ctx, dep2, "test"); err != nil {
		t.Fatalf("Failed to add reply2 -> reply1 dependency: %v", err)
	}

	t.Run("findRepliesTo walks UP the thread", func(t *testing.T) {
		// From reply2, should find reply1
		parent := findRepliesTo(ctx, reply2.ID, nil, testStore)
		if parent != reply1.ID {
			t.Errorf("findRepliesTo(reply2) = %q, want %q", parent, reply1.ID)
		}

		// From reply1, should find original
		parent = findRepliesTo(ctx, reply1.ID, nil, testStore)
		if parent != original.ID {
			t.Errorf("findRepliesTo(reply1) = %q, want %q", parent, original.ID)
		}

		// From original, should return empty (no parent)
		parent = findRepliesTo(ctx, original.ID, nil, testStore)
		if parent != "" {
			t.Errorf("findRepliesTo(original) = %q, want empty string", parent)
		}
	})

	t.Run("findReplies walks DOWN the thread", func(t *testing.T) {
		// From original, should find reply1
		replies := findReplies(ctx, original.ID, nil, testStore)
		if len(replies) != 1 {
			t.Fatalf("findReplies(original) returned %d replies, want 1", len(replies))
		}
		if replies[0].ID != reply1.ID {
			t.Errorf("findReplies(original)[0].ID = %q, want %q", replies[0].ID, reply1.ID)
		}

		// From reply1, should find reply2
		replies = findReplies(ctx, reply1.ID, nil, testStore)
		if len(replies) != 1 {
			t.Fatalf("findReplies(reply1) returned %d replies, want 1", len(replies))
		}
		if replies[0].ID != reply2.ID {
			t.Errorf("findReplies(reply1)[0].ID = %q, want %q", replies[0].ID, reply2.ID)
		}

		// From reply2, should return empty (no children)
		replies = findReplies(ctx, reply2.ID, nil, testStore)
		if len(replies) != 0 {
			t.Errorf("findReplies(reply2) returned %d replies, want 0", len(replies))
		}
	})

	t.Run("thread root finding via repeated findRepliesTo", func(t *testing.T) {
		// Starting from reply2, walk up via findRepliesTo() until reaching original
		current := reply2.ID
		var visited []string
		visited = append(visited, current)

		for {
			parent := findRepliesTo(ctx, current, nil, testStore)
			if parent == "" {
				break
			}
			current = parent
			visited = append(visited, current)
		}

		// Should have visited: reply2, reply1, original
		if len(visited) != 3 {
			t.Fatalf("Thread walk visited %d nodes, want 3: %v", len(visited), visited)
		}

		// Final current should be the original (root)
		if current != original.ID {
			t.Errorf("Thread root = %q, want %q", current, original.ID)
		}

		// Verify visited order: reply2 -> reply1 -> original
		if visited[0] != reply2.ID {
			t.Errorf("visited[0] = %q, want %q", visited[0], reply2.ID)
		}
		if visited[1] != reply1.ID {
			t.Errorf("visited[1] = %q, want %q", visited[1], reply1.ID)
		}
		if visited[2] != original.ID {
			t.Errorf("visited[2] = %q, want %q", visited[2], original.ID)
		}
	})
}

// TestThreadTraversalEmptyThread tests thread traversal with an isolated message
func TestThreadTraversalEmptyThread(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	// Create a single message with no thread
	now := time.Now()
	standalone := &types.Issue{
		Title:       "Standalone Message",
		Description: "This message has no thread",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Assignee:    "user",
		Sender:      "sender",
		Ephemeral:        true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := testStore.CreateIssue(ctx, standalone, "test"); err != nil {
		t.Fatalf("Failed to create standalone message: %v", err)
	}

	// findRepliesTo should return empty
	parent := findRepliesTo(ctx, standalone.ID, nil, testStore)
	if parent != "" {
		t.Errorf("findRepliesTo(standalone) = %q, want empty string", parent)
	}

	// findReplies should return empty slice
	replies := findReplies(ctx, standalone.ID, nil, testStore)
	if len(replies) != 0 {
		t.Errorf("findReplies(standalone) returned %d replies, want 0", len(replies))
	}
}

// TestThreadTraversalBranching tests a branching thread (one message with multiple replies)
func TestThreadTraversalBranching(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	now := time.Now()

	// Create original message
	original := &types.Issue{
		Title:       "Original Message",
		Description: "This message will have multiple replies",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Assignee:    "user",
		Sender:      "sender",
		Ephemeral:        true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := testStore.CreateIssue(ctx, original, "test"); err != nil {
		t.Fatalf("Failed to create original message: %v", err)
	}

	// Create two replies to the original (branching)
	replyA := &types.Issue{
		Title:       "Reply A",
		Description: "First branch reply",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Assignee:    "sender",
		Sender:      "user",
		Ephemeral:        true,
		CreatedAt:   now.Add(time.Minute),
		UpdatedAt:   now.Add(time.Minute),
	}
	if err := testStore.CreateIssue(ctx, replyA, "test"); err != nil {
		t.Fatalf("Failed to create replyA: %v", err)
	}

	replyB := &types.Issue{
		Title:       "Reply B",
		Description: "Second branch reply",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Assignee:    "sender",
		Sender:      "another-user",
		Ephemeral:        true,
		CreatedAt:   now.Add(2 * time.Minute),
		UpdatedAt:   now.Add(2 * time.Minute),
	}
	if err := testStore.CreateIssue(ctx, replyB, "test"); err != nil {
		t.Fatalf("Failed to create replyB: %v", err)
	}

	// Both replies point to original
	depA := &types.Dependency{
		IssueID:     replyA.ID,
		DependsOnID: original.ID,
		Type:        types.DepRepliesTo,
		CreatedAt:   now.Add(time.Minute),
	}
	if err := testStore.AddDependency(ctx, depA, "test"); err != nil {
		t.Fatalf("Failed to add replyA -> original dependency: %v", err)
	}

	depB := &types.Dependency{
		IssueID:     replyB.ID,
		DependsOnID: original.ID,
		Type:        types.DepRepliesTo,
		CreatedAt:   now.Add(2 * time.Minute),
	}
	if err := testStore.AddDependency(ctx, depB, "test"); err != nil {
		t.Fatalf("Failed to add replyB -> original dependency: %v", err)
	}

	t.Run("findRepliesTo from branches find original", func(t *testing.T) {
		parentA := findRepliesTo(ctx, replyA.ID, nil, testStore)
		if parentA != original.ID {
			t.Errorf("findRepliesTo(replyA) = %q, want %q", parentA, original.ID)
		}

		parentB := findRepliesTo(ctx, replyB.ID, nil, testStore)
		if parentB != original.ID {
			t.Errorf("findRepliesTo(replyB) = %q, want %q", parentB, original.ID)
		}
	})

	t.Run("findReplies from original returns both branches", func(t *testing.T) {
		replies := findReplies(ctx, original.ID, nil, testStore)
		if len(replies) != 2 {
			t.Fatalf("findReplies(original) returned %d replies, want 2", len(replies))
		}

		// Verify both replies are present (order may vary)
		foundA := false
		foundB := false
		for _, r := range replies {
			if r.ID == replyA.ID {
				foundA = true
			}
			if r.ID == replyB.ID {
				foundB = true
			}
		}
		if !foundA {
			t.Errorf("findReplies(original) missing replyA")
		}
		if !foundB {
			t.Errorf("findReplies(original) missing replyB")
		}
	})
}

// TestThreadTraversalNonexistentIssue tests behavior with nonexistent issue IDs
func TestThreadTraversalNonexistentIssue(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	// findRepliesTo with nonexistent ID should return empty
	parent := findRepliesTo(ctx, "nonexistent-id", nil, testStore)
	if parent != "" {
		t.Errorf("findRepliesTo(nonexistent) = %q, want empty string", parent)
	}

	// findReplies with nonexistent ID should return nil/empty
	replies := findReplies(ctx, "nonexistent-id", nil, testStore)
	if len(replies) != 0 {
		t.Errorf("findReplies(nonexistent) returned %d replies, want 0", len(replies))
	}
}

// TestThreadTraversalOnlyRepliesTo verifies that only replies-to dependencies are followed
func TestThreadTraversalOnlyRepliesTo(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	now := time.Now()

	// Create three messages to test different dependency types
	msg1 := &types.Issue{
		Title:       "Message 1",
		Description: "First message (target of blocks dep)",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Assignee:    "user",
		Sender:      "sender",
		Ephemeral:        true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := testStore.CreateIssue(ctx, msg1, "test"); err != nil {
		t.Fatalf("Failed to create msg1: %v", err)
	}

	msg2 := &types.Issue{
		Title:       "Message 2",
		Description: "Second message with blocks dependency to msg1",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeMessage,
		Assignee:    "user",
		Sender:      "sender",
		Ephemeral:        true,
		CreatedAt:   now.Add(time.Minute),
		UpdatedAt:   now.Add(time.Minute),
	}
	if err := testStore.CreateIssue(ctx, msg2, "test"); err != nil {
		t.Fatalf("Failed to create msg2: %v", err)
	}

	// Add a "blocks" dependency (NOT replies-to)
	// msg2 depends on (blocks) msg1
	blocksDep := &types.Dependency{
		IssueID:     msg2.ID,
		DependsOnID: msg1.ID,
		Type:        types.DepBlocks,
		CreatedAt:   now.Add(time.Minute),
	}
	if err := testStore.AddDependency(ctx, blocksDep, "test"); err != nil {
		t.Fatalf("Failed to add blocks dependency: %v", err)
	}

	// findRepliesTo should NOT find msg1 (blocks dependency, not replies-to)
	parent := findRepliesTo(ctx, msg2.ID, nil, testStore)
	if parent != "" {
		t.Errorf("findRepliesTo(msg2) = %q, want empty (blocks dep should be ignored)", parent)
	}

	// findReplies from msg1 should NOT find msg2 (blocks dependency, not replies-to)
	replies := findReplies(ctx, msg1.ID, nil, testStore)
	if len(replies) != 0 {
		t.Errorf("findReplies(msg1) returned %d replies, want 0 (blocks dep should be ignored)", len(replies))
	}
}
