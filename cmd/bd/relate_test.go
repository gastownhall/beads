package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestRelateCommand tests the bd relate command functionality.
// This is a regression test for Decision 004 Phase 4 - relates-to links
// are now stored in the dependencies table.
func TestRelateCommand(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	t.Run("relate creates bidirectional link", func(t *testing.T) {
		// Create two issues
		issue1 := &types.Issue{
			ID:        "test-relate-1",
			Title:     "Issue 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		issue2 := &types.Issue{
			ID:        "test-relate-2",
			Title:     "Issue 2",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
		if err := s.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}

		// Simulate what bd relate does: add bidirectional relates-to
		dep1 := &types.Dependency{
			IssueID:     issue1.ID,
			DependsOnID: issue2.ID,
			Type:        types.DepRelatesTo,
		}
		if err := s.AddDependency(ctx, dep1, "test"); err != nil {
			t.Fatalf("AddDependency (1->2) failed: %v", err)
		}

		dep2 := &types.Dependency{
			IssueID:     issue2.ID,
			DependsOnID: issue1.ID,
			Type:        types.DepRelatesTo,
		}
		if err := s.AddDependency(ctx, dep2, "test"); err != nil {
			t.Fatalf("AddDependency (2->1) failed: %v", err)
		}

		// Verify bidirectional link exists
		deps1, err := s.GetDependenciesWithMetadata(ctx, issue1.ID)
		if err != nil {
			t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
		}
		found1to2 := false
		for _, d := range deps1 {
			if d.ID == issue2.ID && d.DependencyType == types.DepRelatesTo {
				found1to2 = true
			}
		}
		if !found1to2 {
			t.Errorf("issue1 should have relates-to link to issue2")
		}

		deps2, err := s.GetDependenciesWithMetadata(ctx, issue2.ID)
		if err != nil {
			t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
		}
		found2to1 := false
		for _, d := range deps2 {
			if d.ID == issue1.ID && d.DependencyType == types.DepRelatesTo {
				found2to1 = true
			}
		}
		if !found2to1 {
			t.Errorf("issue2 should have relates-to link to issue1")
		}
	})

	t.Run("unrelate removes bidirectional link", func(t *testing.T) {
		// Create two issues
		issue1 := &types.Issue{
			ID:        "test-unrelate-1",
			Title:     "Issue 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		issue2 := &types.Issue{
			ID:        "test-unrelate-2",
			Title:     "Issue 2",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
		if err := s.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}

		// Add bidirectional relates-to
		dep1 := &types.Dependency{
			IssueID:     issue1.ID,
			DependsOnID: issue2.ID,
			Type:        types.DepRelatesTo,
		}
		if err := s.AddDependency(ctx, dep1, "test"); err != nil {
			t.Fatalf("AddDependency (1->2) failed: %v", err)
		}
		dep2 := &types.Dependency{
			IssueID:     issue2.ID,
			DependsOnID: issue1.ID,
			Type:        types.DepRelatesTo,
		}
		if err := s.AddDependency(ctx, dep2, "test"); err != nil {
			t.Fatalf("AddDependency (2->1) failed: %v", err)
		}

		// Simulate what bd unrelate does: remove both directions
		if err := s.RemoveDependency(ctx, issue1.ID, issue2.ID, "test"); err != nil {
			t.Fatalf("RemoveDependency (1->2) failed: %v", err)
		}
		if err := s.RemoveDependency(ctx, issue2.ID, issue1.ID, "test"); err != nil {
			t.Fatalf("RemoveDependency (2->1) failed: %v", err)
		}

		// Verify links are gone
		deps1, err := s.GetDependenciesWithMetadata(ctx, issue1.ID)
		if err != nil {
			t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
		}
		for _, d := range deps1 {
			if d.ID == issue2.ID && d.DependencyType == types.DepRelatesTo {
				t.Errorf("issue1 should NOT have relates-to link to issue2 after unrelate")
			}
		}

		deps2, err := s.GetDependenciesWithMetadata(ctx, issue2.ID)
		if err != nil {
			t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
		}
		for _, d := range deps2 {
			if d.ID == issue1.ID && d.DependencyType == types.DepRelatesTo {
				t.Errorf("issue2 should NOT have relates-to link to issue1 after unrelate")
			}
		}
	})

	t.Run("relates-to does not block", func(t *testing.T) {
		// Create two issues
		issue1 := &types.Issue{
			ID:        "test-noblock-1",
			Title:     "Issue 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		issue2 := &types.Issue{
			ID:        "test-noblock-2",
			Title:     "Issue 2",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
		if err := s.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}

		// Add relates-to (not blocks)
		dep := &types.Dependency{
			IssueID:     issue1.ID,
			DependsOnID: issue2.ID,
			Type:        types.DepRelatesTo,
		}
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency failed: %v", err)
		}

		// Issue1 should NOT be blocked (relates-to doesn't block)
		blocked, err := s.GetBlockedIssues(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetBlockedIssues failed: %v", err)
		}
		for _, b := range blocked {
			if b.ID == issue1.ID {
				t.Errorf("issue1 should NOT be blocked by relates-to dependency")
			}
		}
	})
}

// TestRelateCommandInit tests that the relate and unrelate commands are properly initialized.
func TestRelateCommandInit(t *testing.T) {
	if relateCmd == nil {
		t.Fatal("relateCmd should be initialized")
	}
	if relateCmd.Use != "relate <id1> <id2>" {
		t.Errorf("Expected Use='relate <id1> <id2>', got %q", relateCmd.Use)
	}

	if unrelateCmd == nil {
		t.Fatal("unrelateCmd should be initialized")
	}
	if unrelateCmd.Use != "unrelate <id1> <id2>" {
		t.Errorf("Expected Use='unrelate <id1> <id2>', got %q", unrelateCmd.Use)
	}
}
