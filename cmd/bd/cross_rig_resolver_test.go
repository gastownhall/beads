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

// crossRigTargetDatabase is the dolt_database the routed rig's metadata.json
// names in writeCrossRigTown.
const crossRigTargetDatabase = "rig_b_db"

// writeCrossRigTown lays out a town whose source workspace routes bb- to the
// sibling rig "rig-b", whose metadata.json names crossRigTargetDatabase, and
// aa- to itself. The resolver must not follow a GT_ROOT of the host's.
func writeCrossRigTown(t *testing.T) (sourceBeadsDir, targetBeadsDir string) {
	t.Helper()
	t.Setenv("GT_ROOT", "")
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
	writeCrossRigMetadata(t, targetBeadsDir, crossRigTargetDatabase)
	return sourceBeadsDir, targetBeadsDir
}

// writeCrossRigMetadata writes a metadata.json naming database into beadsDir.
func writeCrossRigMetadata(t *testing.T, beadsDir, database string) {
	t.Helper()
	meta := `{"backend":"dolt","dolt_database":"` + database + `"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
}

// GH#5784 AC-2: each evaluation reopens the routed rig and reads the blocker's
// current row, and closes the store before returning, so a foreign close or
// reopen is seen by the next query.
func TestCrossRigIssueResolverReadsRoutedRigAtEachCall(t *testing.T) {
	sourceBeadsDir, targetBeadsDir := writeCrossRigTown(t)
	blocker := &types.Issue{ID: "bb-1", Status: types.StatusOpen}
	opens, closes := 0, 0
	resolve := newCrossRigIssueResolver(func() string { return sourceBeadsDir }, func(_ context.Context, beadsDir, database string) (storage.DoltStorage, error) {
		if beadsDir != targetBeadsDir {
			t.Fatalf("opened %s, want the routed rig %s", beadsDir, targetBeadsDir)
		}
		if database != crossRigTargetDatabase {
			// The routed rig's own database, never the launcher's
			// BEADS_DOLT_SERVER_DATABASE an empty one would fall back to.
			t.Fatalf("opened database %q, want the routed rig's own %q", database, crossRigTargetDatabase)
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

// GH#5784: a routed rig whose metadata.json names no dolt_database is answered
// unavailable without being opened: an open with no database would fall back
// to the launching workspace's and read the wrong rig.
func TestCrossRigIssueResolverRefusesARigWithoutItsOwnDatabase(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(t *testing.T, beadsDir string)
	}{
		{"empty dolt_database", func(t *testing.T, beadsDir string) { writeCrossRigMetadata(t, beadsDir, "") }},
		{"no metadata.json", func(t *testing.T, beadsDir string) {
			if err := os.Remove(filepath.Join(beadsDir, "metadata.json")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sourceBeadsDir, targetBeadsDir := writeCrossRigTown(t)
			tc.write(t, targetBeadsDir)
			resolve := newCrossRigIssueResolver(func() string { return sourceBeadsDir }, func(context.Context, string, string) (storage.DoltStorage, error) {
				t.Fatal("opened a routed rig that names no database of its own")
				return nil, nil
			})
			found, unavailable, err := resolve(t.Context(), []string{"bb-1"})
			if err != nil || len(found) != 0 || !strings.Contains(unavailable["bb-1"], "no dolt_database configured") {
				t.Fatalf("found=%v unavailable=%v err=%v, want bb-1 unavailable naming the missing dolt_database", found, unavailable, err)
			}
		})
	}
}

// GH#5784's own layout: an orchestrator writes routes.jsonl only at its town
// root ($GT_ROOT/.beads) and none in a rig. A rig's bare cross-rig target
// resolves through the town's routes, their paths taken from the town root, as
// it does from the town root itself; without GT_ROOT the target stays
// unavailable and the reason names the rig's missing file.
func TestCrossRigIssueResolverFallsBackToTheTownRoutes(t *testing.T) {
	town := t.TempDir()
	townBeads := filepath.Join(town, ".beads")
	rigA := filepath.Join(town, "aaa", ".beads")
	rigB := filepath.Join(town, "bbb", ".beads")
	for _, dir := range []string{townBeads, rigA, rigB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	routes := `{"prefix":"hq-","path":"."}` + "\n" + `{"prefix":"aa-","path":"aaa"}` + "\n" + `{"prefix":"bb-","path":"bbb"}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCrossRigMetadata(t, rigB, "bbb_db")
	closes := 0
	var opened []string
	resolver := newCrossRigIssueResolver(func() string { return rigA }, func(_ context.Context, beadsDir, database string) (storage.DoltStorage, error) {
		opened = append(opened, beadsDir+"#"+database)
		return &crossRigTargetStore{issues: map[string]*types.Issue{"bb-1": {ID: "bb-1", Status: types.StatusOpen}}, closed: &closes}, nil
	})

	t.Setenv("GT_ROOT", town)
	found, unavailable, err := resolver(t.Context(), []string{"bb-1", "aa-404"})
	if err != nil || found["bb-1"] == nil || found["bb-1"].Status != types.StatusOpen {
		t.Fatalf("found=%v unavailable=%v err=%v, want bb-1 read from the sibling rig", found, unavailable, err)
	}
	if want := []string{rigB + "#bbb_db"}; len(opened) != 1 || opened[0] != want[0] {
		t.Fatalf("opened %v, want %v: the town route's path is relative to the town root", opened, want)
	}
	if !strings.Contains(unavailable["aa-404"], "route points to this workspace") {
		t.Fatalf("unavailable[aa-404] = %q, want the town's route back to this rig named", unavailable["aa-404"])
	}
	if !crossRigRoutesFollowable(rigA) {
		t.Fatal("crossRigRoutesFollowable(rig) = false under GT_ROOT, want the town's routes followable")
	}

	t.Setenv("GT_ROOT", "")
	opened = nil
	_, unavailable, err = resolver(t.Context(), []string{"bb-1"})
	if err != nil || len(opened) != 0 || !strings.Contains(unavailable["bb-1"], "no routes.jsonl in "+rigA) {
		t.Fatalf("without GT_ROOT: unavailable=%v opened=%v err=%v, want bb-1 unavailable naming the rig's routes.jsonl", unavailable, opened, err)
	}
	if crossRigRoutesFollowable(rigA) {
		t.Fatal("crossRigRoutesFollowable(rig) = true without routes, want false so a view skips its edge read")
	}
}
