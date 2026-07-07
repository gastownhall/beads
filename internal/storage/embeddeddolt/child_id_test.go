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

	t.Run("wisp_parent_uses_wisp_counter", func(t *testing.T) {
		te := newTestEnv(t, "wp")
		ctx := t.Context()

		parent := &types.Issue{
			ID:        "wp-wisp-parent",
			Title:     "Wisp parent",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if err := te.store.CreateIssue(ctx, parent, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		id1, err := te.store.GetNextChildID(ctx, "wp-wisp-parent")
		if err != nil {
			t.Fatalf("GetNextChildID (1): %v", err)
		}
		id2, err := te.store.GetNextChildID(ctx, "wp-wisp-parent")
		if err != nil {
			t.Fatalf("GetNextChildID (2): %v", err)
		}
		if id1 != "wp-wisp-parent.1" {
			t.Errorf("first: got %q, want wp-wisp-parent.1", id1)
		}
		if id2 != "wp-wisp-parent.2" {
			t.Errorf("second: got %q, want wp-wisp-parent.2", id2)
		}

		// Counter row landed in wisp_child_counters, not child_counters.
		var lastChild int
		te.queryScalar(t, ctx,
			"SELECT last_child FROM wisp_child_counters WHERE parent_id = ?",
			[]any{"wp-wisp-parent"}, &lastChild)
		if lastChild != 2 {
			t.Errorf("wisp_child_counters last_child: got %d, want 2", lastChild)
		}
		var ccCount int
		te.queryScalar(t, ctx,
			"SELECT COUNT(*) FROM child_counters WHERE parent_id = ?",
			[]any{"wp-wisp-parent"}, &ccCount)
		if ccCount != 0 {
			t.Errorf("child_counters rows for wisp parent: got %d, want 0", ccCount)
		}
	})

	t.Run("wisp_parent_reconciles_existing_wisp_children", func(t *testing.T) {
		te := newTestEnv(t, "wr")
		ctx := t.Context()

		parent := &types.Issue{
			ID:        "wr-wisp-parent",
			Title:     "Wisp parent",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if err := te.store.CreateIssue(ctx, parent, "tester"); err != nil {
			t.Fatalf("CreateIssue parent: %v", err)
		}
		for _, n := range []int{3, 5} {
			child := &types.Issue{
				ID:        fmt.Sprintf("wr-wisp-parent.%d", n),
				Title:     fmt.Sprintf("Child %d", n),
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				Ephemeral: true,
			}
			if err := te.store.CreateIssue(ctx, child, "tester"); err != nil {
				t.Fatalf("CreateIssue child %d: %v", n, err)
			}
		}

		// GetNextChildID should reconcile from the wisps table and skip past 5.
		childID, err := te.store.GetNextChildID(ctx, "wr-wisp-parent")
		if err != nil {
			t.Fatalf("GetNextChildID: %v", err)
		}
		if childID != "wr-wisp-parent.6" {
			t.Errorf("got %q, want wr-wisp-parent.6", childID)
		}
	})
}
