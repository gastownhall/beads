package dolt

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// seedCountWispCorpus creates a mixed corpus spanning every cardinality trap
// from GH#4387: durable issues, a template, no_history wisps (durable work in
// the wisps table), and a true ephemeral wisp.
//
// Layout (prefix distinguishes per-test corpora on the shared branch store):
//
//	<p>-perm-task-1   issues  task  open
//	<p>-perm-task-2   issues  task  closed
//	<p>-perm-bug      issues  bug   open
//	<p>-perm-tpl      issues  task  open  is_template=1
//	<p>-nohist-task   wisps   task  open  no_history=1
//	<p>-nohist-bug    wisps   bug   open  no_history=1
//	<p>-eph-task      wisps   task  open  ephemeral=1
func seedCountWispCorpus(t *testing.T, ctx context.Context, store *DoltStore, p string) {
	t.Helper()
	now := time.Now()
	mk := func(id string, mut func(*types.Issue)) {
		t.Helper()
		issue := &types.Issue{
			ID:        p + "-" + id,
			Title:     id,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if mut != nil {
			mut(issue)
		}
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	mk("perm-task-1", nil)
	mk("perm-task-2", func(i *types.Issue) {
		i.Status = types.StatusClosed
		i.ClosedAt = &now
	})
	mk("perm-bug", func(i *types.Issue) { i.IssueType = types.TypeBug })
	mk("perm-tpl", func(i *types.Issue) { i.IsTemplate = true })
	mk("nohist-task", func(i *types.Issue) { i.NoHistory = true })
	mk("nohist-bug", func(i *types.Issue) {
		i.NoHistory = true
		i.IssueType = types.TypeBug
	})
	mk("eph-task", func(i *types.Issue) { i.Ephemeral = true })
}

// TestCountIssuesMergedMatchesSearch is the storage-side parity guard for
// GH#4387: for any filter, CountIssues with SkipWisps=false must return
// exactly len(SearchIssues) for the same filter — the merged issues+wisps
// cardinality that `bd list --include-infra` materializes.
func TestCountIssuesMergedMatchesSearch(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	seedCountWispCorpus(t, ctx, store, "cm")

	task := types.TypeTask
	noTemplate := false
	ephemeral := true

	cases := []struct {
		name   string
		filter types.IssueFilter
		want   int64
	}{
		// 4 issues + 2 no_history wisps + 1 ephemeral wisp.
		{"merged_all", types.IssueFilter{}, 7},
		// Durable tier only (today's bd count default).
		{"durable_only", types.IssueFilter{SkipWisps: true}, 4},
		// Type filter applies identically to both tables.
		{"merged_type_task", types.IssueFilter{IssueType: &task}, 5},
		// Template exclusion (bd list default) drops cm-perm-tpl.
		{"merged_no_templates", types.IssueFilter{IsTemplate: &noTemplate}, 6},
		{"merged_no_templates_task", types.IssueFilter{IsTemplate: &noTemplate, IssueType: &task}, 4},
		// Ephemeral-only routes to the wisps tier (bd list --type <infra>).
		{"ephemeral_only", types.IssueFilter{Ephemeral: &ephemeral}, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			count, err := store.CountIssues(ctx, "", tc.filter)
			if err != nil {
				t.Fatalf("CountIssues: %v", err)
			}
			if count != tc.want {
				t.Errorf("CountIssues = %d, want %d", count, tc.want)
			}

			results, err := store.SearchIssues(ctx, "", tc.filter)
			if err != nil {
				t.Fatalf("SearchIssues: %v", err)
			}
			if count != int64(len(results)) {
				t.Errorf("CountIssues = %d but SearchIssues returned %d rows; count/list parity broken (GH#4387)", count, len(results))
			}
		})
	}
}

// TestCountIssuesEphemeralFallsThroughToDurable pins the GH#4387 count/list
// parity fix for the ephemeral (infra-type) branch. SearchIssuesInTx, when an
// Ephemeral=true filter matches no wisps, falls through and searches the
// durable issues table (search.go "Fall through: wisps table doesn't exist or
// returned no results"). Before the fix CountIssuesInTx returned the wisps-only
// count and so reported 0 where list returned the durable row, breaking the
// "count == len(list) for any filter" contract. This reproduces the exact
// empty-wisps-but-durable-match state: a durable issues row carrying
// ephemeral=1 (what an infra bead that landed durable looks like), which normal
// creation never produces because it routes ephemeral/infra beads to the wisps
// table, so the row is inserted directly.
func TestCountIssuesEphemeralFallsThroughToDurable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A durable, non-ephemeral task that the Ephemeral=true filter must exclude,
	// so the test proves the filter is applied rather than counting everything.
	if err := store.CreateIssue(ctx, &types.Issue{
		ID:        "ed-task-1",
		Title:     "durable task",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("create durable task: %v", err)
	}

	// A durable issues-table row flagged ephemeral=1 with no matching wisp.
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral) "+
			"VALUES ('ed-infra-1', 'durable ephemeral', '', '', '', '', 'open', 2, 'agent', 1)"); err != nil {
		t.Fatalf("seed durable ephemeral issue: %v", err)
	}

	ephemeral := true
	filter := types.IssueFilter{Ephemeral: &ephemeral}

	results, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	count, err := store.CountIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("CountIssues: %v", err)
	}
	if count != int64(len(results)) {
		t.Errorf("CountIssues = %d but SearchIssues returned %d rows; ephemeral fall-through parity broken (GH#4387)", count, len(results))
	}
	if count != 1 {
		t.Errorf("CountIssues = %d, want 1 (durable ephemeral row must be counted via fall-through to the issues table)", count)
	}
}

