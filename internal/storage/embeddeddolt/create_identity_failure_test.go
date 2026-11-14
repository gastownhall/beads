//go:build cgo

package embeddeddolt_test

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// The create failure contract (bd-t4h0n7, PR #6754): when a create's
// transaction fails, the identity fields the create generated — the minted
// issue id above all — are reset on the caller's struct, because the rows
// they named were rolled back and the ids are nobody's. A caller-supplied id
// survives every failure: it was the caller's before the transaction and
// stays the caller's after it. The deterministic post-assignment failure
// lever is an over-length label: labels persist after the mint and the row
// insert, so the create has assigned its identities by the time it fails.

// TestCreateIssueResetsGeneratedIDOnFailure pins the single-create half.
func TestCreateIssueResetsGeneratedIDOnFailure(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cl")
	ctx := t.Context()

	issue := &types.Issue{
		Title:     "generated then failed",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{strings.Repeat("x", 300)},
	}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err == nil {
		t.Fatal("CreateIssue(over-length label) = nil error, want the label refusal")
	}
	if issue.ID != "" {
		t.Errorf("failed create kept the generated id %q, want it reset: the row it named was rolled back", issue.ID)
	}
}

// TestCreateIssueKeepsSuppliedIDOnFailure pins the caller-owned half: a
// supplied id is not the create's to clear.
func TestCreateIssueKeepsSuppliedIDOnFailure(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cl")
	ctx := t.Context()

	issue := &types.Issue{
		ID:        "cl-named",
		Title:     "supplied then failed",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{strings.Repeat("x", 300)},
	}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err == nil {
		t.Fatal("CreateIssue(over-length label) = nil error, want the label refusal")
	}
	if issue.ID != "cl-named" {
		t.Errorf("failed create changed the supplied id to %q, want cl-named", issue.ID)
	}
}

// TestCreateIssuesResetGeneratedIDsOnBatchFailure pins the batch half: a
// failure late in the batch (the SECOND item's label) resets the identities
// the batch generated for the EARLIER, already-minted item too — the whole
// batch rolled back, so every id it generated is a phantom.
func TestCreateIssuesResetGeneratedIDsOnBatchFailure(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cl")
	ctx := t.Context()

	first := &types.Issue{Title: "batch one", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	second := &types.Issue{
		Title:     "batch two",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{strings.Repeat("x", 300)},
	}
	if err := te.store.CreateIssues(ctx, []*types.Issue{first, second}, "tester"); err == nil {
		t.Fatal("CreateIssues(over-length label) = nil error, want the label refusal")
	}
	if first.ID != "" {
		t.Errorf("failed batch kept the earlier item's generated id %q, want it reset: the batch rolled back", first.ID)
	}
	if second.ID != "" {
		t.Errorf("failed batch kept the failing item's generated id %q, want it reset", second.ID)
	}
}
