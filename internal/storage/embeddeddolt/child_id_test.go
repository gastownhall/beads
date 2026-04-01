//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestGetNextChildID(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("first_child", func(t *testing.T) {
		te := newTestEnv(t, "nc")
		ctx := t.Context()

		parent := &types.Issue{
			ID:        "nc-parent",
			Title:     "Parent",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeEpic,
		}
		if err := te.store.CreateIssue(ctx, parent, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		childID, err := te.store.GetNextChildID(ctx, "nc-parent")
		if err != nil {
			t.Fatalf("GetNextChildID: %v", err)
		}
		if childID != "nc-parent.1" {
			t.Errorf("got %q, want %q", childID, "nc-parent.1")
		}
	})

	t.Run("increments", func(t *testing.T) {
		te := newTestEnv(t, "ni")
		ctx := t.Context()

		parent := &types.Issue{
			ID:        "ni-parent",
			Title:     "Parent",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeEpic,
		}
		if err := te.store.CreateIssue(ctx, parent, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		id1, err := te.store.GetNextChildID(ctx, "ni-parent")
		if err != nil {
			t.Fatalf("GetNextChildID (1): %v", err)
		}
		id2, err := te.store.GetNextChildID(ctx, "ni-parent")
		if err != nil {
			t.Fatalf("GetNextChildID (2): %v", err)
		}

		if id1 != "ni-parent.1" {
			t.Errorf("first: got %q, want %q", id1, "ni-parent.1")
		}
		if id2 != "ni-parent.2" {
			t.Errorf("second: got %q, want %q", id2, "ni-parent.2")
		}
	})

	t.Run("reconciles_existing_children", func(t *testing.T) {
		te := newTestEnv(t, "rc")
		ctx := t.Context()

		parent := &types.Issue{
			ID:        "rc-parent",
			Title:     "Parent",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeEpic,
		}
		if err := te.store.CreateIssue(ctx, parent, "tester"); err != nil {
			t.Fatalf("CreateIssue parent: %v", err)
		}

		// Simulate imported children by creating them directly.
		for i := 1; i <= 3; i++ {
			child := &types.Issue{
				ID:        fmt.Sprintf("rc-parent.%d", i),
				Title:     fmt.Sprintf("Child %d", i),
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := te.store.CreateIssue(ctx, child, "tester"); err != nil {
				t.Fatalf("CreateIssue child %d: %v", i, err)
			}
		}

		// Counter hasn't been used yet, but 3 children exist.
		// GetNextChildID should skip past them.
		childID, err := te.store.GetNextChildID(ctx, "rc-parent")
		if err != nil {
			t.Fatalf("GetNextChildID: %v", err)
		}
		if childID != "rc-parent.4" {
			t.Errorf("got %q, want %q", childID, "rc-parent.4")
		}
	})
}
