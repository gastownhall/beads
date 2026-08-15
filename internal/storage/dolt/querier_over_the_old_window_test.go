package dolt

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// oldQueryWindow is the row bound both `bd query` routes used to put on a
// predicate query's fetch: max(3*limit, 100), which for any limit below 34 was
// the flat 100 below.
const oldQueryWindow = 100

// TestAPredicateQueryFindsAMatchBeyondTheOldWindow is the end-to-end proof of
// the defect this role shipped to fix, and it lives HERE — one backend, not the
// portable contract — because making the old window observable needs more than
// a hundred candidate rows, which would cost three times as much as a
// conformance case for arithmetic already pinned in microseconds by
// TestBuildQueryPlanLeavesAPredicateQueryUNBOUNDED.
//
// THE SHAPE OF THE OLD FAILURE, which is what the seeding reproduces. With
// --limit 1 the old code fetched 100 candidate rows and applied the predicate
// to those; a match sitting at row 101 of the query's own order was therefore
// absent from the page AND unreported by has-more. The single matching row is
// seeded at the WORST priority so the default order (priority ASC, created
// DESC, id ASC) puts it last, behind a full window of rows the predicate
// rejects.
func TestAPredicateQueryFindsAMatchBeyondTheOldWindow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	querier, err := store.Querier()
	if err != nil {
		t.Fatalf("Querier(): %v", err)
	}

	const scope = "window-scope"
	for i := 0; i < oldQueryWindow; i++ {
		seedWindowIssue(t, ctx, store, fmt.Sprintf("window-miss-%03d", i), types.TypeTask, 0, scope)
	}
	needle := "window-needle"
	seedWindowIssue(t, ctx, store, needle, types.TypeBug, 4, scope)

	limit := 1
	page, err := querier.Query(ctx, issueops.QueryRequest{
		Expression: fmt.Sprintf("(type=bug OR type=epic) AND label=%s", scope),
		Limit:      &limit,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Issue == nil || page.Items[0].ID != needle {
		t.Fatalf("query over %d candidates returned %d rows (%v), want the one match beyond the old %d-row window",
			oldQueryWindow+1, len(page.Items), windowIDs(page), oldQueryWindow)
	}
	if page.HasMore {
		t.Errorf("has_more is true with every match on the page; the verdict is about the MATCHING set")
	}
}

func seedWindowIssue(t *testing.T, ctx context.Context, store *DoltStore, id string, issueType types.IssueType, priority int, label string) {
	t.Helper()
	issue := &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  priority,
		IssueType: issueType,
		Labels:    []string{label},
	}
	if err := store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", id, err)
	}
}

func windowIDs(page issueops.IssuePage) []string {
	ids := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		if item != nil && item.Issue != nil {
			ids = append(ids, item.ID)
		}
	}
	return ids
}
