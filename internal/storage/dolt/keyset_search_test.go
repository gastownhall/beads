package dolt

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// ksIDs projects issue IDs in result order.
func ksIDs(issues []*types.Issue) []string {
	out := make([]string, len(issues))
	for i, iss := range issues {
		out[i] = iss.ID
	}
	return out
}

func ksEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSearchIssuesKeysetSameSecondOverflow is the same-second-overflow keyset regression: a same-second
// group LARGER than one page must page completely under the (created_at DESC,
// id ASC) keyset with no dropped or duplicated row — the exact case a
// created_at-only cursor loses. It also pins the cross-second DESC + id-ASC order
// and that the keyset resumes exactly at the boundary. Exercised through the
// public SearchIssues (SLICE stack), which routes the new IssueFilter keyset
// fields into the shared sqlbuild predicate.
func TestSearchIssuesKeysetSameSecondOverflow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// A 5-row same-second group at T, one row a second newer, one a second older.
	// Under created_at DESC, id ASC the total order is: newer, a1..a5, older.
	base := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	seeds := []struct {
		id string
		at time.Time
	}{
		{"k-newer", base.Add(time.Second)},
		{"k-a1", base}, {"k-a2", base}, {"k-a3", base}, {"k-a4", base}, {"k-a5", base},
		{"k-older", base.Add(-time.Second)},
	}
	for _, s := range seeds {
		iss := &types.Issue{ID: s.id, Title: s.id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: s.at}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", s.id, err)
		}
	}

	wantOrder := []string{"k-newer", "k-a1", "k-a2", "k-a3", "k-a4", "k-a5", "k-older"}

	// One-shot ordered read (no keyset): pins created_at DESC, id ASC.
	full, err := store.SearchIssues(ctx, "", types.IssueFilter{
		IDPrefix: "k-", SkipWisps: true, SortBy: "created", Limit: 100,
	})
	if err != nil {
		t.Fatalf("SearchIssues(full): %v", err)
	}
	if got := ksIDs(full); !ksEqual(got, wantOrder) {
		t.Fatalf("full order = %v, want %v", got, wantOrder)
	}

	// Keyset walk, page size 2 (< the 5-row same-second group). The union across
	// pages must be exactly wantOrder in order, with no id seen twice.
	const pageSize = 2
	var collected []string
	seen := map[string]bool{}
	var afterCreatedAt *time.Time
	afterID := ""
	for i := 0; i < 100; i++ {
		f := types.IssueFilter{
			IDPrefix: "k-", SkipWisps: true, SortBy: "created", Limit: pageSize,
			AfterCreatedAt: afterCreatedAt, AfterID: afterID,
		}
		page, err := store.SearchIssues(ctx, "", f)
		if err != nil {
			t.Fatalf("SearchIssues(page %d): %v", i, err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > pageSize {
			t.Fatalf("page %d size = %d, want <= %d", i, len(page), pageSize)
		}
		for _, iss := range page {
			if seen[iss.ID] {
				t.Fatalf("duplicate id %q across pages — same-second overflow was lost", iss.ID)
			}
			seen[iss.ID] = true
			collected = append(collected, iss.ID)
		}
		last := page[len(page)-1]
		at := last.CreatedAt.UTC()
		afterCreatedAt = &at
		afterID = last.ID
	}
	if !ksEqual(collected, wantOrder) {
		t.Fatalf("keyset paged order = %v, want %v (no drop/dup)", collected, wantOrder)
	}

	// Resume-at-boundary: a cursor planted in the middle of the same-second group
	// (after k-a3) yields exactly the strictly-later tail (a4, a5, older).
	afterA3 := base.UTC()
	tail, err := store.SearchIssues(ctx, "", types.IssueFilter{
		IDPrefix: "k-", SkipWisps: true, SortBy: "created", Limit: 100,
		AfterCreatedAt: &afterA3, AfterID: "k-a3",
	})
	if err != nil {
		t.Fatalf("SearchIssues(after k-a3): %v", err)
	}
	if got := ksIDs(tail); !ksEqual(got, []string{"k-a4", "k-a5", "k-older"}) {
		t.Fatalf("resume after k-a3 = %v, want [k-a4 k-a5 k-older]", got)
	}
}

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
