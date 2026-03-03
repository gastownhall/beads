//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestBurnWisps tests direct wisp deletion via burnWisps.
func TestBurnWisps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)

	t.Run("DeletesSingleWisp", func(t *testing.T) {
		wisp := &types.Issue{
			Title:     "Wisp to burn",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
			CreatedAt: time.Now(),
		}
		if err := s.CreateIssue(ctx, wisp, "test"); err != nil {
			t.Fatalf("Failed to create wisp: %v", err)
		}

		result, err := burnWisps(ctx, s, []string{wisp.ID})
		if err != nil {
			t.Fatalf("burnWisps failed: %v", err)
		}

		if result.DeletedCount != 1 {
			t.Errorf("DeletedCount = %d, want 1", result.DeletedCount)
		}
		if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != wisp.ID {
			t.Errorf("DeletedIDs = %v, want [%s]", result.DeletedIDs, wisp.ID)
		}

		// Verify wisp is actually gone
		_, err = s.GetIssue(ctx, wisp.ID)
		if err == nil {
			t.Error("Expected error getting deleted wisp, got nil")
		}
	})

	t.Run("DeletesMultipleWisps", func(t *testing.T) {
		var ids []string
		for i := 0; i < 3; i++ {
			wisp := &types.Issue{
				Title:     "Batch wisp",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				Ephemeral: true,
				CreatedAt: time.Now(),
			}
			if err := s.CreateIssue(ctx, wisp, "test"); err != nil {
				t.Fatalf("Failed to create wisp %d: %v", i, err)
			}
			ids = append(ids, wisp.ID)
		}

		result, err := burnWisps(ctx, s, ids)
		if err != nil {
			t.Fatalf("burnWisps failed: %v", err)
		}

		if result.DeletedCount != 3 {
			t.Errorf("DeletedCount = %d, want 3", result.DeletedCount)
		}
		if len(result.DeletedIDs) != 3 {
			t.Errorf("DeletedIDs length = %d, want 3", len(result.DeletedIDs))
		}
	})

	t.Run("EmptyIDs", func(t *testing.T) {
		result, err := burnWisps(ctx, s, []string{})
		if err != nil {
			t.Fatalf("burnWisps failed: %v", err)
		}

		if result.DeletedCount != 0 {
			t.Errorf("DeletedCount = %d, want 0", result.DeletedCount)
		}
	})

	t.Run("InvalidIDsContinue", func(t *testing.T) {
		// Create one valid wisp
		wisp := &types.Issue{
			Title:     "Valid wisp",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
			CreatedAt: time.Now(),
		}
		if err := s.CreateIssue(ctx, wisp, "test"); err != nil {
			t.Fatalf("Failed to create wisp: %v", err)
		}

		// Mix valid and invalid IDs — burnWisps should continue past failures
		result, err := burnWisps(ctx, s, []string{"nonexistent-id", wisp.ID})
		if err != nil {
			t.Fatalf("burnWisps failed: %v", err)
		}

		// The valid wisp should still be deleted even though the first ID failed
		if result.DeletedCount < 1 {
			t.Errorf("DeletedCount = %d, want at least 1", result.DeletedCount)
		}
	})
}

