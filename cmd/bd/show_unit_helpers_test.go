//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestValidateIssueUpdatable(t *testing.T) {
	if err := validateIssueUpdatable("x", nil); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if err := validateIssueUpdatable("x", &types.Issue{IsTemplate: false}); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if err := validateIssueUpdatable("bd-1", &types.Issue{IsTemplate: true}); err == nil {
		t.Fatalf("expected error")
	}
}

func TestFindRepliesToAndReplies_WorksWithDoltStorage(t *testing.T) {
	ctx := context.Background()
	st := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	root := &types.Issue{Title: "root", Status: types.StatusOpen, Priority: 2, IssueType: "message", Sender: "a", Assignee: "b"}
	reply1 := &types.Issue{Title: "r1", Status: types.StatusOpen, Priority: 2, IssueType: "message", Sender: "b", Assignee: "a"}
	reply2 := &types.Issue{Title: "r2", Status: types.StatusOpen, Priority: 2, IssueType: "message", Sender: "a", Assignee: "b"}
	if err := st.CreateIssue(ctx, root, "tester"); err != nil {
		t.Fatalf("CreateIssue(root): %v", err)
	}
	if err := st.CreateIssue(ctx, reply1, "tester"); err != nil {
		t.Fatalf("CreateIssue(reply1): %v", err)
	}
	if err := st.CreateIssue(ctx, reply2, "tester"); err != nil {
		t.Fatalf("CreateIssue(reply2): %v", err)
	}

	if err := st.AddDependency(ctx, &types.Dependency{IssueID: reply1.ID, DependsOnID: root.ID, Type: types.DepRepliesTo}, "tester"); err != nil {
		t.Fatalf("AddDependency(reply1->root): %v", err)
	}
	if err := st.AddDependency(ctx, &types.Dependency{IssueID: reply2.ID, DependsOnID: reply1.ID, Type: types.DepRepliesTo}, "tester"); err != nil {
		t.Fatalf("AddDependency(reply2->reply1): %v", err)
	}

	if got := findRepliesTo(ctx, root.ID, st); got != "" {
		t.Fatalf("expected root replies-to to be empty, got %q", got)
	}
	if got := findRepliesTo(ctx, reply2.ID, st); got != reply1.ID {
		t.Fatalf("expected reply2 parent %q, got %q", reply1.ID, got)
	}

	rootReplies := findReplies(ctx, root.ID, st)
	if len(rootReplies) != 1 || rootReplies[0].ID != reply1.ID {
		t.Fatalf("expected root replies [%s], got %+v", reply1.ID, rootReplies)
	}
	r1Replies := findReplies(ctx, reply1.ID, st)
	if len(r1Replies) != 1 || r1Replies[0].ID != reply2.ID {
		t.Fatalf("expected reply1 replies [%s], got %+v", reply2.ID, r1Replies)
	}
}