// TestCountIssuesByGroupIncludesWisps verifies the grouped-count paths honor
// SkipWisps the same way CountIssues does, so `bd count --include-infra
// --by-*` reports the merged issues+wisps numbers instead of silently-wrong
// durable-only buckets (GH#4387).
func TestCountIssuesByGroupIncludesWisps(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	seedCountWispCorpus(t, ctx, store, "cg")
	if err := store.AddLabel(ctx, "cg-perm-task-1", "shared", "tester"); err != nil {
		t.Fatalf("label cg-perm-task-1: %v", err)
	}
	if err := store.AddLabel(ctx, "cg-nohist-task", "shared", "tester"); err != nil {
		t.Fatalf("label cg-nohist-task: %v", err)
	}

	t.Run("by_type_merged", func(t *testing.T) {
		counts, err := store.CountIssuesByGroup(ctx, types.IssueFilter{}, "type")
		if err != nil {
			t.Fatalf("CountIssuesByGroup: %v", err)
		}
		// task: perm-task-1, perm-task-2, perm-tpl, nohist-task, eph-task.
		if counts["task"] != 5 {
			t.Errorf("merged task count = %d, want 5 (wisps tier dropped from grouped count)", counts["task"])
		}
		// bug: perm-bug, nohist-bug.
		if counts["bug"] != 2 {
			t.Errorf("merged bug count = %d, want 2", counts["bug"])
		}
	})

	t.Run("by_type_durable_only", func(t *testing.T) {
		counts, err := store.CountIssuesByGroup(ctx, types.IssueFilter{SkipWisps: true}, "type")
		if err != nil {
			t.Fatalf("CountIssuesByGroup: %v", err)
		}
		if counts["task"] != 3 {
			t.Errorf("durable task count = %d, want 3 (SkipWisps must keep today's semantics)", counts["task"])
		}
		if counts["bug"] != 1 {
			t.Errorf("durable bug count = %d, want 1", counts["bug"])
		}
	})

	t.Run("by_status_merged", func(t *testing.T) {
		counts, err := store.CountIssuesByGroup(ctx, types.IssueFilter{}, "status")
		if err != nil {
			t.Fatalf("CountIssuesByGroup: %v", err)
		}
		if counts["open"] != 6 {
			t.Errorf("merged open count = %d, want 6", counts["open"])
		}
		if counts["closed"] != 1 {
			t.Errorf("merged closed count = %d, want 1", counts["closed"])
		}
	})

	t.Run("by_label_merged", func(t *testing.T) {
		counts, err := store.CountIssuesByGroup(ctx, types.IssueFilter{}, "label")
		if err != nil {
			t.Fatalf("CountIssuesByGroup: %v", err)
		}
		// "shared" sits on one durable issue and one no_history wisp.
		if counts["shared"] != 2 {
			t.Errorf("merged label count = %d, want 2 (wisp labels dropped)", counts["shared"])
		}
		// 5 remaining beads carry no label.
		if counts["(no labels)"] != 5 {
			t.Errorf("merged (no labels) count = %d, want 5", counts["(no labels)"])
		}
	})

	t.Run("by_type_ephemeral_only", func(t *testing.T) {
		ephemeral := true
		counts, err := store.CountIssuesByGroup(ctx, types.IssueFilter{Ephemeral: &ephemeral}, "type")
		if err != nil {
			t.Fatalf("CountIssuesByGroup: %v", err)
		}
		if counts["task"] != 1 {
			t.Errorf("ephemeral task count = %d, want 1", counts["task"])
		}
		if counts["bug"] != 0 {
			t.Errorf("ephemeral bug count = %d, want 0", counts["bug"])
		}
	})
}

