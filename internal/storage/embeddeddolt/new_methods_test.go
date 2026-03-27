//go:build embeddeddolt

package embeddeddolt_test

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestSlotSetGetClear(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("set_and_get", func(t *testing.T) {
		te := newTestEnv(t, "ss")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "ss-1",
			Title:     "Slot test",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		// Set a slot value.
		if err := te.store.SlotSet(ctx, "ss-1", "branch", "feature-x", "tester"); err != nil {
			t.Fatalf("SlotSet: %v", err)
		}

		// Get it back.
		val, err := te.store.SlotGet(ctx, "ss-1", "branch")
		if err != nil {
			t.Fatalf("SlotGet: %v", err)
		}
		if val != "feature-x" {
			t.Errorf("SlotGet: got %q, want %q", val, "feature-x")
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		te := newTestEnv(t, "so")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "so-1",
			Title:     "Overwrite test",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		if err := te.store.SlotSet(ctx, "so-1", "branch", "old-val", "tester"); err != nil {
			t.Fatalf("SlotSet (first): %v", err)
		}
		if err := te.store.SlotSet(ctx, "so-1", "branch", "new-val", "tester"); err != nil {
			t.Fatalf("SlotSet (second): %v", err)
		}

		val, err := te.store.SlotGet(ctx, "so-1", "branch")
		if err != nil {
			t.Fatalf("SlotGet: %v", err)
		}
		if val != "new-val" {
			t.Errorf("SlotGet after overwrite: got %q, want %q", val, "new-val")
		}
	})

	t.Run("clear", func(t *testing.T) {
		te := newTestEnv(t, "sc")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "sc-1",
			Title:     "Clear test",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		if err := te.store.SlotSet(ctx, "sc-1", "branch", "to-clear", "tester"); err != nil {
			t.Fatalf("SlotSet: %v", err)
		}
		if err := te.store.SlotClear(ctx, "sc-1", "branch", "tester"); err != nil {
			t.Fatalf("SlotClear: %v", err)
		}

		// After clear, SlotGet should return ErrNotFound.
		_, err := te.store.SlotGet(ctx, "sc-1", "branch")
		if err == nil {
			t.Fatal("expected error after SlotClear, got nil")
		}
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("expected ErrNotFound after clear, got: %v", err)
		}
	})

	t.Run("get_nonexistent_key", func(t *testing.T) {
		te := newTestEnv(t, "sn")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "sn-1",
			Title:     "No slot test",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		_, err := te.store.SlotGet(ctx, "sn-1", "nonexistent")
		if err == nil {
			t.Fatal("expected error for nonexistent slot key, got nil")
		}
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got: %v", err)
		}
	})
}

func TestReopenIssue(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("close_then_reopen", func(t *testing.T) {
		te := newTestEnv(t, "ro")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "ro-1",
			Title:     "Reopen test",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		// Close it.
		if err := te.store.CloseIssue(ctx, "ro-1", "done", "tester", ""); err != nil {
			t.Fatalf("CloseIssue: %v", err)
		}

		// Verify it is closed.
		got, err := te.store.GetIssue(ctx, "ro-1")
		if err != nil {
			t.Fatalf("GetIssue after close: %v", err)
		}
		if got.Status != types.StatusClosed {
			t.Fatalf("expected status closed, got %q", got.Status)
		}
		if got.ClosedAt == nil {
			t.Fatal("expected ClosedAt to be set after close")
		}

		// Reopen it.
		if err := te.store.ReopenIssue(ctx, "ro-1", "not actually done", "tester"); err != nil {
			t.Fatalf("ReopenIssue: %v", err)
		}

		got, err = te.store.GetIssue(ctx, "ro-1")
		if err != nil {
			t.Fatalf("GetIssue after reopen: %v", err)
		}
		if got.Status != types.StatusOpen {
			t.Errorf("expected status open after reopen, got %q", got.Status)
		}
		if got.ClosedAt != nil {
			t.Errorf("expected ClosedAt nil after reopen, got %v", got.ClosedAt)
		}
	})
}

func TestUpdateIssueType(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("task_to_feature", func(t *testing.T) {
		te := newTestEnv(t, "ut")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "ut-1",
			Title:     "Type change test",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		if err := te.store.UpdateIssueType(ctx, "ut-1", string(types.TypeFeature), "tester"); err != nil {
			t.Fatalf("UpdateIssueType: %v", err)
		}

		got, err := te.store.GetIssue(ctx, "ut-1")
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got.IssueType != types.TypeFeature {
			t.Errorf("IssueType: got %q, want %q", got.IssueType, types.TypeFeature)
		}
	})
}

