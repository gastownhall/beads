package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func cloneIssues(in []*types.Issue) []*types.Issue {
	out := make([]*types.Issue, len(in))
	copy(out, in)
	return out
}

func firstIndex(haystack, needle string) int {
	i := strings.Index(haystack, needle)
	if i < 0 {
		return 1 << 30
	}
	return i
}

func assertIDOrder(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSortListTreeGroup_DefaultPriorityThenID(t *testing.T) {
	// Mixed insertion order so a no-op sort cannot pass.
	issues := []*types.Issue{
		{ID: "scr-z", Title: "Charlie", Priority: 2},
		{ID: "scr-a", Title: "Alpha", Priority: 2},
		{ID: "scr-m", Title: "Beta", Priority: 1},
	}
	sortListTreeGroup(issues, "", false)
	assertIDOrder(t, idsOf(issues), "scr-m", "scr-a", "scr-z")
}

func TestSortListTreeGroup_ExplicitSortAndReverse(t *testing.T) {
	issues := []*types.Issue{
		{ID: "scr-z", Title: "Charlie", Priority: 2},
		{ID: "scr-a", Title: "Alpha", Priority: 3},
		{ID: "scr-m", Title: "Beta", Priority: 1},
	}

	byTitle := cloneIssues(issues)
	sortListTreeGroup(byTitle, "title", false)
	assertIDOrder(t, idsOf(byTitle), "scr-a", "scr-m", "scr-z")

	reversed := cloneIssues(issues)
	sortListTreeGroup(reversed, "title", true)
	assertIDOrder(t, idsOf(reversed), "scr-z", "scr-m", "scr-a")
}

func TestSortListTreeGroup_EmptySortLeavesDefaultEvenWithReverse(t *testing.T) {
	issues := []*types.Issue{
		{ID: "scr-z", Title: "Charlie", Priority: 2},
		{ID: "scr-a", Title: "Alpha", Priority: 2},
		{ID: "scr-m", Title: "Beta", Priority: 1},
	}
	// --reverse with no --sort is a no-op on the JSON path (SortRows returns
	// immediately). The tree must keep the same default, not invert priority.
	sortListTreeGroup(issues, "", true)
	assertIDOrder(t, idsOf(issues), "scr-m", "scr-a", "scr-z")
}

func TestDisplayPrettyList_HonorsSortReverseAndDefault(t *testing.T) {
	t0 := time.Date(2026, 8, 21, 8, 54, 40, 0, time.UTC)
	delta := &types.Issue{ID: "scr-1kx", Title: "Delta task", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask, UpdatedAt: t0.Add(3 * time.Second)}
	charlie := &types.Issue{ID: "scr-1py", Title: "Charlie task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, UpdatedAt: t0.Add(1 * time.Second)}
	alpha := &types.Issue{ID: "scr-t85", Title: "Alpha task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, UpdatedAt: t0.Add(2 * time.Second)}
	// Insertion order is neither priority, title, nor updated.
	issues := []*types.Issue{delta, charlie, alpha}

	defaultOut := captureStdout(t, func() error {
		displayPrettyListWithDepsMode(cloneIssues(issues), false, nil, "", false, false, "", "", false)
		return nil
	})
	if !(firstIndex(defaultOut, "scr-1py") < firstIndex(defaultOut, "scr-t85") && firstIndex(defaultOut, "scr-t85") < firstIndex(defaultOut, "scr-1kx")) {
		t.Fatalf("default tree must stay priority-then-ID (Charlie P1, Alpha P2, Delta P3), got:\n%s", defaultOut)
	}

	titleOut := captureStdout(t, func() error {
		displayPrettyListWithDepsMode(cloneIssues(issues), false, nil, "", false, false, "", "title", false)
		return nil
	})
	if !(firstIndex(titleOut, "scr-t85") < firstIndex(titleOut, "scr-1py") && firstIndex(titleOut, "scr-1py") < firstIndex(titleOut, "scr-1kx")) {
		t.Fatalf("--sort title must render Alpha, Charlie, Delta, got:\n%s", titleOut)
	}

	reverseOut := captureStdout(t, func() error {
		displayPrettyListWithDepsMode(cloneIssues(issues), false, nil, "", false, false, "", "title", true)
		return nil
	})
	if !(firstIndex(reverseOut, "scr-1kx") < firstIndex(reverseOut, "scr-1py") && firstIndex(reverseOut, "scr-1py") < firstIndex(reverseOut, "scr-t85")) {
		t.Fatalf("--sort title --reverse must render Delta, Charlie, Alpha, got:\n%s", reverseOut)
	}

	updatedOut := captureStdout(t, func() error {
		displayPrettyListWithDepsMode(cloneIssues(issues), false, nil, "", false, false, "", "updated", false)
		return nil
	})
	// CompareIssuesBy("updated") is newest first, matching --json.
	if !(firstIndex(updatedOut, "scr-1kx") < firstIndex(updatedOut, "scr-t85") && firstIndex(updatedOut, "scr-t85") < firstIndex(updatedOut, "scr-1py")) {
		t.Fatalf("--sort updated must render newest first (Delta, Alpha, Charlie), got:\n%s", updatedOut)
	}
}

func TestApplyListTreeOrder_EqualKeysKeepPageOrder(t *testing.T) {
	same := time.Date(2026, 8, 21, 8, 54, 46, 0, time.UTC)
	newer := same.Add(time.Second)
	page := []*types.Issue{
		{ID: "scr-1kx", Title: "Delta", Priority: 3, UpdatedAt: newer},
		{ID: "scr-1py", Title: "Charlie", Priority: 1, UpdatedAt: same},
		{ID: "scr-t85", Title: "Alpha", Priority: 2, UpdatedAt: same},
		{ID: "scr-wh3", Title: "Beta", Priority: 0, UpdatedAt: same},
	}
	// Start from the tree's default priority order so a missing restore
	// would emit Beta, Charlie, Alpha after the equal-key group.
	level := []*types.Issue{page[3], page[1], page[2], page[0]}
	applyListTreeOrder(level, page, "updated", false)
	assertIDOrder(t, idsOf(level), "scr-1kx", "scr-1py", "scr-t85", "scr-wh3")
}

func TestDisplayPrettyList_SortAppliesToSiblings(t *testing.T) {
	parent := &types.Issue{ID: "scr-p", Title: "Parent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	childZ := &types.Issue{ID: "scr-z", Title: "Zebra", Status: types.StatusOpen, Priority: 0, IssueType: types.TypeTask}
	childA := &types.Issue{ID: "scr-a", Title: "Aardvark", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	deps := map[string][]*types.Dependency{
		"scr-z": {{IssueID: "scr-z", DependsOnID: "scr-p", Type: types.DepParentChild}},
		"scr-a": {{IssueID: "scr-a", DependsOnID: "scr-p", Type: types.DepParentChild}},
	}
	issues := []*types.Issue{childZ, parent, childA}

	defaultOut := captureStdout(t, func() error {
		displayPrettyListWithDepsMode(cloneIssues(issues), false, deps, "", false, false, "", "", false)
		return nil
	})
	// Default sibling order is priority: Zebra P0 before Aardvark P2.
	if !(firstIndex(defaultOut, "scr-z") < firstIndex(defaultOut, "scr-a")) {
		t.Fatalf("default sibling order must stay priority (Zebra P0 before Aardvark P2), got:\n%s", defaultOut)
	}

	titleOut := captureStdout(t, func() error {
		displayPrettyListWithDepsMode(cloneIssues(issues), false, deps, "", false, false, "", "title", false)
		return nil
	})
	if !(firstIndex(titleOut, "scr-a") < firstIndex(titleOut, "scr-z")) {
		t.Fatalf("--sort title must order siblings Aardvark then Zebra, got:\n%s", titleOut)
	}
}