// TestAutoCloseCompletedMolecule tests that closing the last step auto-closes the parent molecule.
func TestAutoCloseCompletedMolecule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)

	t.Run("ClosesWhenAllStepsComplete", func(t *testing.T) {
		// Create molecule root
		root := &types.Issue{
			Title:     "Auto-close molecule",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeEpic,
			Labels:    []string{BeadsTemplateLabel},
			CreatedAt: time.Now(),
		}
		if err := s.CreateIssue(ctx, root, "test"); err != nil {
			t.Fatalf("Failed to create root: %v", err)
		}

		// Create two steps — one closed, one open
		step1 := &types.Issue{
			Title:     "Step 1",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		step2 := &types.Issue{
			Title:     "Step 2",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		for _, step := range []*types.Issue{step1, step2} {
			if err := s.CreateIssue(ctx, step, "test"); err != nil {
				t.Fatalf("Failed to create step: %v", err)
			}
			if err := s.AddDependency(ctx, &types.Dependency{
				IssueID:     step.ID,
				DependsOnID: root.ID,
				Type:        types.DepParentChild,
			}, "test"); err != nil {
				t.Fatalf("Failed to add parent-child dep: %v", err)
			}
		}

		// Close step2 (the last open step)
		if err := s.CloseIssue(ctx, step2.ID, "done", "test-actor", "test-session"); err != nil {
			t.Fatalf("Failed to close step2: %v", err)
		}

		// Now trigger auto-close
		autoCloseCompletedMolecule(ctx, s, step2.ID, "test-actor", "test-session")

		// Verify root is now closed
		updatedRoot, err := s.GetIssue(ctx, root.ID)
		if err != nil {
			t.Fatalf("Failed to get root: %v", err)
		}
		if updatedRoot.Status != types.StatusClosed {
			t.Errorf("Root status = %q, want %q", updatedRoot.Status, types.StatusClosed)
		}
	})

	t.Run("DoesNotCloseWhenStepsRemain", func(t *testing.T) {
		// Create molecule root
		root := &types.Issue{
			Title:     "Incomplete molecule",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeEpic,
			Labels:    []string{BeadsTemplateLabel},
			CreatedAt: time.Now(),
		}
		if err := s.CreateIssue(ctx, root, "test"); err != nil {
			t.Fatalf("Failed to create root: %v", err)
		}

		// Create two steps — one closed, one still open
		step1 := &types.Issue{
			Title:     "Closed step",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		step2 := &types.Issue{
			Title:     "Open step",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		for _, step := range []*types.Issue{step1, step2} {
			if err := s.CreateIssue(ctx, step, "test"); err != nil {
				t.Fatalf("Failed to create step: %v", err)
			}
			if err := s.AddDependency(ctx, &types.Dependency{
				IssueID:     step.ID,
				DependsOnID: root.ID,
				Type:        types.DepParentChild,
			}, "test"); err != nil {
				t.Fatalf("Failed to add parent-child dep: %v", err)
			}
		}

		// Auto-close after closing step1 — step2 still open
		autoCloseCompletedMolecule(ctx, s, step1.ID, "test-actor", "test-session")

		// Verify root is still open
		updatedRoot, err := s.GetIssue(ctx, root.ID)
		if err != nil {
			t.Fatalf("Failed to get root: %v", err)
		}
		if updatedRoot.Status != types.StatusOpen {
			t.Errorf("Root status = %q, want %q (steps still open)", updatedRoot.Status, types.StatusOpen)
		}
	})

	t.Run("NoOpForOrphanIssue", func(t *testing.T) {
		// Create an issue not part of any molecule
		orphan := &types.Issue{
			Title:     "Orphan issue",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		if err := s.CreateIssue(ctx, orphan, "test"); err != nil {
			t.Fatalf("Failed to create orphan: %v", err)
		}

		// Should not panic or error — just no-op
		autoCloseCompletedMolecule(ctx, s, orphan.ID, "test-actor", "test-session")
	})

	t.Run("NoOpForAlreadyClosedMolecule", func(t *testing.T) {
		// Create molecule root that's already closed
		root := &types.Issue{
			Title:     "Already closed molecule",
			Status:    types.StatusClosed,
			Priority:  1,
			IssueType: types.TypeEpic,
			Labels:    []string{BeadsTemplateLabel},
			CreatedAt: time.Now(),
		}
		if err := s.CreateIssue(ctx, root, "test"); err != nil {
			t.Fatalf("Failed to create root: %v", err)
		}

		step := &types.Issue{
			Title:     "Step in closed mol",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		if err := s.CreateIssue(ctx, step, "test"); err != nil {
			t.Fatalf("Failed to create step: %v", err)
		}
		if err := s.AddDependency(ctx, &types.Dependency{
			IssueID:     step.ID,
			DependsOnID: root.ID,
			Type:        types.DepParentChild,
		}, "test"); err != nil {
			t.Fatalf("Failed to add dep: %v", err)
		}

		// Should not panic — early return because root is already closed
		autoCloseCompletedMolecule(ctx, s, step.ID, "test-actor", "test-session")
	})
}

// TestFindStaleMolecules tests detection of complete-but-unclosed molecules.
func TestFindStaleMolecules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)

	// Create a stale molecule: open epic with all children closed
	staleRoot := &types.Issue{
		Title:     "Stale molecule",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		CreatedAt: time.Now(),
	}
	if err := s.CreateIssue(ctx, staleRoot, "test"); err != nil {
		t.Fatalf("Failed to create stale root: %v", err)
	}

	closedChild := &types.Issue{
		Title:     "Closed child",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}
	if err := s.CreateIssue(ctx, closedChild, "test"); err != nil {
		t.Fatalf("Failed to create closed child: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     closedChild.ID,
		DependsOnID: staleRoot.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add dep: %v", err)
	}

	// Create a non-stale molecule: open epic with open children
	activeRoot := &types.Issue{
		Title:     "Active molecule",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		CreatedAt: time.Now(),
	}
	if err := s.CreateIssue(ctx, activeRoot, "test"); err != nil {
		t.Fatalf("Failed to create active root: %v", err)
	}

	openChild := &types.Issue{
		Title:     "Open child",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}
	if err := s.CreateIssue(ctx, openChild, "test"); err != nil {
		t.Fatalf("Failed to create open child: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     openChild.ID,
		DependsOnID: activeRoot.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("Failed to add dep: %v", err)
	}

	t.Run("FindsStaleNotActive", func(t *testing.T) {
		result, err := findStaleMolecules(ctx, s, false, false, false)
		if err != nil {
			t.Fatalf("findStaleMolecules failed: %v", err)
		}

		// Should find the stale molecule
		found := false
		for _, mol := range result.StaleMolecules {
			if mol.ID == staleRoot.ID {
				found = true
				if mol.TotalChildren != 1 {
					t.Errorf("TotalChildren = %d, want 1", mol.TotalChildren)
				}
				if mol.ClosedChildren != 1 {
					t.Errorf("ClosedChildren = %d, want 1", mol.ClosedChildren)
				}
			}
			if mol.ID == activeRoot.ID {
				t.Error("Should not find active molecule as stale")
			}
		}
		if !found {
			t.Errorf("Did not find stale molecule %s in results", staleRoot.ID)
		}
	})

	t.Run("BlockingFilter", func(t *testing.T) {
		// Create an issue blocked by the stale molecule
		blockedIssue := &types.Issue{
			Title:     "Blocked by stale",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeEpic,
			CreatedAt: time.Now(),
		}
		if err := s.CreateIssue(ctx, blockedIssue, "test"); err != nil {
			t.Fatalf("Failed to create blocked issue: %v", err)
		}
		if err := s.AddDependency(ctx, &types.Dependency{
			IssueID:     blockedIssue.ID,
			DependsOnID: staleRoot.ID,
			Type:        types.DepBlocks,
		}, "test"); err != nil {
			t.Fatalf("Failed to add blocking dep: %v", err)
		}

		// With blocking filter, should find the stale molecule
		result, err := findStaleMolecules(ctx, s, true, false, false)
		if err != nil {
			t.Fatalf("findStaleMolecules (blocking) failed: %v", err)
		}

		found := false
		for _, mol := range result.StaleMolecules {
			if mol.ID == staleRoot.ID {
				found = true
				if mol.BlockingCount == 0 {
					t.Error("Expected blocking count > 0")
				}
			}
		}
		if !found {
			t.Errorf("Did not find blocking stale molecule %s", staleRoot.ID)
		}
	})

	t.Run("UnassignedFilter", func(t *testing.T) {
		// Create an assigned stale molecule
		assignedRoot := &types.Issue{
			Title:     "Assigned stale",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeEpic,
			Assignee:  "some-agent",
			CreatedAt: time.Now(),
		}
		if err := s.CreateIssue(ctx, assignedRoot, "test"); err != nil {
			t.Fatalf("Failed to create assigned root: %v", err)
		}
		assignedChild := &types.Issue{
			Title:     "Assigned child closed",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		if err := s.CreateIssue(ctx, assignedChild, "test"); err != nil {
			t.Fatalf("Failed to create assigned child: %v", err)
		}
		if err := s.AddDependency(ctx, &types.Dependency{
			IssueID:     assignedChild.ID,
			DependsOnID: assignedRoot.ID,
			Type:        types.DepParentChild,
		}, "test"); err != nil {
			t.Fatalf("Failed to add dep: %v", err)
		}

		// Unassigned filter should exclude the assigned molecule
		result, err := findStaleMolecules(ctx, s, false, true, false)
		if err != nil {
			t.Fatalf("findStaleMolecules (unassigned) failed: %v", err)
		}

		for _, mol := range result.StaleMolecules {
			if mol.ID == assignedRoot.ID {
				t.Errorf("Unassigned filter should exclude assigned molecule %s", assignedRoot.ID)
			}
		}
	})
}

// TestBuildBlockingMap tests the helper that builds the blocking relationship map.
func TestBuildBlockingMap(t *testing.T) {
	t.Parallel()

	t.Run("EmptyInput", func(t *testing.T) {
		result := buildBlockingMap(nil)
		if len(result) != 0 {
			t.Errorf("Expected empty map, got %d entries", len(result))
		}
	})

	t.Run("SingleBlocker", func(t *testing.T) {
		blocked := []*types.BlockedIssue{
			{
				Issue:          types.Issue{ID: "issue-1"},
				BlockedByCount: 1,
				BlockedBy:      []string{"blocker-1"},
			},
		}
		result := buildBlockingMap(blocked)
		if issues, ok := result["blocker-1"]; !ok {
			t.Error("Expected blocker-1 in map")
		} else if len(issues) != 1 || issues[0] != "issue-1" {
			t.Errorf("blocker-1 blocks %v, want [issue-1]", issues)
		}
	})

	t.Run("MultipleBlockers", func(t *testing.T) {
		blocked := []*types.BlockedIssue{
			{
				Issue:          types.Issue{ID: "issue-1"},
				BlockedByCount: 2,
				BlockedBy:      []string{"blocker-a", "blocker-b"},
			},
			{
				Issue:          types.Issue{ID: "issue-2"},
				BlockedByCount: 1,
				BlockedBy:      []string{"blocker-a"},
			},
		}
		result := buildBlockingMap(blocked)

		// blocker-a blocks both issue-1 and issue-2
		if issues, ok := result["blocker-a"]; !ok {
			t.Error("Expected blocker-a in map")
		} else if len(issues) != 2 {
			t.Errorf("blocker-a blocks %d issues, want 2", len(issues))
		}

		// blocker-b blocks only issue-1
		if issues, ok := result["blocker-b"]; !ok {
			t.Error("Expected blocker-b in map")
		} else if len(issues) != 1 {
			t.Errorf("blocker-b blocks %d issues, want 1", len(issues))
		}
	})
}

// TestPostCreateWritesDoltCommit tests that post-create metadata (deps, labels)
// is committed to Dolt after issue creation (GH#2009 regression test).
func TestPostCreateWritesDoltCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)

	t.Run("LabelsPersistedAfterCommit", func(t *testing.T) {
		// Create an issue
		issue := &types.Issue{
			Title:     "Issue with label",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		// Add label (post-create write)
		if err := s.AddLabel(ctx, issue.ID, "important", "test"); err != nil {
			t.Fatalf("Failed to add label: %v", err)
		}

		// Commit (like postCreateWrites does)
		if err := s.Commit(ctx, "test: post-create metadata"); err != nil {
			// Dolt may return "nothing to commit" if auto-commit is on
			t.Logf("Commit returned: %v (may be expected with auto-commit)", err)
		}

		// Verify label is retrievable
		labels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get labels: %v", err)
		}

		found := false
		for _, l := range labels {
			if l == "important" {
				found = true
			}
		}
		if !found {
			t.Errorf("Label 'important' not found after commit, got: %v", labels)
		}
	})

	t.Run("DependencyPersistedAfterCommit", func(t *testing.T) {
		// Create parent and child issues
		parent := &types.Issue{
			Title:     "Parent issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeEpic,
			CreatedAt: time.Now(),
		}
		child := &types.Issue{
			Title:     "Child issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		if err := s.CreateIssue(ctx, parent, "test"); err != nil {
			t.Fatalf("Failed to create parent: %v", err)
		}
		if err := s.CreateIssue(ctx, child, "test"); err != nil {
			t.Fatalf("Failed to create child: %v", err)
		}

		// Add dependency (post-create write)
		if err := s.AddDependency(ctx, &types.Dependency{
			IssueID:     child.ID,
			DependsOnID: parent.ID,
			Type:        types.DepParentChild,
		}, "test"); err != nil {
			t.Fatalf("Failed to add dependency: %v", err)
		}

		// Commit
		if err := s.Commit(ctx, "test: post-create dependency"); err != nil {
			t.Logf("Commit returned: %v (may be expected with auto-commit)", err)
		}

		// Verify dependency is retrievable
		deps, err := s.GetDependenciesWithMetadata(ctx, child.ID)
		if err != nil {
			t.Fatalf("Failed to get dependencies: %v", err)
		}

		found := false
		for _, dep := range deps {
			if dep.ID == parent.ID && dep.DependencyType == types.DepParentChild {
				found = true
			}
		}
		if !found {
			t.Errorf("Parent-child dependency not found after commit")
		}
	})
}
