package dolt

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestIsBlockedFilterPlanIsIndexed is the sargability guard for the
// IssueFilter.IsBlocked predicate: `is_blocked = ?` must seek
// idx_issues_is_blocked(is_blocked, status) (IndexedTableAccess), not
// full-scan-and-filter, so a (status × is_blocked) count matrix stays cheap
// on the reference Dolt backend. It EXPLAINs the exact production predicate shape
// and skips rather than fails if the EXPLAIN format is unrecognizable.
func TestIsBlockedFilterPlanIsIndexed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for i := 0; i < 5; i++ {
		iss := &types.Issue{ID: "ibp-" + string(rune('a'+i)), Title: "ibp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create seed: %v", err)
		}
	}

	// The production filter emits `is_blocked = ?`; EXPLAIN the same predicate with
	// a literal so the planner shape is under test (callers bind 1 and 0).
	plan := explainPlan(t, ctx, store.db, "SELECT id FROM issues WHERE is_blocked = 1")
	if !looksLikeDoltPlan(plan) {
		t.Skipf("EXPLAIN output not in a recognized Dolt plan format, skipping sargability assertion; plan=\n%s", plan)
	}
	if !strings.Contains(plan, "IndexedTableAccess") || !strings.Contains(plan, "issues.is_blocked") {
		t.Fatalf("is_blocked predicate does not seek idx_issues_is_blocked (want IndexedTableAccess on [issues.is_blocked]) — the filter regressed to a full Table scan.\nplan:\n%s", plan)
	}
}
