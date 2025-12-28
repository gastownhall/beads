package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/memory"
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

func TestValidateIssueClosable(t *testing.T) {
	if err := validateIssueClosable("x", nil, false); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if err := validateIssueClosable("bd-1", &types.Issue{IsTemplate: true}, false); err == nil {
		t.Fatalf("expected template close error")
	}
	if err := validateIssueClosable("bd-2", &types.Issue{Status: types.StatusPinned}, false); err == nil {
		t.Fatalf("expected pinned close error")
	}
	if err := validateIssueClosable("bd-2", &types.Issue{Status: types.StatusPinned}, true); err != nil {
		t.Fatalf("expected pinned close to succeed with force, got %v", err)
	}
}

func TestApplyLabelUpdates_SetAddRemove(t *testing.T) {
	ctx := context.Background()
	st := memory.New("")
	if err := st.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	issue := &types.Issue{Title: "x", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := st.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	_ = st.AddLabel(ctx, issue.ID, "old1", "tester")
	_ = st.AddLabel(ctx, issue.ID, "old2", "tester")

	if err := applyLabelUpdates(ctx, st, issue.ID, "tester", []string{"a", "b"}, []string{"b", "c"}, []string{"a"}); err != nil {
		t.Fatalf("applyLabelUpdates: %v", err)
	}
	labels, _ := st.GetLabels(ctx, issue.ID)
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %v", labels)
	}
	// Order is not guaranteed.
	foundB := false
	foundC := false
	for _, l := range labels {
		if l == "b" {
			foundB = true
		}
		if l == "c" {
			foundC = true
		}
		if l == "old1" || l == "old2" || l == "a" {
			t.Fatalf("unexpected label %q in %v", l, labels)
		}
	}
	if !foundB || !foundC {
		t.Fatalf("expected labels b and c, got %v", labels)
	}
}

func TestApplyLabelUpdates_AddRemoveOnly(t *testing.T) {
	ctx := context.Background()
	st := memory.New("")
	issue := &types.Issue{Title: "x", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := st.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	_ = st.AddLabel(ctx, issue.ID, "a", "tester")
	if err := applyLabelUpdates(ctx, st, issue.ID, "tester", nil, []string{"b"}, []string{"a"}); err != nil {
		t.Fatalf("applyLabelUpdates: %v", err)
	}
	labels, _ := st.GetLabels(ctx, issue.ID)
	if len(labels) != 1 || labels[0] != "b" {
		t.Fatalf("expected [b], got %v", labels)
	}
}

func TestFindRepliesToAndReplies_WorksWithMemoryStorage(t *testing.T) {
	ctx := context.Background()
	st := memory.New("")
	if err := st.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	root := &types.Issue{Title: "root", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeMessage, Sender: "a", Assignee: "b"}
	reply1 := &types.Issue{Title: "r1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeMessage, Sender: "b", Assignee: "a"}
	reply2 := &types.Issue{Title: "r2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeMessage, Sender: "a", Assignee: "b"}
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

	if got := findRepliesTo(ctx, root.ID, nil, st); got != "" {
		t.Fatalf("expected root replies-to to be empty, got %q", got)
	}
	if got := findRepliesTo(ctx, reply2.ID, nil, st); got != reply1.ID {
		t.Fatalf("expected reply2 parent %q, got %q", reply1.ID, got)
	}

	rootReplies := findReplies(ctx, root.ID, nil, st)
	if len(rootReplies) != 1 || rootReplies[0].ID != reply1.ID {
		t.Fatalf("expected root replies [%s], got %+v", reply1.ID, rootReplies)
	}
	r1Replies := findReplies(ctx, reply1.ID, nil, st)
	if len(r1Replies) != 1 || r1Replies[0].ID != reply2.ID {
		t.Fatalf("expected reply1 replies [%s], got %+v", reply2.ID, r1Replies)
	}
}
