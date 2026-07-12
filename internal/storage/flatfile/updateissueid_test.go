package flatfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateIssueIDSurfacesRescanFailure pins the TASKS-m1kp fix: when the
// inbound-edge rescan fails after the rename, UpdateIssueID must report the
// failure (the caller needs to retry cleanup) instead of returning success
// with dangling references. The rename itself still lands.
func TestUpdateIssueIDSurfacesRescanFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny reads")
	}
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: "ren-old", Title: "rename me"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := s.CreateIssue(ctx, &types.Issue{ID: "ren-dep", Title: "dependent"}, "tester"); err != nil {
		t.Fatalf("CreateIssue dependent: %v", err)
	}
	dep := &types.Dependency{IssueID: "ren-dep", DependsOnID: "ren-old", Type: types.DepBlocks}
	if err := s.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	depPath := filepath.Join(s.issuesDir, "ren-dep.json")
	if err := os.Chmod(depPath, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(depPath, 0o644) })

	renamed := &types.Issue{Title: "rename me"}
	if err := s.UpdateIssueID(ctx, "ren-old", "ren-new", renamed, "tester"); err == nil {
		t.Fatal("UpdateIssueID swallowed the inbound-edge rescan failure; want an error")
	}
	// The rename itself must still have landed (partial success, not rollback).
	if _, err := os.Stat(filepath.Join(s.issuesDir, "ren-new.json")); err != nil {
		t.Errorf("renamed file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.issuesDir, "ren-old.json")); !os.IsNotExist(err) {
		t.Errorf("old file still present (err=%v); issue duplicated under two IDs", err)
	}
}

// TestUpdateIssueIDRewritesInboundEdges pins the success path: an inbound
// edge pointing at the old ID follows the rename.
func TestUpdateIssueIDRewritesInboundEdges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: "rw-old", Title: "target"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := s.CreateIssue(ctx, &types.Issue{ID: "rw-dep", Title: "dependent"}, "tester"); err != nil {
		t.Fatalf("CreateIssue dependent: %v", err)
	}
	dep := &types.Dependency{IssueID: "rw-dep", DependsOnID: "rw-old", Type: types.DepBlocks}
	if err := s.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	if err := s.UpdateIssueID(ctx, "rw-old", "rw-new", &types.Issue{Title: "target"}, "tester"); err != nil {
		t.Fatalf("UpdateIssueID: %v", err)
	}
	deps, err := s.GetDependencyRecords(ctx, "rw-dep")
	if err != nil {
		t.Fatalf("GetDependencyRecords: %v", err)
	}
	if len(deps) != 1 || deps[0].DependsOnID != "rw-new" {
		t.Errorf("inbound edge = %+v, want DependsOnID rw-new", deps)
	}
}
