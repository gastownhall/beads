package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// crossRigTargetStore is a routed workspace's store: GetIssuesByIDs answers
// from issues, and Close is counted.
type crossRigTargetStore struct {
	storage.DoltStorage
	issues map[string]*types.Issue
	closed *int
}

func (s *crossRigTargetStore) GetIssuesByIDs(_ context.Context, ids []string) ([]*types.Issue, error) {
	var out []*types.Issue
	for _, id := range ids {
		if issue, ok := s.issues[id]; ok {
			out = append(out, issue)
		}
	}
	return out, nil
}

func (s *crossRigTargetStore) Close() error {
	*s.closed++
	return nil
}

// writeCrossRigTown lays out a town whose source workspace routes bb- to the
// sibling rig "rig-b" and aa- to itself.
func writeCrossRigTown(t *testing.T) (sourceBeadsDir, targetBeadsDir string) {
	t.Helper()
	town := t.TempDir()
	sourceBeadsDir = filepath.Join(town, ".beads")
	targetBeadsDir = filepath.Join(town, "rig-b", ".beads")
	for _, dir := range []string{sourceBeadsDir, targetBeadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	routes := `{"prefix":"aa-","path":"."}` + "\n" + `{"prefix":"bb-","path":"rig-b"}` + "\n"
	if err := os.WriteFile(filepath.Join(sourceBeadsDir, "routes.jsonl"), []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}
	return sourceBeadsDir, targetBeadsDir
}

// GH#5784 AC-2: each evaluation reopens the routed rig and reads the blocker's
// current row, and closes the store before returning, so a foreign close or
// reopen is seen by the next query.
func TestCrossRigIssueResolverReadsRoutedRigAtEachCall(t *testing.T) {
	sourceBeadsDir, targetBeadsDir := writeCrossRigTown(t)
	blocker := &types.Issue{ID: "bb-1", Status: types.StatusOpen}
	opens, closes := 0, 0
	resolve := newCrossRigIssueResolver(func() string { return sourceBeadsDir }, func(_ context.Context, beadsDir, _ string) (storage.DoltStorage, error) {
		if beadsDir != targetBeadsDir {
			t.Fatalf("opened %s, want the routed rig %s", beadsDir, targetBeadsDir)
		}
		opens++
		snapshot := *blocker
		return &crossRigTargetStore{issues: map[string]*types.Issue{"bb-1": &snapshot}, closed: &closes}, nil
	})

	for i, status := range []types.Status{types.StatusOpen, types.StatusClosed, types.StatusOpen, types.StatusPinned} {
		blocker.Status = status
		found, unavailable, err := resolve(t.Context(), []string{"bb-1"})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if len(unavailable) != 0 || found["bb-1"] == nil || found["bb-1"].Status != status {
			t.Fatalf("call %d: found=%v unavailable=%v, want bb-1 %s", i, found, unavailable, status)
		}
	}
	if opens != 4 || closes != 4 {
		t.Fatalf("opens=%d closes=%d, want one open and one close per call (4)", opens, closes)
	}
}
