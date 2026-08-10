package dolt

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// The observable keyset behavior this file used to assert now belongs to the
// shared suite: backend/conformance's RunSearchPaging owns the same-second
// overflow walk, the boundary resume, and filter composition, and this backend
// runs it through TestConformance. What remains here is Dolt PLANNER residue,
// which is outside the observable contract and cannot move.

// TestSearchIssuesKeysetPlanIsIndexed is the sargability regression guard: the
// keyset predicate must seek idx_issues_created_at (IndexedTableAccess), not
// full-scan-and-filter. The redundant `created_at <= ?` leading bound is what
// keeps the Dolt planner on the index. It EXPLAINs the exact production predicate
// (single-sourced from sqlbuild.KeysetCreatedAtIDPredicate) with literals, and
// skips rather than fails if the EXPLAIN format is unrecognizable.
func TestSearchIssuesKeysetPlanIsIndexed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	base := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		iss := &types.Issue{ID: "kp-" + string(rune('a'+i)), Title: "kp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: base.Add(time.Duration(i) * time.Second)}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create seed: %v", err)
		}
	}

	const cur = "2023-01-01 00:00:00"
	// Single-source the guarded SQL from production: the three placeholders bind
	// created_at (sargable upper bound), created_at (strict), id (tie-break).
	pred := literalizeParams(sqlbuild.KeysetCreatedAtIDPredicate, "'"+cur+"'", "'"+cur+"'", "''")
	//nolint:gosec // G202: pred is a literalized production constant, no user input.
	plan := explainPlan(t, ctx, store.db, "SELECT id FROM issues WHERE "+pred+" ORDER BY created_at DESC, id ASC LIMIT 100")

	if !looksLikeDoltPlan(plan) {
		t.Skipf("EXPLAIN output not in a recognized Dolt plan format, skipping sargability assertion; plan=\n%s", plan)
	}
	if !strings.Contains(plan, "IndexedTableAccess") || !strings.Contains(plan, "issues.created_at") {
		t.Fatalf("keyset predicate does not seek idx_issues_created_at (want IndexedTableAccess on [issues.created_at]) — the sargable upper bound regressed to a full Table scan.\nplan:\n%s", plan)
	}
}
