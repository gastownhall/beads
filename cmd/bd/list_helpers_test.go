package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestListParseTimeFlag(t *testing.T) {
	cases := []string{
		"2025-12-26",
		"2025-12-26T12:34:56",
		"2025-12-26 12:34:56",
		time.DateOnly,
		time.RFC3339,
	}

	for _, c := range cases {
		// Just make sure we accept the expected formats.
		var s string
		switch c {
		case time.DateOnly:
			s = "2025-12-26"
		case time.RFC3339:
			s = "2025-12-26T12:34:56Z"
		default:
			s = c
		}
		got, err := parseTimeFlag(s)
		if err != nil {
			t.Fatalf("parseTimeFlag(%q) error: %v", s, err)
		}
		if got.Year() != 2025 {
			t.Fatalf("parseTimeFlag(%q) year=%d, want 2025", s, got.Year())
		}
	}

	if _, err := parseTimeFlag("not-a-date"); err == nil {
		t.Fatalf("expected error")
	}
}

func TestListPinIndicator(t *testing.T) {
	if pinIndicator(&types.Issue{Pinned: true}) == "" {
		t.Fatalf("expected pin indicator")
	}
	if pinIndicator(&types.Issue{Pinned: false}) != "" {
		t.Fatalf("expected empty pin indicator")
	}
}

func TestListFormatPrettyIssue_BadgesAndDefaults(t *testing.T) {
	iss := &types.Issue{ID: "bd-1", Title: "Hello", Status: "wat", Priority: 99, IssueType: "bug"}
	out := formatPrettyIssue(iss)
	if !strings.Contains(out, "bd-1") || !strings.Contains(out, "Hello") {
		t.Fatalf("unexpected output: %q", out)
	}
	if !strings.Contains(out, "[bug]") {
		t.Fatalf("expected bug badge: %q", out)
	}
}

func TestListBuildIssueTree_ParentChildByDotID(t *testing.T) {
	parent := &types.Issue{ID: "bd-1", Title: "Parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	child := &types.Issue{ID: "bd-1.1", Title: "Child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	orphan := &types.Issue{ID: "bd-2.1", Title: "Orphan", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}

	roots, children := buildIssueTree([]*types.Issue{child, parent, orphan})
	if len(children["bd-1"]) != 1 || children["bd-1"][0].ID != "bd-1.1" {
		t.Fatalf("expected bd-1 to have bd-1.1 child: %+v", children)
	}
	if len(roots) != 2 {
		t.Fatalf("expected 2 roots (parent + orphan), got %d", len(roots))
	}
}

// Regression test for https://github.com/steveyegge/beads/issues/1446
// A task with multiple dependencies on the same epic should only appear once.
func TestListBuildIssueTree_NoDuplicateChildrenFromMultipleDeps(t *testing.T) {
	epic := &types.Issue{ID: "bd-epic", Title: "Epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
	task := &types.Issue{ID: "bd-task", Title: "Task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}

	// The task has two different dependency types pointing at the same epic
	allDeps := map[string][]*types.Dependency{
		"bd-task": {
			{IssueID: "bd-task", DependsOnID: "bd-epic", Type: types.DepParentChild},
			{IssueID: "bd-task", DependsOnID: "bd-epic", Type: types.DepBlocks},
		},
	}

	roots, children := buildIssueTreeWithDeps([]*types.Issue{epic, task}, allDeps)

	if len(roots) != 1 || roots[0].ID != "bd-epic" {
		t.Fatalf("expected 1 root (epic), got %d: %+v", len(roots), roots)
	}
	if len(children["bd-epic"]) != 1 {
		t.Fatalf("expected 1 child under epic, got %d", len(children["bd-epic"]))
	}
	if children["bd-epic"][0].ID != "bd-task" {
		t.Fatalf("expected bd-task as child, got %s", children["bd-epic"][0].ID)
	}
}

func TestListSortIssues_ClosedNilLast(t *testing.T) {
	t1 := time.Now().Add(-2 * time.Hour)
	t2 := time.Now().Add(-1 * time.Hour)

	closedOld := &types.Issue{ID: "bd-1", ClosedAt: &t1}
	closedNew := &types.Issue{ID: "bd-2", ClosedAt: &t2}
	open := &types.Issue{ID: "bd-3", ClosedAt: nil}

	issues := []*types.Issue{open, closedOld, closedNew}
	sortIssues(issues, "closed", false)
	if issues[0].ID != "bd-2" || issues[1].ID != "bd-1" || issues[2].ID != "bd-3" {
		t.Fatalf("unexpected order: %s, %s, %s", issues[0].ID, issues[1].ID, issues[2].ID)
	}
}

func TestListDisplayPrettyList(t *testing.T) {
	out := captureStdout(t, func() error {
		displayPrettyList(nil, false)
		return nil
	})
	if !strings.Contains(out, "No issues found") {
		t.Fatalf("unexpected output: %q", out)
	}

	issues := []*types.Issue{
		{ID: "bd-1", Title: "A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bd-2", Title: "B", Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeFeature},
		{ID: "bd-1.1", Title: "C", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}

	out = captureStdout(t, func() error {
		displayPrettyList(issues, false)
		return nil
	})
	if !strings.Contains(out, "bd-1") || !strings.Contains(out, "bd-1.1") || !strings.Contains(out, "Total:") {
		t.Fatalf("unexpected output: %q", out)
	}
}
