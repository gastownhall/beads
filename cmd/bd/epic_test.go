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
}

func newEpicTestHelper(t *testing.T) *epicTestHelper {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	return &epicTestHelper{
		s:   newTestStore(t, testDB),
		ctx: context.Background(),
	}
}

func (h *epicTestHelper) createIssue(t *testing.T, issue *types.Issue) {
	t.Helper()
	if err := h.s.CreateIssue(h.ctx, issue, "test"); err != nil {
		t.Fatal(err)
	}
}

func (h *epicTestHelper) addDependency(t *testing.T, dep *types.Dependency) {
	t.Helper()
	if err := h.s.AddDependency(h.ctx, dep, "test"); err != nil {
		t.Fatal(err)
	}
}

func (h *epicTestHelper) getEpicStatus(t *testing.T, epicID string) *types.EpicStatus {
	t.Helper()
	epics, err := h.s.GetEpicsEligibleForClosure(h.ctx)
	if err != nil {
		t.Fatalf("GetEpicsEligibleForClosure failed: %v", err)
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
		epic := &types.Issue{
			ID:          "test-epic-1",
			Title:       "Test Epic",
			Description: "Epic description",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeEpic,
			CreatedAt:   time.Now(),
		}
		h.createIssue(t, epic)

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
		h.createIssue(t, child1)
		h.createIssue(t, child2)

		h.addDependency(t, &types.Dependency{
			IssueID:     child1.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		})
		h.addDependency(t, &types.Dependency{
			IssueID:     child2.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		})

		store = h.s
		epicStatus := h.getEpicStatus(t, "test-epic-1")
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
		epic := &types.Issue{
			ID:          "test-epic-wisp",
			Title:       "Epic with wisp child",
			Description: "Tests that wisp children are counted for closure eligibility",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeEpic,
			CreatedAt:   time.Now(),
		}
		h.createIssue(t, epic)

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
		h.createIssue(t, regularChild)
		h.createIssue(t, wispChild)

		h.addDependency(t, &types.Dependency{
			IssueID:     regularChild.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		})
		h.addDependency(t, &types.Dependency{
			IssueID:     wispChild.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		})

		epicStatus := h.getEpicStatus(t, "test-epic-wisp")
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
		epic := &types.Issue{
			ID:          "test-epic-2",
			Title:       "Fully Completed Epic",
			Description: "Epic description",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeEpic,
			CreatedAt:   time.Now(),
		}
		h.createIssue(t, epic)

		for i := 1; i <= 3; i++ {
			child := &types.Issue{
				Title:     fmt.Sprintf("Child Task %d", i),
				Status:    types.StatusClosed,
				Priority:  2,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
				ClosedAt:  ptrTime(time.Now()),
			}
			h.createIssue(t, child)
			h.addDependency(t, &types.Dependency{
				IssueID:     child.ID,
				DependsOnID: epic.ID,
				Type:        types.DepParentChild,
			})
		}

		epicStatus := h.getEpicStatus(t, "test-epic-2")
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
