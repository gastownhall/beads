package flatfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestLoadAllIssuesSkipsCorruptFile pins the one-bad-file-must-not-brick-the-
// workspace behavior: a file that fails to decode is skipped, live issues
// still load.
func TestLoadAllIssuesSkipsCorruptFile(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: "good-1", Title: "fine"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.issuesDir, "bad-1.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	issues, err := s.loadAllIssues()
	if err != nil {
		t.Fatalf("loadAllIssues with corrupt file: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "good-1" {
		t.Errorf("got %d issues, want just good-1", len(issues))
	}
}

// TestLoadAllIssuesPropagatesIOError pins the TASKS-c523 fix: a transient
// I/O failure (here EACCES) must NOT be downgraded to a corrupt-file skip —
// silently dropping a live issue corrupts orphan-guard and cycle-check
// decisions.
func TestLoadAllIssuesPropagatesIOError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny reads")
	}
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: "hidden-1", Title: "unreadable"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	path := filepath.Join(s.issuesDir, "hidden-1.json")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	if _, err := s.loadAllIssues(); err == nil {
		t.Fatal("loadAllIssues swallowed an I/O read error; want propagation")
	}
}
