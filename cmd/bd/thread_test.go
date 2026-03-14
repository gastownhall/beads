//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/beads/internal/types"
)

func TestThreadTraversalSuite(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testStore := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()
	now := time.Now()

	// === Linear chain data: original → reply1 → reply2 ===
	original := &types.Issue{
		ID: "tt-orig", Title: "Original Message", Description: "This is the original message",
		Status: types.StatusOpen, Priority: 2, IssueType: "message",
		Assignee: "worker", Sender: "manager", Ephemeral: true,
		CreatedAt: now, UpdatedAt: now,
	}
	reply1 := &types.Issue{
		ID: "tt-reply1", Title: "Re: Original Message", Description: "This is reply 1",
		Status: types.StatusOpen, Priority: 2, IssueType: "message",
		Assignee: "manager", Sender: "worker", Ephemeral: true,
		CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute),
	}
	reply2 := &types.Issue{
		ID: "tt-reply2", Title: "Re: Re: Original Message", Description: "This is reply 2",
		Status: types.StatusOpen, Priority: 2, IssueType: "message",
		Assignee: "worker", Sender: "manager", Ephemeral: true,
		CreatedAt: now.Add(2 * time.Minute), UpdatedAt: now.Add(2 * time.Minute),
	}
	for _, msg := range []*types.Issue{original, reply1, reply2} {
		if err := testStore.CreateIssue(ctx, msg, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", msg.ID, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: reply1.ID, DependsOnID: original.ID, Type: types.DepRepliesTo, CreatedAt: now.Add(time.Minute)},
		{IssueID: reply2.ID, DependsOnID: reply1.ID, Type: types.DepRepliesTo, CreatedAt: now.Add(2 * time.Minute)},
	} {
		if err := testStore.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
	}

	// === Standalone message data ===
	standalone := &types.Issue{
		ID: "tt-standalone", Title: "Standalone Message", Description: "No thread",
		Status: types.StatusOpen, Priority: 2, IssueType: "message",
		Assignee: "user", Sender: "sender", Ephemeral: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := testStore.CreateIssue(ctx, standalone, "test"); err != nil {
		t.Fatalf("CreateIssue(standalone): %v", err)
	}

	// === Branching data: branchOrig → replyA, replyB ===
	branchOrig := &types.Issue{
		ID: "tt-branch-orig", Title: "Branch Original", Description: "Multiple replies",
		Status: types.StatusOpen, Priority: 2, IssueType: "message",
		Assignee: "user", Sender: "sender", Ephemeral: true,
		CreatedAt: now, UpdatedAt: now,
	}
	replyA := &types.Issue{
		ID: "tt-reply-a", Title: "Reply A", Description: "First branch",
		Status: types.StatusOpen, Priority: 2, IssueType: "message",
		Assignee: "sender", Sender: "user", Ephemeral: true,
		CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute),
	}
	replyB := &types.Issue{
		ID: "tt-reply-b", Title: "Reply B", Description: "Second branch",
		Status: types.StatusOpen, Priority: 2, IssueType: "message",
		Assignee: "sender", Sender: "another-user", Ephemeral: true,
		CreatedAt: now.Add(2 * time.Minute), UpdatedAt: now.Add(2 * time.Minute),
	}
	for _, msg := range []*types.Issue{branchOrig, replyA, replyB} {
		if err := testStore.CreateIssue(ctx, msg, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", msg.ID, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: replyA.ID, DependsOnID: branchOrig.ID, Type: types.DepRepliesTo, CreatedAt: now.Add(time.Minute)},
		{IssueID: replyB.ID, DependsOnID: branchOrig.ID, Type: types.DepRepliesTo, CreatedAt: now.Add(2 * time.Minute)},
	} {
		if err := testStore.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
	}

	// === OnlyRepliesTo data: blocks dep should be ignored ===
	msg1 := &types.Issue{
		ID: "tt-blocks-1", Title: "Message 1", Description: "Target of blocks dep",
		Status: types.StatusOpen, Priority: 2, IssueType: "message",
		Assignee: "user", Sender: "sender", Ephemeral: true,
		CreatedAt: now, UpdatedAt: now,
	}
	msg2 := &types.Issue{
		ID: "tt-blocks-2", Title: "Message 2", Description: "Has blocks dep to msg1",
		Status: types.StatusOpen, Priority: 2, IssueType: "message",
		Assignee: "user", Sender: "sender", Ephemeral: true,
		CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute),
	}
	for _, msg := range []*types.Issue{msg1, msg2} {
		if err := testStore.CreateIssue(ctx, msg, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", msg.ID, err)
		}
	}
	if err := testStore.AddDependency(ctx, &types.Dependency{
		IssueID: msg2.ID, DependsOnID: msg1.ID, Type: types.DepBlocks, CreatedAt: now.Add(time.Minute),
	}, "test"); err != nil {
		t.Fatalf("AddDependency(blocks): %v", err)
	}

	// === Tests ===

	t.Run("LinearChain_findRepliesTo_walksUp", func(t *testing.T) {
		parent := findRepliesTo(ctx, reply2.ID, testStore)
		if parent != reply1.ID {
			t.Errorf("findRepliesTo(reply2) = %q, want %q", parent, reply1.ID)
		}
		parent = findRepliesTo(ctx, reply1.ID, testStore)
		if parent != original.ID {
			t.Errorf("findRepliesTo(reply1) = %q, want %q", parent, original.ID)
		}
		parent = findRepliesTo(ctx, original.ID, testStore)
		if parent != "" {
			t.Errorf("findRepliesTo(original) = %q, want empty", parent)
		}
	})

	t.Run("LinearChain_findReplies_walksDown", func(t *testing.T) {
		replies := findReplies(ctx, original.ID, testStore)
		if len(replies) != 1 || replies[0].ID != reply1.ID {
			t.Errorf("findReplies(original) = %v, want [%s]", replies, reply1.ID)
		}
		replies = findReplies(ctx, reply1.ID, testStore)
		if len(replies) != 1 || replies[0].ID != reply2.ID {
			t.Errorf("findReplies(reply1) = %v, want [%s]", replies, reply2.ID)
		}
		replies = findReplies(ctx, reply2.ID, testStore)
		if len(replies) != 0 {
			t.Errorf("findReplies(reply2) = %d replies, want 0", len(replies))
		}
	})

	t.Run("LinearChain_threadRootFinding", func(t *testing.T) {
		current := reply2.ID
		var visited []string
		visited = append(visited, current)
		for {
			parent := findRepliesTo(ctx, current, testStore)
			if parent == "" {
				break
			}
			current = parent
			visited = append(visited, current)
		}
		if len(visited) != 3 {
			t.Fatalf("Thread walk visited %d nodes, want 3: %v", len(visited), visited)
		}
		if current != original.ID {
			t.Errorf("Thread root = %q, want %q", current, original.ID)
		}
	})

	t.Run("EmptyThread_standalone", func(t *testing.T) {
		parent := findRepliesTo(ctx, standalone.ID, testStore)
		if parent != "" {
			t.Errorf("findRepliesTo(standalone) = %q, want empty", parent)
		}
		replies := findReplies(ctx, standalone.ID, testStore)
		if len(replies) != 0 {
			t.Errorf("findReplies(standalone) = %d, want 0", len(replies))
		}
	})

	t.Run("Branching_findRepliesTo", func(t *testing.T) {
		parentA := findRepliesTo(ctx, replyA.ID, testStore)
		if parentA != branchOrig.ID {
			t.Errorf("findRepliesTo(replyA) = %q, want %q", parentA, branchOrig.ID)
		}
		parentB := findRepliesTo(ctx, replyB.ID, testStore)
		if parentB != branchOrig.ID {
			t.Errorf("findRepliesTo(replyB) = %q, want %q", parentB, branchOrig.ID)
		}
	})

	t.Run("Branching_findReplies_returnsBoth", func(t *testing.T) {
		replies := findReplies(ctx, branchOrig.ID, testStore)
		if len(replies) != 2 {
			t.Fatalf("findReplies(branchOrig) = %d, want 2", len(replies))
		}
		foundA, foundB := false, false
		for _, r := range replies {
			if r.ID == replyA.ID {
				foundA = true
			}
			if r.ID == replyB.ID {
				foundB = true
			}
		}
		if !foundA || !foundB {
			t.Errorf("missing replies: A=%v B=%v", foundA, foundB)
		}
	})

	t.Run("NonexistentIssue", func(t *testing.T) {
		parent := findRepliesTo(ctx, "nonexistent-id", testStore)
		if parent != "" {
			t.Errorf("findRepliesTo(nonexistent) = %q, want empty", parent)
		}
		replies := findReplies(ctx, "nonexistent-id", testStore)
		if len(replies) != 0 {
			t.Errorf("findReplies(nonexistent) = %d, want 0", len(replies))
		}
	})

	t.Run("OnlyRepliesTo_ignoresBlocksDeps", func(t *testing.T) {
		parent := findRepliesTo(ctx, msg2.ID, testStore)
		if parent != "" {
			t.Errorf("findRepliesTo(msg2) = %q, want empty (blocks should be ignored)", parent)
		}
		replies := findReplies(ctx, msg1.ID, testStore)
		if len(replies) != 0 {
			t.Errorf("findReplies(msg1) = %d, want 0 (blocks should be ignored)", len(replies))
		}
	})
}