func TestListWisps(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("only_ephemeral_returned", func(t *testing.T) {
		te := newTestEnv(t, "lw")
		ctx := t.Context()

		// Create a regular (non-ephemeral) issue.
		regular := &types.Issue{
			ID:        "lw-regular",
			Title:     "Regular issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, regular, "tester"); err != nil {
			t.Fatalf("CreateIssue (regular): %v", err)
		}

		// Create an ephemeral issue (wisp).
		wisp := &types.Issue{
			ID:        "lw-wisp-1",
			Title:     "Ephemeral wisp",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if err := te.store.CreateIssue(ctx, wisp, "tester"); err != nil {
			t.Fatalf("CreateIssue (wisp): %v", err)
		}

		// ListWisps with empty filter should return only the ephemeral issue.
		wisps, err := te.store.ListWisps(ctx, types.WispFilter{})
		if err != nil {
			t.Fatalf("ListWisps: %v", err)
		}

		// Verify only the ephemeral one is in results.
		found := false
		for _, w := range wisps {
			if w.ID == "lw-regular" {
				t.Errorf("ListWisps returned non-ephemeral issue %q", w.ID)
			}
			if w.ID == "lw-wisp-1" {
				found = true
			}
		}
		if !found {
			t.Errorf("ListWisps did not return ephemeral issue lw-wisp-1; got %d results", len(wisps))
		}
	})

	t.Run("empty_when_no_wisps", func(t *testing.T) {
		te := newTestEnv(t, "le")
		ctx := t.Context()

		// Create only a regular issue.
		regular := &types.Issue{
			ID:        "le-1",
			Title:     "Regular only",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, regular, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		wisps, err := te.store.ListWisps(ctx, types.WispFilter{})
		if err != nil {
			t.Fatalf("ListWisps: %v", err)
		}
		if len(wisps) != 0 {
			t.Errorf("expected 0 wisps, got %d", len(wisps))
		}
	})
}

func TestGetReadyWorkMoleculeFilter(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("filter_by_molecule_id", func(t *testing.T) {
		te := newTestEnv(t, "gm")
		ctx := t.Context()

		// Create a molecule (parent).
		mol := &types.Issue{
			ID:        "gm-mol-1",
			Title:     "Test molecule",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeMolecule,
		}
		if err := te.store.CreateIssue(ctx, mol, "tester"); err != nil {
			t.Fatalf("CreateIssue (molecule): %v", err)
		}

		// Create child issues of this molecule.
		child1 := &types.Issue{
			ID:        "gm-child-1",
			Title:     "Child 1",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		child2 := &types.Issue{
			ID:        "gm-child-2",
			Title:     "Child 2",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, child1, "tester"); err != nil {
			t.Fatalf("CreateIssue (child1): %v", err)
		}
		if err := te.store.CreateIssue(ctx, child2, "tester"); err != nil {
			t.Fatalf("CreateIssue (child2): %v", err)
		}

		// Create an unrelated issue (not a child of the molecule).
		unrelated := &types.Issue{
			ID:        "gm-other",
			Title:     "Unrelated",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, unrelated, "tester"); err != nil {
			t.Fatalf("CreateIssue (unrelated): %v", err)
		}

		// Add parent-child dependencies: molecule -> child1, molecule -> child2.
		dep1 := &types.Dependency{
			IssueID:     "gm-child-1",
			DependsOnID: "gm-mol-1",
			Type:        types.DepParentChild,
		}
		dep2 := &types.Dependency{
			IssueID:     "gm-child-2",
			DependsOnID: "gm-mol-1",
			Type:        types.DepParentChild,
		}
		if err := te.store.AddDependency(ctx, dep1, "tester"); err != nil {
			t.Fatalf("AddDependency (child1): %v", err)
		}
		if err := te.store.AddDependency(ctx, dep2, "tester"); err != nil {
			t.Fatalf("AddDependency (child2): %v", err)
		}

		// GetReadyWork filtered by MoleculeID should return only children.
		filter := types.WorkFilter{
			MoleculeID: "gm-mol-1",
		}
		ready, err := te.store.GetReadyWork(ctx, filter)
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}

		ids := make(map[string]bool)
		for _, r := range ready {
			ids[r.ID] = true
		}

		if !ids["gm-child-1"] {
			t.Errorf("expected gm-child-1 in ready work, got %v", ids)
		}
		if !ids["gm-child-2"] {
			t.Errorf("expected gm-child-2 in ready work, got %v", ids)
		}
		if ids["gm-other"] {
			t.Errorf("unrelated issue gm-other should not appear in molecule-filtered ready work")
		}
		if ids["gm-mol-1"] {
			t.Errorf("molecule gm-mol-1 itself should not appear as ready work child")
		}
	})
}
