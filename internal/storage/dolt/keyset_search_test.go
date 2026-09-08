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

// TestSearchIssuesPriorityKeysetPlanIsIndexed is the sargability guard for the
// second served order, and the record of a MEASUREMENT that decided against a
// migration.
//
// What it pins: the redundant `priority >= ?` leading bound keeps the priority
// keyset on an IndexedTableAccess range over idx_issues_priority. Drop that
// bound — leaving the bare OR — and Dolt has nothing to seek and full-scans.
//
// WHAT IT DOES NOT PIN, DELIBERATELY: an index-STREAMED page. The plan under
// this predicate is an index range plus a Filter and a TopN, i.e. a bounded
// per-page sort rather than a walk down a btree in the ORDER BY's own order.
// The obvious fix is a composite (priority, created_at, id) index, and it was
// measured on Dolt 2.2.0 against this exact query: BOTH the plain ascending
// composite and the mixed-direction (priority ASC, created_at DESC, id ASC)
// spelling — which Dolt's DDL accepts — leave the plan byte-identical. The
// planner keeps the single-column priority seek and the TopN in all three
// cases. A migration would therefore have bought one more btree's worth of
// write amplification, on every insert and every `bd update --priority`, for no
// measured change to any plan. It is not shipped, and this comment is why; if a
// later Dolt learns to stream the composite, the measurement to redo is this
// one.
func TestSearchIssuesPriorityKeysetPlanIsIndexed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	base := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		iss := &types.Issue{
			ID: "pkp-" + string(rune('a'+i)), Title: "pkp", Status: types.StatusOpen,
			Priority: i % 4, IssueType: types.TypeTask, CreatedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create seed: %v", err)
		}
	}

	const cur = "2023-01-01 00:00:04"
	// Single-source the guarded SQL from production: the five placeholders bind
	// priority (sargable lower bound), priority (strict), created_at (sargable
	// upper bound), created_at (strict), id (tie-break).
	pred := literalizeParams(sqlbuild.KeysetPriorityCreatedAtIDPredicate, "2", "2", "'"+cur+"'", "'"+cur+"'", "''")
	//nolint:gosec // G202: pred is a literalized production constant, no user input.
	plan := explainPlan(t, ctx, store.db, "SELECT id FROM issues WHERE "+pred+" ORDER BY priority ASC, created_at DESC, id ASC LIMIT 100")

	if !looksLikeDoltPlan(plan) {
		t.Skipf("EXPLAIN output not in a recognized Dolt plan format, skipping sargability assertion; plan=\n%s", plan)
	}
	if !strings.Contains(plan, "IndexedTableAccess") || !strings.Contains(plan, "issues.priority") {
		t.Fatalf("the priority keyset predicate does not seek idx_issues_priority (want IndexedTableAccess on [issues.priority]) — the sargable leading bound regressed to a full Table scan.\nplan:\n%s", plan)
	}
}
