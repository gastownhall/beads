//go:build cgo

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

type epicTestHelper struct {
	s   *dolt.DoltStore
	ctx context.Context
	t   *testing.T
}

func newEpicTestHelper(t *testing.T) *epicTestHelper {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	return &epicTestHelper{
		s:   newTestStore(t, testDB),
		ctx: context.Background(),
		t:   t,
	}
}

func (h *epicTestHelper) createIssue(issue *types.Issue) {
	if err := h.s.CreateIssue(h.ctx, issue, "test"); err != nil {
		h.t.Fatal(err)
	}
}

func (h *epicTestHelper) addDependency(dep *types.Dependency) {
	if err := h.s.AddDependency(h.ctx, dep, "test"); err != nil {
		h.t.Fatal(err)
	}
}

func (h *epicTestHelper) getEpicStatus(epicID string) *types.EpicStatus {
	epics, err := h.s.GetEpicsEligibleForClosure(h.ctx)
	if err != nil {
		h.t.Fatalf("GetEpicsEligibleForClosure failed: %v", err)
	}

	for _, epic := range epics {
		if epic.Epic.ID == epicID {
			return epic
		}
	}
	return nil
}

func TestEpicSuite(t *testing.T) {
	h := newEpicTestHelper(t)

	t.Run("MixedChildrenNotEligible", func(t *testing.T) {
		h.t = t

		epic := &types.Issue{
			ID:          "test-epic-1",
			Title:       "Test Epic",
			Description: "Epic description",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeEpic,
			CreatedAt:   time.Now(),
		}
		h.createIssue(epic)

		child1 := &types.Issue{
			Title:     "Child Task 1",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			ClosedAt:  ptrTime(time.Now()),
		}
		child2 := &types.Issue{
			Title:     "Child Task 2",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		h.createIssue(child1)
		h.createIssue(child2)

		h.addDependency(&types.Dependency{
			IssueID:     child1.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		})
		h.addDependency(&types.Dependency{
			IssueID:     child2.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		})

		store = h.s
		epicStatus := h.getEpicStatus("test-epic-1")
		if epicStatus == nil {
			t.Fatal("Epic test-epic-1 not found in results")
		}
		if epicStatus.TotalChildren != 2 {
			t.Errorf("Expected 2 total children, got %d", epicStatus.TotalChildren)
		}
		if epicStatus.ClosedChildren != 1 {
			t.Errorf("Expected 1 closed child, got %d", epicStatus.ClosedChildren)
		}
		if epicStatus.EligibleForClose {
			t.Error("Epic should not be eligible for close with open children")
		}
	})

	t.Run("OpenWispChildNotEligible", func(t *testing.T) {
		h.t = t

		epic := &types.Issue{
			ID:          "test-epic-wisp",
			Title:       "Epic with wisp child",
			Description: "Tests that wisp children are counted for closure eligibility",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeEpic,
			CreatedAt:   time.Now(),
		}
		h.createIssue(epic)

		regularChild := &types.Issue{
			Title:     "Regular child",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			ClosedAt:  ptrTime(time.Now()),
		}
		wispChild := &types.Issue{
			Title:     "Wisp child",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
			CreatedAt: time.Now(),
		}
		h.createIssue(regularChild)
		h.createIssue(wispChild)

		h.addDependency(&types.Dependency{
			IssueID:     regularChild.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		})
		h.addDependency(&types.Dependency{
			IssueID:     wispChild.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		})

		epicStatus := h.getEpicStatus("test-epic-wisp")
		if epicStatus == nil {
			t.Fatal("Epic test-epic-wisp not found in results")
		}
		if epicStatus.TotalChildren != 2 {
			t.Errorf("Expected 2 total children (1 regular + 1 wisp), got %d", epicStatus.TotalChildren)
		}
		if epicStatus.ClosedChildren != 1 {
			t.Errorf("Expected 1 closed child, got %d", epicStatus.ClosedChildren)
		}
		if epicStatus.EligibleForClose {
			t.Error("Epic should NOT be eligible for close with open wisp child")
		}
	})

	t.Run("AllChildrenClosedEligible", func(t *testing.T) {
		h.t = t

		epic := &types.Issue{
			ID:          "test-epic-2",
			Title:       "Fully Completed Epic",
			Description: "Epic description",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeEpic,
			CreatedAt:   time.Now(),
		}
		h.createIssue(epic)

		for i := 1; i <= 3; i++ {
			child := &types.Issue{
				Title:     fmt.Sprintf("Child Task %d", i),
				Status:    types.StatusClosed,
				Priority:  2,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
				ClosedAt:  ptrTime(time.Now()),
			}
			h.createIssue(child)
			h.addDependency(&types.Dependency{
				IssueID:     child.ID,
				DependsOnID: epic.ID,
				Type:        types.DepParentChild,
			})
		}

		epicStatus := h.getEpicStatus("test-epic-2")
		if epicStatus == nil {
			t.Fatal("Epic test-epic-2 not found in results")
		}
		if epicStatus.TotalChildren != 3 {
			t.Errorf("Expected 3 total children, got %d", epicStatus.TotalChildren)
		}
		if epicStatus.ClosedChildren != 3 {
			t.Errorf("Expected 3 closed children, got %d", epicStatus.ClosedChildren)
		}
		if !epicStatus.EligibleForClose {
			t.Error("Epic should be eligible for close when all children are closed")
		}
	})
}

func TestEpicCommandInit(t *testing.T) {
	if epicCmd == nil {
		t.Fatal("epicCmd should be initialized")
	}

	if epicCmd.Use != "epic" {
		t.Errorf("Expected Use='epic', got %q", epicCmd.Use)
	}

	var hasStatusCmd bool
	for _, cmd := range epicCmd.Commands() {
		if cmd.Use == "status" {
			hasStatusCmd = true
		}
	}

	if !hasStatusCmd {
		t.Error("epic command should have status subcommand")
	}
}
