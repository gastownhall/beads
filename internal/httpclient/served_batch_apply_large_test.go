//go:build cgo

// Written fresh for OSS beads S5 (no bd-enterprise source copied).

package httpclient

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage/batchfixtures"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestServedBatchApplyShape356CommitsAtomically and
// TestServedBatchApplyShape356PlusAStaleGuardLeavesZeroRows are the http
// leg's half of slice S5's named trio ("a 356-item plan against a masked
// server refuses pre-dial; against the main server it commits atomically; an
// injected mid-plan failure leaves zero rows"). The masked-server half lives
// in internal/httpclient/wire (TestApplyBatchAt356ItemsWithoutTheCapabilityRefusesBeforeDialing):
// the item-count gate it exercises never looks past len(body.Items), so a
// synthetic plan there is behaviorally identical to a REAL
// batchfixtures.Shape356 plan for that one gate. These two cases are what a
// REALISTIC, heterogeneous 356-item plan (the design's primary measured
// shape: 102 creates with metadata, 136 blocks edges, 102 tracks-to-root
// edges, 16 assign updates) must still do once past that gate: commit every
// row in one transaction, or refuse the whole thing and leave none.
//
// Both drive the real in-process bd serve stack (newServedEnv), which
// unconditionally advertises issues.batchApplyLarge in its static
// behaviorCapabilities -- so neither needs the masked handshake the other
// case already covers; they are about the CONTENT this gate lets through,
// not the gate itself.

// TestServedBatchApplyShape356CommitsAtomically applies the full 356-item
// shape once and asserts the whole plan actually landed: every create's key
// bound to a minted id, and the issues table holding exactly one row per
// create on top of the pre-seeded root -- proving the client's positional
// result-decoding and key-binding checks (decodeApplyBatchResult,
// checkApplyKeyBinding) hold up at this scale, not only on the handful-of-
// items plans the rest of served_batch_apply_test.go uses.
func TestServedBatchApplyShape356CommitsAtomically(t *testing.T) {
	env := newServedEnv(t, "hbalg0")
	ctx := t.Context()

	root := &types.Issue{Title: "root", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := env.createIssue(ctx, root, "tester"); err != nil {
		t.Fatalf("seed root issue: %v", err)
	}

	applier, err := env.subject.BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}

	plan := batchfixtures.Shape356("tester", root.ID)
	wantCreates := 0
	for _, item := range plan.Items {
		if item.Kind == issueops.ItemCreate {
			wantCreates++
		}
	}

	result, err := applier.ApplyBatch(ctx, plan)
	if err != nil {
		t.Fatalf("ApplyBatch(%d items): %v", len(plan.Items), err)
	}
	if len(result.Items) != len(plan.Items) {
		t.Fatalf("got %d item results, want %d (one per requested item)", len(result.Items), len(plan.Items))
	}
	if len(result.Keys) != wantCreates {
		t.Errorf("got %d bound keys, want %d (one per named create)", len(result.Keys), wantCreates)
	}

	var createdRows int
	if err := env.queryScalar(ctx, "SELECT COUNT(*) FROM issues WHERE id != ?", []any{root.ID}, &createdRows); err != nil {
		t.Fatalf("count created rows: %v", err)
	}
	if createdRows != wantCreates {
		t.Errorf("issues table holds %d non-root row(s) after a %d-item apply, want %d (every create must have landed, none dropped or duplicated)",
			createdRows, len(plan.Items), wantCreates)
	}
}

// TestServedBatchApplyShape356PlusAStaleGuardLeavesZeroRows is the "injected
// mid-plan failure" half: the full 356-item shape, plus one more item at the
// tail guarding the PRE-EXISTING root issue with a deliberately stale
// ExpectedVersion. The root is never created or touched by any of the 356
// shape items, so this guard takes the real precondition_failed round trip
// (not the static "guard on a row this same request already wrote" refusal
// RunBatchApplyRefusesExpectedVersionOnARowAnEarlierItemCreated/Touched pin
// elsewhere) -- proving, at realistic scale, both Requirement 2 (a batch
// item's ExpectedVersion rides the strict revision token and a server-side
// precondition_failed maps back to issueops.ErrVersionMismatch via
// errors.Is) and the "never chunk" ceiling: a 357-item ALL-OR-NOTHING
// request that fails on its last item must leave NONE of the other 356
// rows behind, which a client that silently split the request into several
// smaller ones could not guarantee.
func TestServedBatchApplyShape356PlusAStaleGuardLeavesZeroRows(t *testing.T) {
	env := newServedEnv(t, "hbalg1")
	ctx := t.Context()

	root := &types.Issue{Title: "root", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := env.createIssue(ctx, root, "tester"); err != nil {
		t.Fatalf("seed root issue: %v", err)
	}

	applier, err := env.subject.BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}

	plan := batchfixtures.Shape356("tester", root.ID)
	staleVersion := int64(999999)
	plan.Items = append(plan.Items, issueops.ApplyItem{
		Kind: issueops.ItemUpdate,
		Update: &issueops.UpdateItem{
			Target:          issueops.Ref{ID: root.ID},
			Patch:           issueops.IssuePatch{Title: issueops.Field[string]{Set: true, Value: "clobbered"}},
			ExpectedVersion: &staleVersion,
		},
	})
	lastIndex := len(plan.Items) - 1

	_, err = applier.ApplyBatch(ctx, plan)
	if !errors.Is(err, issueops.ErrVersionMismatch) {
		t.Fatalf("a stale ExpectedVersion on a %d-item plan: error = %v, want ErrVersionMismatch", len(plan.Items), err)
	}
	var itemErr *issueops.ItemError
	if !errors.As(err, &itemErr) {
		t.Fatalf("error = %v, want the refusal wrapped in an *ItemError naming the item", err)
	}
	if itemErr.Index != lastIndex || itemErr.IssueID != root.ID {
		t.Errorf("ItemError = %#v, want Index %d acting on %s", itemErr, lastIndex, root.ID)
	}

	// Nothing from the 356-item shape landed: no creates, and the root's own
	// title is untouched by the refused guard.
	var createdRows int
	if err := env.queryScalar(ctx, "SELECT COUNT(*) FROM issues WHERE id != ?", []any{root.ID}, &createdRows); err != nil {
		t.Fatalf("count created rows: %v", err)
	}
	if createdRows != 0 {
		t.Errorf("issues table holds %d non-root row(s) after a refused %d-item apply, want 0 (an all-or-nothing request must leave nothing behind)",
			createdRows, len(plan.Items))
	}
	var title string
	if err := env.queryScalar(ctx, "SELECT title FROM issues WHERE id = ?", []any{root.ID}, &title); err != nil {
		t.Fatalf("read back root title: %v", err)
	}
	if title != root.Title {
		t.Errorf("root title = %q after a refused guard, want unchanged %q", title, root.Title)
	}
}
