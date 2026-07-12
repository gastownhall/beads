package flatfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// Extends the TASKS-eg5j/TASKS-c523 regression class (see loadall_test.go and
// deps.go GetDependencies) to the extensions.go bulk read paths: a missing
// issue simply produces no row (SQL IN (...) semantics), but a real I/O error
// must propagate — the SQL backends fail the whole query rather than render
// an issue with no labels/deps and comment count 0.

// unreadableIssue creates an issue and chmods its file to 0o000.
func unreadableIssue(t *testing.T, s *FlatFileStore, id string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny reads")
	}
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: id, Title: "unreadable", Labels: []string{"l1"}}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	path := filepath.Join(s.issuesDir, id+".json")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
}

func TestGetDependencyRecordsForIssuesSkipsOnlyNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: "ok-1", Title: "fine"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Missing id: no row, no error.
	recs, err := s.GetDependencyRecordsForIssues(ctx, []string{"ok-1", "gone-1"})
	if err != nil {
		t.Fatalf("GetDependencyRecordsForIssues with missing id: %v", err)
	}
	if _, ok := recs["gone-1"]; ok {
		t.Error("missing id produced a row")
	}

	unreadableIssue(t, s, "locked-1")
	if _, err := s.GetDependencyRecordsForIssues(ctx, []string{"ok-1", "locked-1"}); err == nil {
		t.Fatal("GetDependencyRecordsForIssues swallowed an I/O read error; want propagation")
	}
}

func TestGetLabelsForIssuesSkipsOnlyNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: "ok-1", Title: "fine", Labels: []string{"keep"}}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	labels, err := s.GetLabelsForIssues(ctx, []string{"ok-1", "gone-1"})
	if err != nil {
		t.Fatalf("GetLabelsForIssues with missing id: %v", err)
	}
	if _, ok := labels["gone-1"]; ok {
		t.Error("missing id produced a row")
	}

	unreadableIssue(t, s, "locked-1")
	if _, err := s.GetLabelsForIssues(ctx, []string{"ok-1", "locked-1"}); err == nil {
		t.Fatal("GetLabelsForIssues swallowed an I/O read error; want propagation")
	}
}

func TestGetCommentCountsPropagatesIOError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny reads")
	}
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: "ok-1", Title: "fine"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if _, err := s.AddIssueComment(ctx, "ok-1", "tester", "hello"); err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}

	// Issue with no comments dir counts 0 without error.
	counts, err := s.GetCommentCounts(ctx, []string{"ok-1", "gone-1"})
	if err != nil {
		t.Fatalf("GetCommentCounts: %v", err)
	}
	if counts["ok-1"] != 1 || counts["gone-1"] != 0 {
		t.Errorf("counts = %v, want ok-1:1 gone-1:0", counts)
	}

	dir := filepath.Join(s.commentsDir, "ok-1")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := s.GetCommentCounts(ctx, []string{"ok-1"}); err == nil {
		t.Fatal("GetCommentCounts swallowed an I/O read error; want propagation (count 0 misreports existing comments)")
	}
}
