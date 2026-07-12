package flatfile

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDeleteIssuesWispInIdsAndCascadeClosure pins the duplicate-wisp dedupe:
// a wisp W passed in ids that ALSO depends on a regular id R in the same ids
// is rediscovered by the cascade BFS. Independent oracle: exactly two issues
// exist, so a successful 'bd delete --cascade W R' must report DeletedCount=2
// (the pre-fix code reported 3, counting W twice; the SQL reference aborts
// the whole batch on the duplicate — its own bug, tracked upstream).
func TestDeleteIssuesWispInIdsAndCascadeClosure(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateIssue(ctx, &types.Issue{ID: "bd-r", Title: "regular", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}, "a"); err != nil {
		t.Fatalf("CreateIssue bd-r: %v", err)
	}
	if err := s.CreateIssue(ctx, &types.Issue{ID: "bd-w", Title: "wisp", Priority: 2, Ephemeral: true}, "a"); err != nil {
		t.Fatalf("CreateIssue bd-w: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{IssueID: "bd-w", DependsOnID: "bd-r", Type: types.DepBlocks}, "a"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// Dry-run must report the same count as the real delete.
	dry, err := s.DeleteIssues(ctx, []string{"bd-w", "bd-r"}, true, false, true)
	if err != nil {
		t.Fatalf("DeleteIssues dry-run: %v", err)
	}
	if dry.DeletedCount != 2 {
		t.Errorf("dry-run DeletedCount = %d, want 2 (only 2 issues exist)", dry.DeletedCount)
	}

	res, err := s.DeleteIssues(ctx, []string{"bd-w", "bd-r"}, true, false, false)
	if err != nil {
		t.Fatalf("DeleteIssues: %v", err)
	}
	if res.DeletedCount != 2 {
		t.Errorf("DeletedCount = %d, want 2 (only 2 issues exist)", res.DeletedCount)
	}
	for _, id := range []string{"bd-w", "bd-r"} {
		if _, err := s.GetIssue(ctx, id); err == nil {
			t.Errorf("%s still exists after delete", id)
		}
	}
}
