//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestRelateCommand tests the bd relate command functionality.
// This is a regression test for Decision 004 Phase 4 - relates-to links
// are now stored in the dependencies table.
func TestRelateCommand(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	t.Run("relate creates bidirectional link", func(t *testing.T) {
		// Create two issues
		issue1 := &types.Issue{
			ID:        "test-relate-1",
			Title:     "Issue 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		issue2 := &types.Issue{
			ID:        "test-relate-2",
			Title:     "Issue 2",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
		if err := s.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}

		// Simulate what bd relate does: add bidirectional relates-to
		dep1 := &types.Dependency{
			IssueID:     issue1.ID,
			DependsOnID: issue2.ID,
			Type:        types.DepRelatesTo,
		}
		if err := s.AddDependency(ctx, dep1, "test"); err != nil {
			t.Fatalf("AddDependency (1->2) failed: %v", err)
		}

		dep2 := &types.Dependency{
			IssueID:     issue2.ID,
			DependsOnID: issue1.ID,
			Type:        types.DepRelatesTo,
		}
		if err := s.AddDependency(ctx, dep2, "test"); err != nil {
			t.Fatalf("AddDependency (2->1) failed: %v", err)
		}

		// Verify bidirectional link exists
		deps1, err := s.GetDependenciesWithMetadata(ctx, issue1.ID)
		if err != nil {
			t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
		}
		found1to2 := false
		for _, d := range deps1 {
			if d.ID == issue2.ID && d.DependencyType == types.DepRelatesTo {
				found1to2 = true
			}
		}
		if !found1to2 {
			t.Errorf("issue1 should have relates-to link to issue2")
		}

		deps2, err := s.GetDependenciesWithMetadata(ctx, issue2.ID)
		if err != nil {
			t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
		}
		found2to1 := false
		for _, d := range deps2 {
			if d.ID == issue1.ID && d.DependencyType == types.DepRelatesTo {
				found2to1 = true
			}
		}
		if !found2to1 {
			t.Errorf("issue2 should have relates-to link to issue1")
		}
	})

	t.Run("unrelate removes bidirectional link", func(t *testing.T) {
		// Create two issues
		issue1 := &types.Issue{
			ID:        "test-unrelate-1",
			Title:     "Issue 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		issue2 := &types.Issue{
			ID:        "test-unrelate-2",
			Title:     "Issue 2",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
		if err := s.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}

		// Add bidirectional relates-to
		dep1 := &types.Dependency{
			IssueID:     issue1.ID,
			DependsOnID: issue2.ID,
			Type:        types.DepRelatesTo,
		}
		if err := s.AddDependency(ctx, dep1, "test"); err != nil {
			t.Fatalf("AddDependency (1->2) failed: %v", err)
		}
		dep2 := &types.Dependency{
			IssueID:     issue2.ID,
			DependsOnID: issue1.ID,
			Type:        types.DepRelatesTo,
		}
		if err := s.AddDependency(ctx, dep2, "test"); err != nil {
			t.Fatalf("AddDependency (2->1) failed: %v", err)
		}

		// Simulate what bd unrelate does: remove both directions
		if err := s.RemoveDependency(ctx, issue1.ID, issue2.ID, "test"); err != nil {
			t.Fatalf("RemoveDependency (1->2) failed: %v", err)
		}
		if err := s.RemoveDependency(ctx, issue2.ID, issue1.ID, "test"); err != nil {
			t.Fatalf("RemoveDependency (2->1) failed: %v", err)
		}

		// Verify links are gone
		deps1, err := s.GetDependenciesWithMetadata(ctx, issue1.ID)
		if err != nil {
			t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
		}
		for _, d := range deps1 {
			if d.ID == issue2.ID && d.DependencyType == types.DepRelatesTo {
				t.Errorf("issue1 should NOT have relates-to link to issue2 after unrelate")
			}
		}

		deps2, err := s.GetDependenciesWithMetadata(ctx, issue2.ID)
		if err != nil {
			t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
		}
		for _, d := range deps2 {
			if d.ID == issue1.ID && d.DependencyType == types.DepRelatesTo {
				t.Errorf("issue2 should NOT have relates-to link to issue1 after unrelate")
			}
		}
	})

	t.Run("relates-to does not block", func(t *testing.T) {
		// Create two issues
		issue1 := &types.Issue{
			ID:        "test-noblock-1",
			Title:     "Issue 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		issue2 := &types.Issue{
			ID:        "test-noblock-2",
			Title:     "Issue 2",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
		if err := s.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}

		// Add relates-to (not blocks)
		dep := &types.Dependency{
			IssueID:     issue1.ID,
			DependsOnID: issue2.ID,
			Type:        types.DepRelatesTo,
		}
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency failed: %v", err)
		}

		// Issue1 should NOT be blocked (relates-to doesn't block)
		blocked, err := s.GetBlockedIssues(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetBlockedIssues failed: %v", err)
		}
		for _, b := range blocked {
			if b.ID == issue1.ID {
				t.Errorf("issue1 should NOT be blocked by relates-to dependency")
			}
		}
	})
}

