package dolt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestRenameLabel_Basic renames a label carried by two issues and confirms
// it lands on both: old gone, new present, no merge (neither issue already
// had newLabel).
func TestRenameLabel_Basic(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a := &types.Issue{ID: "rename-basic-a", Title: "A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	b := &types.Issue{ID: "rename-basic-b", Title: "B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	for _, issue := range []*types.Issue{a, b} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	if err := store.AddLabel(ctx, a.ID, "backend", "tester"); err != nil {
		t.Fatalf("add label a: %v", err)
	}
	if err := store.AddLabel(ctx, b.ID, "backend", "tester"); err != nil {
		t.Fatalf("add label b: %v", err)
	}

	renamed, merged, ids, err := store.RenameLabel(ctx, "backend", "server", "tester")
	if err != nil {
		t.Fatalf("RenameLabel: %v", err)
	}
	if renamed != 2 {
		t.Errorf("renamed = %d, want 2", renamed)
	}
	if merged != 0 {
		t.Errorf("merged = %d, want 0", merged)
	}
	if len(ids) != 2 {
		t.Errorf("ids = %v, want 2 entries", ids)
	}

	for _, id := range []string{a.ID, b.ID} {
		labels, err := store.GetLabels(ctx, id)
		if err != nil {
			t.Fatalf("GetLabels(%s): %v", id, err)
		}
		if len(labels) != 1 || labels[0] != "server" {
			t.Errorf("GetLabels(%s) = %v, want [server]", id, labels)
		}
	}

	events, err := store.GetEvents(ctx, a.ID, 100)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	found := false
	for _, e := range events {
		if e.EventType == types.EventLabelRenamed && e.OldValue != nil && *e.OldValue == "backend" &&
			e.NewValue != nil && *e.NewValue == "server" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a label_renamed event old=backend new=server on %s, events: %+v", a.ID, events)
	}
}

// TestRenameLabel_Merge covers an issue that already carries newLabel: the
// rename must degrade to a merge (old row dropped, no duplicate-key error)
// and report that issue in merged.
func TestRenameLabel_Merge(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	onlyOld := &types.Issue{ID: "rename-merge-only-old", Title: "OnlyOld", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	both := &types.Issue{ID: "rename-merge-both", Title: "Both", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	for _, issue := range []*types.Issue{onlyOld, both} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	if err := store.AddLabel(ctx, onlyOld.ID, "wip", "tester"); err != nil {
		t.Fatalf("add label onlyOld: %v", err)
	}
	if err := store.AddLabel(ctx, both.ID, "wip", "tester"); err != nil {
		t.Fatalf("add label both/wip: %v", err)
	}
	if err := store.AddLabel(ctx, both.ID, "in-progress", "tester"); err != nil {
		t.Fatalf("add label both/in-progress: %v", err)
	}

	renamed, merged, _, err := store.RenameLabel(ctx, "wip", "in-progress", "tester")
	if err != nil {
		t.Fatalf("RenameLabel: %v", err)
	}
	if renamed != 2 {
		t.Errorf("renamed = %d, want 2", renamed)
	}
	if merged != 1 {
		t.Errorf("merged = %d, want 1", merged)
	}

	labels, err := store.GetLabels(ctx, both.ID)
	if err != nil {
		t.Fatalf("GetLabels(both): %v", err)
	}
	if len(labels) != 1 || labels[0] != "in-progress" {
		t.Errorf("GetLabels(both) = %v, want exactly [in-progress] (no duplicate, no leftover wip)", labels)
	}

	labels, err = store.GetLabels(ctx, onlyOld.ID)
	if err != nil {
		t.Fatalf("GetLabels(onlyOld): %v", err)
	}
	if len(labels) != 1 || labels[0] != "in-progress" {
		t.Errorf("GetLabels(onlyOld) = %v, want [in-progress]", labels)
	}
}

// TestRenameLabel_WispSwept confirms the wisp_labels plane is swept too, not
// only the durable labels table.
func TestRenameLabel_WispSwept(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	wisp := &types.Issue{
		ID: "rename-wisp-target", Title: "Wisp", Status: types.StatusOpen,
		Priority: 1, IssueType: types.TypeTask, Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("create wisp: %v", err)
	}
	if err := store.AddLabel(ctx, wisp.ID, "temp-tag", "tester"); err != nil {
		t.Fatalf("add label: %v", err)
	}

	renamed, merged, ids, err := store.RenameLabel(ctx, "temp-tag", "final-tag", "tester")
	if err != nil {
		t.Fatalf("RenameLabel: %v", err)
	}
	if renamed != 1 {
		t.Errorf("renamed = %d, want 1", renamed)
	}
	if merged != 0 {
		t.Errorf("merged = %d, want 0", merged)
	}
	if len(ids) != 1 || ids[0] != wisp.ID {
		t.Errorf("ids = %v, want [%s]", ids, wisp.ID)
	}

	labels, err := store.GetLabels(ctx, wisp.ID)
	if err != nil {
		t.Fatalf("GetLabels(wisp): %v", err)
	}
	if len(labels) != 1 || labels[0] != "final-tag" {
		t.Errorf("GetLabels(wisp) = %v, want [final-tag]", labels)
	}
}

// TestRenameLabel_ZeroCarrierIsHonestNoOp confirms renaming a label nothing
// carries is a clean no-op: no error, both counts 0, nothing written.
func TestRenameLabel_ZeroCarrierIsHonestNoOp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	renamed, merged, ids, err := store.RenameLabel(ctx, "never-used-label", "irrelevant", "tester")
	if err != nil {
		t.Fatalf("RenameLabel: %v", err)
	}
	if renamed != 0 || merged != 0 {
		t.Errorf("renamed=%d merged=%d, want 0, 0", renamed, merged)
	}
	if len(ids) != 0 {
		t.Errorf("ids = %v, want empty", ids)
	}
}

// TestRenameLabel_OverLongNewLabelRefused confirms the same over-length
// refusal AddLabel applies (ErrFieldTooLong) is enforced before any write,
// consistent across every label-mutating entry point.
func TestRenameLabel_OverLongNewLabelRefused(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	issue := &types.Issue{ID: "rename-overlong", Title: "OverLong", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.AddLabel(ctx, issue.ID, "short", "tester"); err != nil {
		t.Fatalf("add label: %v", err)
	}

	tooLong := make([]byte, types.MaxFieldLen+1)
	for i := range tooLong {
		tooLong[i] = 'x'
	}
	_, _, _, err := store.RenameLabel(ctx, "short", string(tooLong), "tester")
	if err == nil {
		t.Fatal("expected ErrFieldTooLong, got nil")
	}

	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	if len(labels) != 1 || labels[0] != "short" {
		t.Errorf("GetLabels = %v, want unchanged [short] (refusal must write nothing)", labels)
	}
}

// TestRenameLabel_SameNameRefused confirms that renaming a label to itself is
// refused with ErrRenameLabelSameName rather than treated as a no-op merge.
// Without the refusal, RenameLabelInTx's merge branch treats every carrier of
// oldLabel as already carrying newLabel (they are the same string): the
// INSERT IGNORE no-ops and the unconditional DELETE FROM ... WHERE label = ?
// then removes every row for that label, wiping it instead of leaving it
// alone. Covers both label planes (labels and wisp_labels) so a fix scoped to
// only one plane still fails this test.
func TestRenameLabel_SameNameRefused(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	issue := &types.Issue{ID: "rename-samename-issue", Title: "Issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := store.AddLabel(ctx, issue.ID, "dup", "tester"); err != nil {
		t.Fatalf("add label issue: %v", err)
	}

	wisp := &types.Issue{
		ID: "rename-samename-wisp", Title: "Wisp", Status: types.StatusOpen,
		Priority: 1, IssueType: types.TypeTask, Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("create wisp: %v", err)
	}
	if err := store.AddLabel(ctx, wisp.ID, "dup", "tester"); err != nil {
		t.Fatalf("add label wisp: %v", err)
	}

	renamed, merged, ids, err := store.RenameLabel(ctx, "dup", "dup", "tester")
	if !errors.Is(err, issueops.ErrRenameLabelSameName) {
		t.Fatalf("RenameLabel(same name) err = %v, want ErrRenameLabelSameName", err)
	}
	if renamed != 0 || merged != 0 || ids != nil {
		t.Errorf("RenameLabel(same name) = (%d, %d, %v), want (0, 0, nil)", renamed, merged, ids)
	}

	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetLabels(issue): %v", err)
	}
	if len(labels) != 1 || labels[0] != "dup" {
		t.Errorf("GetLabels(issue) = %v, want unchanged [dup] (self-rename must write nothing)", labels)
	}

	wispLabels, err := store.GetLabels(ctx, wisp.ID)
	if err != nil {
		t.Fatalf("GetLabels(wisp): %v", err)
	}
	if len(wispLabels) != 1 || wispLabels[0] != "dup" {
		t.Errorf("GetLabels(wisp) = %v, want unchanged [dup] (self-rename must write nothing on the wisp plane either)", wispLabels)
	}
}

// TestRenameLabel_SameNameAfterTrimRefused confirms the same-name refusal
// compares after trimming: incidental leading/trailing whitespace on one side
// must not mask what is otherwise the identical wipe-out shape.
func TestRenameLabel_SameNameAfterTrimRefused(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	issue := &types.Issue{ID: "rename-samename-trim", Title: "Issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := store.AddLabel(ctx, issue.ID, "dup", "tester"); err != nil {
		t.Fatalf("add label: %v", err)
	}

	_, _, _, err := store.RenameLabel(ctx, "dup", " dup ", "tester")
	if !errors.Is(err, issueops.ErrRenameLabelSameName) {
		t.Fatalf("RenameLabel(\"dup\", \" dup \") err = %v, want ErrRenameLabelSameName", err)
	}

	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	if len(labels) != 1 || labels[0] != "dup" {
		t.Errorf("GetLabels = %v, want unchanged [dup]", labels)
	}
}
