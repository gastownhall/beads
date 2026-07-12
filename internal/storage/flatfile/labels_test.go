package flatfile

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// SQL reference semantics: GetLabelsInTx is a bare SELECT on the labels table,
// so an unknown or just-deleted issue ID returns an empty list with no error
// on every SQL backend. Flatfile must not surface not-found instead. This
// also matches flatfile's own GetIssueComments/CountEvents, which return
// empty/0 for missing issues.
func TestGetLabelsMissingIssueReturnsEmpty(t *testing.T) {
	s := newTestStore(t)

	labels, err := s.GetLabels(ctx, "no-such-1")
	if err != nil {
		t.Fatalf("GetLabels(missing) = %v; SQL backends return empty list, nil", err)
	}
	if len(labels) != 0 {
		t.Errorf("GetLabels(missing) = %v, want empty", labels)
	}
}

// SQL reference semantics: AddLabelInTx/RemoveLabelInTx only insert/delete the
// label row and record an event — no backend touches issues.updated_at on
// label ops. Flatfile must not bump UpdatedAt either, or updated-since filters
// and sync diffing diverge per backend.
func TestLabelOpsDoNotBumpUpdatedAt(t *testing.T) {
	s := newTestStore(t)

	issue := &types.Issue{ID: "lbl-1", Title: "Label timestamps", Priority: 2}
	if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	created, err := s.GetIssue(ctx, "lbl-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	want := created.UpdatedAt

	if err := s.AddLabel(ctx, "lbl-1", "urgent", "tester"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	got, err := s.GetIssue(ctx, "lbl-1")
	if err != nil {
		t.Fatalf("GetIssue after AddLabel: %v", err)
	}
	if !got.UpdatedAt.Equal(want) {
		t.Errorf("AddLabel bumped UpdatedAt: %v -> %v; SQL backends never touch updated_at on label ops", want, got.UpdatedAt)
	}

	if err := s.RemoveLabel(ctx, "lbl-1", "urgent", "tester"); err != nil {
		t.Fatalf("RemoveLabel: %v", err)
	}
	got, err = s.GetIssue(ctx, "lbl-1")
	if err != nil {
		t.Fatalf("GetIssue after RemoveLabel: %v", err)
	}
	if !got.UpdatedAt.Equal(want) {
		t.Errorf("RemoveLabel bumped UpdatedAt: %v -> %v; SQL backends never touch updated_at on label ops", want, got.UpdatedAt)
	}
}