// seedDurableEphemeralRow inserts the exact GH#4387 fall-through trap: a durable
// issues-table row carrying ephemeral=1 with no matching wisp. Normal creation
// never produces this because ephemeral/infra beads route to the wisps table on
// insert, so the row is written directly. Returns the durable ephemeral row id.
func seedDurableEphemeralRow(t *testing.T, ctx context.Context, store *DoltStore, p string) string {
	t.Helper()
	// A durable, non-ephemeral task that the Ephemeral=true filter must exclude,
	// so the test proves the filter is applied rather than counting everything.
	if err := store.CreateIssue(ctx, &types.Issue{
		ID:        p + "-task-1",
		Title:     "durable task",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("create durable task: %v", err)
	}
	infraID := p + "-infra-1"
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral) "+
			"VALUES ('"+infraID+"', 'durable ephemeral', '', '', '', '', 'open', 2, 'agent', 1)"); err != nil {
		t.Fatalf("seed durable ephemeral issue: %v", err)
	}
	return infraID
}

// TestCountIssuesByGroupEphemeralFallsThroughToDurable is the grouped-count
// sibling of TestCountIssuesEphemeralFallsThroughToDurable. Before the fix,
// CountIssuesByGroupInTx returned the wisps-only grouped counts for an
// Ephemeral=true filter, so `bd count --include-infra --by-type` reported empty
// buckets (sum 0) while the scalar Total fell through to the durable issues
// table and reported 1 — sum(groups) != Total, breaking GH#4387 cardinality
// parity. This pins that the grouped path falls through to the durable issues
// table the same way the scalar path does.
func TestCountIssuesByGroupEphemeralFallsThroughToDurable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	infraID := seedDurableEphemeralRow(t, ctx, store, "eg")

	ephemeral := true
	filter := types.IssueFilter{Ephemeral: &ephemeral}

	groups, err := store.CountIssuesByGroup(ctx, filter, "type")
	if err != nil {
		t.Fatalf("CountIssuesByGroup: %v", err)
	}
	count, err := store.CountIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("CountIssues: %v", err)
	}

	sum := 0
	for _, v := range groups {
		sum += v
	}
	if int64(sum) != count {
		t.Errorf("sum(CountIssuesByGroup)=%d but CountIssues=%d; grouped ephemeral fall-through parity broken (GH#4387)", sum, count)
	}
	// The durable ephemeral row has issue_type='agent'; the non-ephemeral
	// durable task must be excluded by the Ephemeral=true filter.
	if groups["agent"] != 1 {
		t.Errorf("grouped agent count = %d, want 1 (durable ephemeral row %q must fall through to the issues table)", groups["agent"], infraID)
	}
	if len(groups) != 1 {
		t.Errorf("grouped counts had %d buckets, want 1 (only the durable ephemeral agent row); groups=%v", len(groups), groups)
	}
}

// TestSearchIssuesWithCountsEphemeralFallsThroughToDurable is the
// counts-projection sibling of the same GH#4387 fix. Before the fix,
// SearchIssuesWithCountsInTx returned nil for an Ephemeral=true filter whenever
// no matching wisp existed, so `bd search --counts --include-infra` dropped a
// durable issues-table row flagged ephemeral=1 that plain SearchIssues (and
// CountIssues) still surface. This pins parity between the counts-projection
// search and the plain search for that durable-ephemeral state.
func TestSearchIssuesWithCountsEphemeralFallsThroughToDurable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	infraID := seedDurableEphemeralRow(t, ctx, store, "es")

	ephemeral := true
	filter := types.IssueFilter{Ephemeral: &ephemeral}

	withCounts, err := store.SearchIssuesWithCounts(ctx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssuesWithCounts: %v", err)
	}
	plain, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(withCounts) != len(plain) {
		t.Errorf("SearchIssuesWithCounts returned %d rows but SearchIssues returned %d; counts-projection ephemeral fall-through parity broken (GH#4387)", len(withCounts), len(plain))
	}
	if len(withCounts) != 1 {
		t.Fatalf("SearchIssuesWithCounts returned %d rows, want 1 (durable ephemeral row must fall through to the issues table)", len(withCounts))
	}
	if withCounts[0] == nil || withCounts[0].Issue == nil || withCounts[0].Issue.ID != infraID {
		t.Errorf("SearchIssuesWithCounts returned unexpected row %+v, want %q", withCounts[0], infraID)
	}
}
