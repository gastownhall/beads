package memory

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestLoadFromIssues_InitializesChildCounters(t *testing.T) {
	store := New("")
	ctx := context.Background()

	issues := []*types.Issue{
		{ID: "bd-parent", Title: "Parent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic},
		{ID: "bd-parent.1", Title: "Child 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "bd-parent.3", Title: "Child 3", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "bd-parent.1.2", Title: "Nested Child 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
	}

	if err := store.LoadFromIssues(issues); err != nil {
		t.Fatalf("LoadFromIssues failed: %v", err)
	}

	next, err := store.GetNextChildID(ctx, "bd-parent")
	if err != nil {
		t.Fatalf("GetNextChildID failed: %v", err)
	}
	if next != "bd-parent.4" {
		t.Fatalf("GetNextChildID = %q, want %q", next, "bd-parent.4")
	}

	nextNested, err := store.GetNextChildID(ctx, "bd-parent.1")
	if err != nil {
		t.Fatalf("GetNextChildID (nested) failed: %v", err)
	}
	if nextNested != "bd-parent.1.3" {
		t.Fatalf("GetNextChildID (nested) = %q, want %q", nextNested, "bd-parent.1.3")
	}
}

func TestGetReadyWork_ExcludesIssuesWithOpenBlocksDependencies(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	closedAt := time.Now()
	blocker := &types.Issue{ID: "bd-1", Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	blocked := &types.Issue{ID: "bd-2", Title: "Blocked", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	closedBlocker := &types.Issue{ID: "bd-3", Title: "Closed blocker", Status: types.StatusClosed, Priority: 1, IssueType: types.TypeTask, ClosedAt: &closedAt}
	unblocked := &types.Issue{ID: "bd-4", Title: "Unblocked", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{blocker, blocked, closedBlocker, unblocked} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// bd-2 is blocked by an open issue
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     blocked.ID,
		DependsOnID: blocker.ID,
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
		CreatedBy:   "test",
	}, "test"); err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	// bd-4 is "blocked" by a closed issue, which should not block ready work
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     unblocked.ID,
		DependsOnID: closedBlocker.ID,
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
		CreatedBy:   "test",
	}, "test"); err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	ready, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork failed: %v", err)
	}

	got := map[string]bool{}
	for _, issue := range ready {
		got[issue.ID] = true
	}

	if got[blocked.ID] {
		t.Fatalf("GetReadyWork should not include blocked issue %s", blocked.ID)
	}
	if !got[unblocked.ID] {
		t.Fatalf("GetReadyWork should include unblocked issue %s", unblocked.ID)
	}
}

func TestGetBlockedIssues_IncludesExplicitlyBlockedStatus(t *testing.T) {
	store := setupTestMemory(t)
	defer store.Close()

	ctx := context.Background()

	explicit := &types.Issue{ID: "bd-1", Title: "Explicitly blocked", Status: types.StatusBlocked, Priority: 1, IssueType: types.TypeTask}
	blocker := &types.Issue{ID: "bd-2", Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	implicitlyBlocked := &types.Issue{ID: "bd-3", Title: "Implicitly blocked", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{explicit, blocker, implicitlyBlocked} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     implicitlyBlocked.ID,
		DependsOnID: blocker.ID,
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
		CreatedBy:   "test",
	}, "test"); err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	blocked, err := store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues failed: %v", err)
	}

	var foundExplicit, foundImplicit bool
	for _, bi := range blocked {
		switch bi.ID {
		case explicit.ID:
			foundExplicit = true
			if bi.BlockedByCount != 0 {
				t.Fatalf("explicit blocked issue should have BlockedByCount=0, got %d", bi.BlockedByCount)
			}
		case implicitlyBlocked.ID:
			foundImplicit = true
			if bi.BlockedByCount != 1 || len(bi.BlockedBy) != 1 || bi.BlockedBy[0] != blocker.ID {
				t.Fatalf("implicit blocked issue blockers mismatch: count=%d blockers=%v", bi.BlockedByCount, bi.BlockedBy)
			}
		}
	}

	if !foundExplicit {
		t.Fatalf("expected explicit blocked issue %s", explicit.ID)
	}
	if !foundImplicit {
		t.Fatalf("expected implicitly blocked issue %s", implicitlyBlocked.ID)
	}
}
