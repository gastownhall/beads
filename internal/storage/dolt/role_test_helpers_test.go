package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// This file holds the test helpers the issueops role tests share. They live
// here, in a file that owns no test of its own, because the per-role duplicate
// files that used to define them are being folded into the shared conformance
// contracts one role at a time: a helper defined inside a file that is about to
// be deleted takes its siblings' compilation with it.

// seedIssues creates durable issues with the given ids, in one batch.
func seedIssues(ctx context.Context, t *testing.T, store *DoltStore, ids ...string) {
	t.Helper()
	issues := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		issues = append(issues, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask})
	}
	if err := store.CreateIssues(ctx, issues, "seed"); err != nil {
		t.Fatalf("seed issues %v: %v", ids, err)
	}
}

// skipKnownDivergence parks one conformance case on the backend that disagrees
// with the leaf contract's doc comment.
//
// The contract case asserts what the doc promises, so a genuine disagreement is
// a behaviour-unification decision for the owner rather than something a test
// slice may settle by weakening the assertion. Parking at the WIRING site (never
// inside the shared Run function) keeps the case running and passing on the
// backends that agree, so their behaviour is pinned the day the divergence is
// found. The "KNOWN DIVERGENCE" prefix is literal so `grep -r "KNOWN DIVERGENCE"`
// finds every parked case, and beadID names the child of bd-yby99 that records
// the three-way observed behaviour.
func skipKnownDivergence(t *testing.T, beadID, reason string) {
	t.Helper()
	t.Skipf("KNOWN DIVERGENCE %s: %s", beadID, reason)
}
