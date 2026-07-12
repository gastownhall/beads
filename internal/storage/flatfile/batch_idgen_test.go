package flatfile

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// SQL-reference oracle: GenerateIssueIDInTable's candidate SELECT runs inside
// the batch transaction, so the second of two identical ID-less issues sees
// the first insert and advances the nonce — dolt imports both. The flatfile
// batch must not abort.
func TestBatchCreateIdenticalIssuesGetDistinctIDs(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetConfig(ctx, "issue_prefix", "proj"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	createdAt := mustTime(t, "2024-05-01T12:00:00Z")
	mk := func() *types.Issue {
		return &types.Issue{
			Title:       "machine-generated dupe",
			Description: "same text",
			Priority:    2,
			CreatedAt:   createdAt,
			UpdatedAt:   createdAt.Add(time.Minute),
		}
	}
	issues := []*types.Issue{mk(), mk()}
	if err := s.CreateIssues(ctx, issues, "importer"); err != nil {
		t.Fatalf("CreateIssues with identical hash inputs: %v", err)
	}
	if issues[0].ID == "" || issues[1].ID == "" {
		t.Fatalf("IDs not assigned: %q, %q", issues[0].ID, issues[1].ID)
	}
	if issues[0].ID == issues[1].ID {
		t.Fatalf("both issues got ID %q; want distinct IDs", issues[0].ID)
	}
	for _, issue := range issues {
		if _, err := s.GetIssue(ctx, issue.ID); err != nil {
			t.Errorf("GetIssue(%s): %v", issue.ID, err)
		}
	}
}