// TestRelateCommandInit tests that the relate and unrelate commands are properly initialized.
func TestRelateCommandInit(t *testing.T) {
	if relateCmd == nil {
		t.Fatal("relateCmd should be initialized")
	}
	if relateCmd.Use != "relate <id1> <id2>" {
		t.Errorf("Expected Use='relate <id1> <id2>', got %q", relateCmd.Use)
	}

	if unrelateCmd == nil {
		t.Fatal("unrelateCmd should be initialized")
	}
	if unrelateCmd.Use != "unrelate <id1> <id2>" {
		t.Errorf("Expected Use='unrelate <id1> <id2>', got %q", unrelateCmd.Use)
	}
}

// crossPrefixRelateEnv is the two-rig routing layout the bd-un3t5j (GH#4647)
// relate tests use: a town store (prefix hq-) at <tmp>/.beads whose
// routes.jsonl routes the gt- prefix to <tmp>/rig, a rig store (prefix gt-)
// holding one issue, and the CLI globals (dbPath, store, rootCtx) and the
// working directory pointed at the town so the route's relative path
// resolves. The rig store is closed after seeding: prefix routing reopens its
// database by path, and two handles on one database answer each other's
// lookups as misses.
//
// The tests that use this helper assign globals and os.Chdir, so none of them
// can run in parallel.
type crossPrefixRelateEnv struct {
	ctx           context.Context
	townRoot      string
	townBeadsDir  string
	rigBeadsDir   string
	townDBPath    string
	townStore     *dolt.DoltStore
	townReadonly  storage.DoltStorage // fresh read-only open of the town database
	rigReadonlyID string              // "gt-target1"
}

