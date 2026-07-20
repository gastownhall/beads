//go:build cgo

package embeddeddolt_test

import (
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateMergeOps_DoltEngine smokes the in-tx merge operations on the
// embedded-Dolt reference backend: metadata edits and note appends are resolved
// against the row read inside the mutation transaction (issueops.ResolveMergeOps),
// with the store's whole-attempt retry preserving concurrent writers' keys. A
// regression there would break every `bd update --set-metadata` / `bd note` on
// a Dolt workspace.
func TestUpdateMergeOps_DoltEngine(t *testing.T) {
	te := newTestEnv(t, "mergeops")
	ctx := t.Context()

	issue := &types.Issue{
		ID:        "mergeops-1",
		Title:     "merge ops on dolt",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
		Metadata:  json.RawMessage(`{"keep":"x"}`),
		Notes:     "line1",
	}
	if err := te.store.CreateIssue(ctx, issue, "actor"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	updates := map[string]interface{}{
		issueops.OpSetMetadata: []string{"team=platform"},
		issueops.OpAppendNotes: "line2",
	}
	if err := te.store.UpdateIssue(ctx, issue.ID, updates, "actor"); err != nil {
		t.Fatalf("UpdateIssue(merge ops): %v", err)
	}

	got, err := te.store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	meta := map[string]any{}
	if err := json.Unmarshal(got.Metadata, &meta); err != nil {
		t.Fatalf("unmarshal metadata %q: %v", got.Metadata, err)
	}
	if meta["keep"] != "x" {
		t.Errorf("metadata[keep]: got %v, want %q", meta["keep"], "x")
	}
	if meta["team"] != "platform" {
		t.Errorf("metadata[team]: got %v, want %q", meta["team"], "platform")
	}
	if got.Notes != "line1\nline2" {
		t.Errorf("notes: got %q, want %q", got.Notes, "line1\nline2")
	}
}

// TestUpdateMergeOps_DoltEngine_TwoIssues pins per-issue correctness of the
// locking read when the SAME statement text runs against DIFFERENT bindings:
// issue 1 is touched first so any text-keyed statement caching in the engine
// is primed with issue 1's row, then issue 2's merge must still read issue 2.
func TestUpdateMergeOps_DoltEngine_TwoIssues(t *testing.T) {
	te := newTestEnv(t, "twoissues")
	ctx := t.Context()

	for _, spec := range []struct{ id, notes string }{
		{"two-1", "first-notes"},
		{"two-2", ""},
	} {
		iss := &types.Issue{ID: spec.id, Title: spec.id, Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2, Notes: spec.notes}
		if err := te.store.CreateIssue(ctx, iss, "actor"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", spec.id, err)
		}
	}

	if err := te.store.UpdateIssue(ctx, "two-1", map[string]interface{}{issueops.OpAppendNotes: "added-1"}, "actor"); err != nil {
		t.Fatalf("UpdateIssue(two-1): %v", err)
	}
	if err := te.store.UpdateIssue(ctx, "two-2", map[string]interface{}{issueops.OpAppendNotes: "added-2"}, "actor"); err != nil {
		t.Fatalf("UpdateIssue(two-2): %v", err)
	}

	got1, err := te.store.GetIssue(ctx, "two-1")
	if err != nil {
		t.Fatalf("GetIssue(two-1): %v", err)
	}
	got2, err := te.store.GetIssue(ctx, "two-2")
	if err != nil {
		t.Fatalf("GetIssue(two-2): %v", err)
	}
	if got1.Notes != "first-notes\nadded-1" {
		t.Errorf("two-1 notes: got %q, want %q", got1.Notes, "first-notes\nadded-1")
	}
	if got2.Notes != "added-2" {
		t.Errorf("two-2 notes: got %q, want %q", got2.Notes, "added-2")
	}
}
