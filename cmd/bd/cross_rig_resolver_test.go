package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// GH#5784 AC-3: every target the routes cannot reach is answered unavailable
// with a reason naming why, and none fails the call.
func TestCrossRigIssueResolverExplainsUnreadableTargets(t *testing.T) {
	sourceBeadsDir, targetBeadsDir := writeCrossRigTown(t)
	closes := 0
	resolve := newCrossRigIssueResolver(func() string { return sourceBeadsDir }, func(context.Context, string, string) (storage.DoltStorage, error) {
		return &crossRigTargetStore{issues: map[string]*types.Issue{}, closed: &closes}, nil
	})
	found, unavailable, err := resolve(t.Context(), []string{"cc-1", "bb-9", "aa-404"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("found = %v, want none", found)
	}
	for id, want := range map[string]string{
		"cc-1":   `no route for prefix "cc"`,
		"bb-9":   "not found in " + targetBeadsDir,
		"aa-404": "route points to this workspace",
	} {
		if !strings.Contains(unavailable[id], want) {
			t.Errorf("unavailable[%s] = %q, want it to contain %q", id, unavailable[id], want)
		}
	}

	failing := newCrossRigIssueResolver(func() string { return sourceBeadsDir }, func(context.Context, string, string) (storage.DoltStorage, error) {
		return nil, errors.New("database is locked")
	})
	_, unavailable, err = failing(t.Context(), []string{"bb-1"})
	if err != nil || !strings.Contains(unavailable["bb-1"], "database is locked") {
		t.Fatalf("open failure: unavailable=%v err=%v, want bb-1 unavailable naming the open error", unavailable, err)
	}

	noRoutes := newCrossRigIssueResolver(func() string { return t.TempDir() }, nil)
	_, unavailable, err = noRoutes(t.Context(), []string{"bb-1"})
	if err != nil || !strings.Contains(unavailable["bb-1"], "no routes.jsonl") {
		t.Fatalf("no routes: unavailable=%v err=%v, want bb-1 unavailable naming routes.jsonl", unavailable, err)
	}
}
