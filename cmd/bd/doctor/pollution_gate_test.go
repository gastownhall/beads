//go:build cgo

package doctor

import (
	"context"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestPollutionStoreParity pins the gate/report agreement (GH#5025): on one
// stable store, PollutionGateScan (the full doctor's Test Pollution gate) and
// a full detailed scan classified by the shared DetectTestPollution identify
// the same candidate IDs and count. The fixture carries the cases where the
// old raw-SQL gate diverged — a mixed-case title, a closed lowercase
// leftover, an ordinary title on a test-prefixed ID — plus a title that hits
// two of the gate's candidate searches (duplicate search hits must dedupe).
func TestPollutionStoreParity(t *testing.T) {
	store := newTestDoltStore(t, "ppar")
	ctx := context.Background()

	if err := store.CreateIssue(ctx, &types.Issue{Title: "Test-quality leftover", Description: "", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "test"); err != nil {
		t.Fatalf("CreateIssue(mixed-case): %v", err)
	}
	if err := store.CreateIssue(ctx, &types.Issue{Title: "test-lowercase leftover", Description: "", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "test"); err != nil {
		t.Fatalf("CreateIssue(lowercase): %v", err)
	}
	lower := &types.Issue{Title: "sample test stub", Description: "", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, lower, "test"); err != nil {
		t.Fatalf("CreateIssue(duplicate search hit): %v", err)
	}
	if err := store.CreateIssue(ctx, &types.Issue{Title: "test-driver.sh isolation broken", Description: "Real work on the test harness.", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug}, "test"); err != nil {
		t.Fatalf("CreateIssue(real work): %v", err)
	}
	// The closed row and the test-prefixed ID with an ordinary title are
	// raw SQL because neither is reachable through the create paths under
	// test (closed statuses and hand-set IDs).
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE issues SET status = 'closed' WHERE title = 'test-lowercase leftover'`); err != nil {
		t.Fatalf("close lowercase leftover: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx,
		`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"ppar-ordinary", "Ordinary titled work", "", "", "", "", "open", 2, "task",
	); err != nil {
		t.Fatalf("insert ordinary-titled row: %v", err)
	}

	// Detailed report path: a full unrestricted scan through the shared
	// classifier.
	fullScan, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	detailed, err := DetectTestPollution(ctx, store, fullScan)
	if err != nil {
		t.Fatalf("DetectTestPollution: %v", err)
	}

	// Gate path.
	gate, err := PollutionGateScan(ctx, store)
	if err != nil {
		t.Fatalf("PollutionGateScan: %v", err)
	}

	ids := func(matches []PollutionMatch) []string {
		out := make([]string, 0, len(matches))
		for _, m := range matches {
			out = append(out, m.Issue.ID)
		}
		sort.Strings(out)
		return out
	}
	got, want := ids(gate), ids(detailed)
	if len(want) == 0 {
		t.Fatalf("fixture produced no detailed matches; parity assertion would be vacuous")
	}
	if len(got) != len(want) {
		t.Errorf("gate matched %d issues (%v), detailed scan matched %d (%v)", len(got), got, len(want), want)
	} else if len(got) != 2 {
		t.Errorf("fixture matched %d issues (%v), want the mixed-case leftover and the duplicate-search-hit stub", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("gate IDs %v != detailed IDs %v", got, want)
			break
		}
	}

	// The duplicate search hit ("sample test stub" matches both the
	// "sample" and "test" searches) must appear exactly once in the gate.
	count := 0
	for _, id := range got {
		if id == lower.ID {
			count++
		}
	}
	if count != 1 {
		t.Errorf("duplicate-search-hit issue %s appeared %d times in gate results, want exactly 1", lower.ID, count)
	}
}