func setupCrossPrefixRelateEnv(t *testing.T) *crossPrefixRelateEnv {
	t.Helper()

	ctx := context.Background()
	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigBeadsDir := filepath.Join(townRoot, "rig", ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("create town beads dir: %v", err)
	}
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("create rig beads dir: %v", err)
	}

	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "hq")
	if err := townStore.CreateIssue(ctx, &types.Issue{
		ID:        "hq-src1",
		Title:     "Town source",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create town issue: %v", err)
	}

	rigStore := newTestStoreIsolatedDB(t, filepath.Join(rigBeadsDir, "dolt"), "gt")
	if err := rigStore.CreateIssue(ctx, &types.Issue{
		ID:        "gt-target1",
		Title:     "Routed relate target",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create rig issue: %v", err)
	}
	// Release the rig store before routing reopens its database.
	rigStore.Close()

	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"),
		[]byte("{\"prefix\":\"gt-\",\"path\":\"rig\"}\n"), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	oldDBPath, oldStore, oldRootCtx := dbPath, store, rootCtx
	dbPath, store, rootCtx = townDBPath, townStore, ctx
	t.Cleanup(func() { dbPath, store, rootCtx = oldDBPath, oldStore, oldRootCtx })

	// The dependency verbs validate a non-empty actor; the raw test binary
	// never ran the root pre-run that resolves one.
	oldActor := currentActor()
	setActor("test")
	t.Cleanup(func() { setActor(oldActor) })

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir to town root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	return &crossPrefixRelateEnv{
		ctx:          ctx,
		townRoot:     townRoot,
		townBeadsDir: townBeadsDir,
		rigBeadsDir:  rigBeadsDir,
		townDBPath:   townDBPath,
		townStore:    townStore,
	}
}

// reopenTownReadonly opens a second, read-only handle on the town database so
// durability is asserted from a connection the writes never used.
func (e *crossPrefixRelateEnv) reopenTownReadonly(t *testing.T) storage.DoltStorage {
	t.Helper()
	ro, err := openForeignStore(context.Background(), e.townBeadsDir, foreignRead)
	if err != nil {
		t.Fatalf("reopen town store read-only: %v", err)
	}
	t.Cleanup(func() { _ = ro.Close() })
	return ro
}

// openRigReadonly resolves a routed rig issue id (the read-only prefix-route
// sequence bd dep add's resolver uses) and returns the routed store it lives
// in, closed at cleanup.
func (e *crossPrefixRelateEnv) openRigReadonly(t *testing.T, id string) storage.DoltStorage {
	t.Helper()
	resolved, routedStore, cleanup, err := resolveIDWithRouting(e.ctx, e.townStore, id)
	if err != nil {
		t.Fatalf("routed read of %s failed: %v", id, err)
	}
	if resolved != id {
		t.Fatalf("routed read resolved %s to %q, want the exact id", id, resolved)
	}
	t.Cleanup(cleanup)
	return routedStore
}

func findRelatesToEdge(deps []*types.Dependency, from, to string) *types.Dependency {
	for _, d := range deps {
		if d != nil && d.DependsOnID == to && d.Type == types.DepRelatesTo && d.IssueID == from {
			return d
		}
	}
	return nil
}

// assertRelatesToEdge asserts st durably holds the directed relates-to edge
// from -> to, and reports the records it found when it does not.
func assertRelatesToEdge(t *testing.T, ctx context.Context, st storage.DoltStorage, from, to string) {
	t.Helper()
	deps, err := st.GetDependencyRecords(ctx, from)
	if err != nil {
		t.Fatalf("GetDependencyRecords(%s): %v", from, err)
	}
	if findRelatesToEdge(deps, from, to) == nil {
		t.Errorf("store holds no relates-to edge %s -> %s; records: %+v", from, to, deps)
	}
}

// assertNoRelatesToEdge asserts st holds no relates-to edge from -> to.
func assertNoRelatesToEdge(t *testing.T, ctx context.Context, st storage.DoltStorage, from, to string) {
	t.Helper()
	deps, err := st.GetDependencyRecords(ctx, from)
	if err != nil {
		t.Fatalf("GetDependencyRecords(%s): %v", from, err)
	}
	if e := findRelatesToEdge(deps, from, to); e != nil {
		t.Errorf("store still holds relates-to edge %+v after removal", e)
	}
}

// assertDependencyEvent asserts issueID's durable event log carries one event
// of the given type (dependency_added / dependency_removed for the relate
// verbs).
func assertDependencyEvent(t *testing.T, ctx context.Context, st storage.DoltStorage, issueID string, want types.EventType) {
	t.Helper()
	events, err := st.GetEvents(ctx, issueID, 0)
	if err != nil {
		t.Fatalf("GetEvents(%s): %v", issueID, err)
	}
	for _, ev := range events {
		if ev != nil && ev.EventType == want {
			return
		}
	}
	t.Errorf("issue %s has no %s event; events: %+v", issueID, want, events)
}

// TestRelateCrossPrefixTargetRoutes is the bd-un3t5j (GH#4647) repro: bd dep
// add routes a cross-prefix target through routes.jsonl (#3168), but bd
// relate resolves both IDs with utils.ResolvePartialID against the local store
// only, so relating a town issue to a routed rig issue fails to resolve the
// rig ID. The correct behaviour is that the routed ID resolves and the link is
// recorded on the local issue (each source store gains its own half; the
// full-fixture test below pins the rig half).
//
// This test uses os.Chdir and assigns the CLI globals, so it cannot run in
// parallel with other tests.
func TestRelateCrossPrefixTargetRoutes(t *testing.T) {
	e := setupCrossPrefixRelateEnv(t)

	// bd dep add's own resolver finds the routed target: the fixture routes.
	if id, _, cleanup, err := resolveIDWithRouting(e.ctx, e.townStore, "gt-target1"); err != nil {
		t.Fatalf("fixture: resolveIDWithRouting(gt-target1) failed: %v", err)
	} else {
		cleanup()
		if id != "gt-target1" {
			t.Fatalf("fixture: resolveIDWithRouting = %q, want gt-target1", id)
		}
	}

	if err := runRelate(relateCmd, []string{"hq-src1", "gt-target1"}); err != nil {
		t.Fatalf("bd relate hq-src1 gt-target1 failed on a routed cross-prefix target (bd dep add resolves it): %v", err)
	}

	assertRelatesToEdge(t, e.ctx, e.townStore, "hq-src1", "gt-target1")
}

// TestRelateCrossPrefixWritesEachHalfInItsSourceRig pins AC-1 of the bd-un3t5j
// design: relating two directly routed rigs must give EACH source store its
// own outgoing relates-to edge and its own dependency_added event, and both
// must be durable — asserted from fresh read-only reopens, not the handles the
// writes went through.
//
// This test uses os.Chdir and assigns the CLI globals, so it cannot run in
// parallel with other tests.
func TestRelateCrossPrefixWritesEachHalfInItsSourceRig(t *testing.T) {
	e := setupCrossPrefixRelateEnv(t)

	if err := runRelate(relateCmd, []string{"hq-src1", "gt-target1"}); err != nil {
		t.Fatalf("bd relate hq-src1 gt-target1: %v", err)
	}

	// Town half, read after a fresh open of the town database.
	townRO := e.reopenTownReadonly(t)
	assertRelatesToEdge(t, e.ctx, townRO, "hq-src1", "gt-target1")
	assertDependencyEvent(t, e.ctx, townRO, "hq-src1", types.EventDependencyAdded)

	// Rig half, read through the routed read-only open.
	rigRO := e.openRigReadonly(t, "gt-target1")
	assertRelatesToEdge(t, e.ctx, rigRO, "gt-target1", "hq-src1")
	assertDependencyEvent(t, e.ctx, rigRO, "gt-target1", types.EventDependencyAdded)
}

// TestUnrelateCrossPrefixRemovesBothHalves pins AC-2: unrelating a cross-rig
// linked pair removes BOTH stores' halves and records a dependency_removed
// event per source. The pair is seeded through the raw dependency verbs (not
// bd relate) so this test fails on the base for its own reason — the
// unresolvable-to-base removal — rather than inheriting relate's.
//
// This test uses os.Chdir and assigns the CLI globals, so it cannot run in
// parallel with other tests.
func TestUnrelateCrossPrefixRemovesBothHalves(t *testing.T) {
	e := setupCrossPrefixRelateEnv(t)

	// Seed both halves before the rig store is released to routing. The
	// fixture already closed the rig store, so reopen it read-write through
	// the same store factory just long enough to write the edge.
	rigRW := openRigWritable(t, e.rigBeadsDir)
	seedRelatesToEdge(t, e.ctx, e.townStore, "hq-src1", "gt-target1")
	seedRelatesToEdge(t, e.ctx, rigRW, "gt-target1", "hq-src1")
	rigRW.Close()

	if err := runUnrelate(unrelateCmd, []string{"hq-src1", "gt-target1"}); err != nil {
		t.Fatalf("bd unrelate hq-src1 gt-target1: %v", err)
	}

	townRO := e.reopenTownReadonly(t)
	assertNoRelatesToEdge(t, e.ctx, townRO, "hq-src1", "gt-target1")
	assertDependencyEvent(t, e.ctx, townRO, "hq-src1", types.EventDependencyRemoved)

	rigRO := e.openRigReadonly(t, "gt-target1")
	assertNoRelatesToEdge(t, e.ctx, rigRO, "gt-target1", "hq-src1")
	assertDependencyEvent(t, e.ctx, rigRO, "gt-target1", types.EventDependencyRemoved)
}

// openRigWritable opens the rig's .beads directory for a seeded write. The
// production relate background is the writable prefix-route open; the seeded
// test uses the same factory so it never routes.
func openRigWritable(t *testing.T, rigBeadsDir string) storage.DoltStorage {
	t.Helper()
	st, err := openForeignStore(context.Background(), rigBeadsDir, foreignWrite)
	if err != nil {
		t.Fatalf("open rig store writable: %v", err)
	}
	return st
}

// seedRelatesToEdge stores a relates-to edge through the raw dependency verb,
// recording its dependency_added event like the explicit verbs do.
func seedRelatesToEdge(t *testing.T, ctx context.Context, st storage.DoltStorage, from, to string) {
	t.Helper()
	if err := st.AddDependencyWithOptions(ctx, &types.Dependency{
		IssueID:     from,
		DependsOnID: to,
		Type:        types.DepRelatesTo,
	}, "seed", storage.DependencyAddOptions{EmitEvent: true}); err != nil {
		t.Fatalf("seed relates-to %s -> %s: %v", from, to, err)
	}
}
