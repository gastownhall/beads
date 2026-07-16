package flatfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// Oracle: sqlkit wraps issueops.DeleteIssuesInTx in withMutationTx, so a
// mid-batch failure deletes NOTHING — no half-deleted batch, no survivors
// with dangling edges, no half-removed child rows.

// blockCommentsDir makes an issue's comments directory unreadable so
// removeIssueChildFiles fails mid-batch.
func blockCommentsDir(t *testing.T, s *FlatFileStore, id string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny reads")
	}
	dir := filepath.Join(s.commentsDir, id)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func TestDeleteIssuesMidBatchFailureRollsBack(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seedIssueWithChildren(t, s, "bd-a", "")
	seedIssueWithChildren(t, s, "bd-b", "")
	// A survivor with an edge onto bd-a: after a failed batch it must not be
	// left pointing at a deleted issue.
	if err := s.CreateIssue(ctx, &types.Issue{ID: "bd-c", Title: "survivor", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}, "a"); err != nil {
		t.Fatalf("CreateIssue bd-c: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{IssueID: "bd-c", DependsOnID: "bd-a", Type: types.DepBlocks}, "a"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// bd-a deletes first (input order), then bd-b's child cleanup fails.
	blockCommentsDir(t, s, "bd-b")
	if _, err := s.DeleteIssues(ctx, []string{"bd-a", "bd-b"}, false, true, false); err == nil {
		t.Fatal("DeleteIssues succeeded despite unreadable comments dir; want error")
	}

	// SQL deletes nothing in this failure: both issues and all their child
	// rows must survive.
	_ = os.Chmod(filepath.Join(s.commentsDir, "bd-b"), 0o755)
	for _, id := range []string{"bd-a", "bd-b"} {
		if _, err := s.GetIssue(ctx, id); err != nil {
			t.Errorf("issue %s lost by failed batch delete: %v", id, err)
		}
		assertChildren(t, s, id, true)
	}
	deps, err := s.GetDependencyRecords(ctx, "bd-c")
	if err != nil {
		t.Fatalf("GetDependencyRecords: %v", err)
	}
	if len(deps) != 1 || deps[0].DependsOnID != "bd-a" {
		t.Errorf("survivor edges = %+v, want the bd-c->bd-a edge intact", deps)
	}
}

func TestDeleteIssuesBySourceRepoMidBatchFailureRollsBack(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seedIssueWithChildren(t, s, "bd-a", "ext-repo")
	seedIssueWithChildren(t, s, "bd-b", "ext-repo")

	// bd-b's child cleanup fails whichever way the scan orders the pair; on
	// either order the rollback must leave BOTH issues fully intact.
	blockCommentsDir(t, s, "bd-b")

	n, err := s.DeleteIssuesBySourceRepo(ctx, "ext-repo")
	if err == nil {
		t.Fatal("DeleteIssuesBySourceRepo succeeded despite unreadable comments dir; want error")
	}
	if n != 0 {
		t.Errorf("deleted count on failure = %d, want 0 (SQL tx deletes nothing)", n)
	}

	_ = os.Chmod(filepath.Join(s.commentsDir, "bd-b"), 0o755)
	for _, id := range []string{"bd-a", "bd-b"} {
		if _, err := s.GetIssue(ctx, id); err != nil {
			t.Errorf("issue %s lost by failed source-repo delete: %v", id, err)
		}
		assertChildren(t, s, id, true)
	}
}
