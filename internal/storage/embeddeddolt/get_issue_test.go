package embeddeddolt_test

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestGetIssue(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("round_trip", func(t *testing.T) {
		te := newTestEnv(t, "gi")
		ctx := t.Context()

		issue := &types.Issue{
			ID:          "gi-test1",
			Title:       "Round trip test",
			Description: "A test description",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeBug,
			Assignee:    "alice",
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		got, err := te.store.GetIssue(ctx, "gi-test1")
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got.ID != "gi-test1" {
			t.Errorf("ID: got %q, want %q", got.ID, "gi-test1")
		}
		if got.Title != "Round trip test" {
			t.Errorf("Title: got %q, want %q", got.Title, "Round trip test")
		}
		if got.Description != "A test description" {
			t.Errorf("Description: got %q, want %q", got.Description, "A test description")
		}
		if got.Priority != 1 {
			t.Errorf("Priority: got %d, want 1", got.Priority)
		}
		if got.IssueType != types.TypeBug {
			t.Errorf("IssueType: got %q, want %q", got.IssueType, types.TypeBug)
		}
		if got.Assignee != "alice" {
			t.Errorf("Assignee: got %q, want %q", got.Assignee, "alice")
		}
	})

	t.Run("not_found", func(t *testing.T) {
		te := newTestEnv(t, "nf")
		ctx := t.Context()

		_, err := te.store.GetIssue(ctx, "nf-nonexistent")
		if err == nil {
			t.Fatal("expected error for non-existent issue")
		}
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got: %v", err)
		}
	})

	t.Run("includes_labels", func(t *testing.T) {
		te := newTestEnv(t, "il")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "il-labeled",
			Title:     "Labeled issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if err := te.store.AddLabel(ctx, "il-labeled", "bug", "tester"); err != nil {
			t.Fatalf("AddLabel: %v", err)
		}
		if err := te.store.AddLabel(ctx, "il-labeled", "urgent", "tester"); err != nil {
			t.Fatalf("AddLabel: %v", err)
		}
		if err := te.store.Commit(ctx, "add labels"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		got, err := te.store.GetIssue(ctx, "il-labeled")
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if len(got.Labels) != 2 {
			t.Fatalf("Labels: got %d, want 2", len(got.Labels))
		}
		// Labels should be sorted
		if got.Labels[0] != "bug" || got.Labels[1] != "urgent" {
			t.Errorf("Labels: got %v, want [bug urgent]", got.Labels)
		}
	})
}

func TestGetLabels(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("empty", func(t *testing.T) {
		te := newTestEnv(t, "gl")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "gl-nolabels",
			Title:     "No labels",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		labels, err := te.store.GetLabels(ctx, "gl-nolabels")
		if err != nil {
			t.Fatalf("GetLabels: %v", err)
		}
		if len(labels) != 0 {
			t.Errorf("expected empty labels, got %v", labels)
		}
	})

	t.Run("sorted", func(t *testing.T) {
		te := newTestEnv(t, "gs")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "gs-sorted",
			Title:     "Sorted labels",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		for _, l := range []string{"zebra", "alpha", "middle"} {
			if err := te.store.AddLabel(ctx, "gs-sorted", l, "tester"); err != nil {
				t.Fatalf("AddLabel(%s): %v", l, err)
			}
		}
		if err := te.store.Commit(ctx, "add labels"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		labels, err := te.store.GetLabels(ctx, "gs-sorted")
		if err != nil {
			t.Fatalf("GetLabels: %v", err)
		}
		want := []string{"alpha", "middle", "zebra"}
		if len(labels) != len(want) {
			t.Fatalf("got %v, want %v", labels, want)
		}
		for i, l := range labels {
			if l != want[i] {
				t.Errorf("labels[%d]: got %q, want %q", i, l, want[i])
			}
		}
	})
}

func TestAddLabel(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("idempotent", func(t *testing.T) {
		te := newTestEnv(t, "al")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "al-idem",
			Title:     "Idempotent label",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		// Add same label twice — should not error.
		if err := te.store.AddLabel(ctx, "al-idem", "dup", "tester"); err != nil {
			t.Fatalf("AddLabel (first): %v", err)
		}
		if err := te.store.AddLabel(ctx, "al-idem", "dup", "tester"); err != nil {
			t.Fatalf("AddLabel (second): %v", err)
		}
		if err := te.store.Commit(ctx, "add labels"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		labels, err := te.store.GetLabels(ctx, "al-idem")
		if err != nil {
			t.Fatalf("GetLabels: %v", err)
		}
		if len(labels) != 1 {
			t.Errorf("expected 1 label, got %v", labels)
		}
	})
}
