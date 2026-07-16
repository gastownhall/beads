package flatfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestAddAndGetDependency(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateIssue(ctx, &types.Issue{ID: "a-1", Title: "Parent"}, "tester")
	s.CreateIssue(ctx, &types.Issue{ID: "a-2", Title: "Child"}, "tester")

	dep := &types.Dependency{
		IssueID:     "a-2",
		DependsOnID: "a-1",
		Type:        "blocks",
	}
	if err := s.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// GetDependencies: a-2 depends on a-1
	deps, err := s.GetDependencies(ctx, "a-2")
	if err != nil {
		t.Fatalf("GetDependencies: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "a-1" {
		t.Errorf("GetDependencies = %v, want [a-1]", deps)
	}

	// GetDependents: a-1 is depended on by a-2
	dependents, err := s.GetDependents(ctx, "a-1")
	if err != nil {
		t.Fatalf("GetDependents: %v", err)
	}
	if len(dependents) != 1 || dependents[0].ID != "a-2" {
		t.Errorf("GetDependents = %v, want [a-2]", dependents)
	}
}

func TestAddDependencyIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateIssue(ctx, &types.Issue{ID: "b-1", Title: "One"}, "tester")
	s.CreateIssue(ctx, &types.Issue{ID: "b-2", Title: "Two"}, "tester")

	dep := &types.Dependency{IssueID: "b-2", DependsOnID: "b-1", Type: "blocks"}
	s.AddDependency(ctx, dep, "tester")
	s.AddDependency(ctx, dep, "tester") // second time is no-op

	issue, _ := s.GetIssue(ctx, "b-2")
	count := 0
	for _, d := range issue.Dependencies {
		if d.DependsOnID == "b-1" && d.Type == "blocks" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("duplicate deps: count = %d, want 1", count)
	}
}

func TestAddDependencyCrossPrefix(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateIssue(ctx, &types.Issue{ID: "rigA-1", Title: "Local"}, "tester")

	// SQL wrappers (dolt isCrossPrefixDep -> AddDependencyOpts.IsCrossPrefix)
	// treat a target with a different ID prefix as living in another rig:
	// existence validation is skipped, the edge is recorded as external.
	dep := &types.Dependency{IssueID: "rigA-1", DependsOnID: "rigB-456", Type: "blocks"}
	if err := s.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("AddDependency cross-prefix: %v", err)
	}

	issue, err := s.GetIssue(ctx, "rigA-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	found := false
	for _, d := range issue.Dependencies {
		if d.DependsOnID == "rigB-456" && d.Type == "blocks" {
			found = true
		}
	}
	if !found {
		t.Errorf("cross-prefix dependency rigA-1 -> rigB-456 not persisted: %v", issue.Dependencies)
	}
}

func TestRemoveDependency(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateIssue(ctx, &types.Issue{ID: "c-1", Title: "Target"}, "tester")
	s.CreateIssue(ctx, &types.Issue{ID: "c-2", Title: "Source"}, "tester")
	s.AddDependency(ctx, &types.Dependency{IssueID: "c-2", DependsOnID: "c-1", Type: "blocks"}, "tester")

	if err := s.RemoveDependency(ctx, "c-2", "c-1", "tester"); err != nil {
		t.Fatalf("RemoveDependency: %v", err)
	}

	deps, _ := s.GetDependencies(ctx, "c-2")
	if len(deps) != 0 {
		t.Errorf("after remove: len(deps) = %d, want 0", len(deps))
	}
}

func TestRemoveDependencyNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateIssue(ctx, &types.Issue{ID: "d-1", Title: "Solo"}, "tester")

	// Mirrors issueops.RemoveDependencyInTx: removing a non-existent edge is
	// a silent no-op (pinned by the RemoveMissingAndUnblock conformance case).
	err := s.RemoveDependency(ctx, "d-1", "nonexistent", "tester")
	if err != nil {
		t.Errorf("RemoveDependency missing = %v, want nil (silent no-op)", err)
	}

	// A missing SOURCE issue is the same silent no-op: the SQL reference never
	// validates source existence — it just finds no matching edge row.
	err = s.RemoveDependency(ctx, "ghost-1", "d-1", "tester")
	if err != nil {
		t.Errorf("RemoveDependency missing source = %v, want nil (silent no-op)", err)
	}
}

func TestGetDependenciesSkipsOnlyMissingAndExternal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateIssue(ctx, &types.Issue{ID: "f-1", Title: "Source"}, "tester")
	s.CreateIssue(ctx, &types.Issue{ID: "f-2", Title: "Target"}, "tester")
	s.CreateIssue(ctx, &types.Issue{ID: "f-9", Title: "Doomed"}, "tester")
	s.AddDependency(ctx, &types.Dependency{IssueID: "f-1", DependsOnID: "f-2", Type: "blocks"}, "tester")
	s.AddDependency(ctx, &types.Dependency{IssueID: "f-1", DependsOnID: "external:other-rig", Type: "related"}, "tester")
	s.AddDependency(ctx, &types.Dependency{IssueID: "f-1", DependsOnID: "f-9", Type: "related"}, "tester")
	if err := os.Remove(filepath.Join(s.issuesDir, "f-9.json")); err != nil {
		t.Fatalf("remove target file: %v", err)
	}

	// f-9's file is gone, external:other-rig never hydrates. Both are silently
	// skipped, mirroring the SQL JOIN that simply produces no row for them.
	deps, err := s.GetDependencies(ctx, "f-1")
	if err != nil {
		t.Fatalf("GetDependencies: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "f-2" {
		t.Fatalf("GetDependencies = %v, want [f-2]", deps)
	}
	depsMeta, err := s.GetDependenciesWithMetadata(ctx, "f-1")
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata: %v", err)
	}
	if len(depsMeta) != 1 || depsMeta[0].ID != "f-2" {
		t.Fatalf("GetDependenciesWithMetadata = %v, want [f-2]", depsMeta)
	}

	// A target file that exists but cannot be decoded is a read failure, not a
	// missing row: SQL backends fail the query, so must flatfile (previously
	// the edge silently vanished from the result).
	if err := os.WriteFile(filepath.Join(s.issuesDir, "f-2.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupt target file: %v", err)
	}
	if _, err := s.GetDependencies(ctx, "f-1"); err == nil {
		t.Error("GetDependencies with corrupt target = nil error, want failure")
	}
	if _, err := s.GetDependenciesWithMetadata(ctx, "f-1"); err == nil {
		t.Error("GetDependenciesWithMetadata with corrupt target = nil error, want failure")
	}
}

func TestCountDependencies(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateIssue(ctx, &types.Issue{ID: "e-1", Title: "A"}, "tester")
	s.CreateIssue(ctx, &types.Issue{ID: "e-2", Title: "B"}, "tester")
	s.CreateIssue(ctx, &types.Issue{ID: "e-3", Title: "C"}, "tester")

	s.AddDependency(ctx, &types.Dependency{IssueID: "e-3", DependsOnID: "e-1", Type: "blocks"}, "tester")
	s.AddDependency(ctx, &types.Dependency{IssueID: "e-3", DependsOnID: "e-2", Type: "blocks"}, "tester")

	count, err := s.CountDependencies(ctx, "e-3")
	if err != nil {
		t.Fatalf("CountDependencies: %v", err)
	}
	if count != 2 {
		t.Errorf("CountDependencies = %d, want 2", count)
	}

	depCount, _ := s.CountDependents(ctx, "e-1")
	if depCount != 1 {
		t.Errorf("CountDependents(e-1) = %d, want 1", depCount)
	}
}

func TestGetDependencyTree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateIssue(ctx, &types.Issue{ID: "t-1", Title: "Root"}, "tester")
	s.CreateIssue(ctx, &types.Issue{ID: "t-2", Title: "Mid"}, "tester")
	s.CreateIssue(ctx, &types.Issue{ID: "t-3", Title: "Leaf"}, "tester")

	s.AddDependency(ctx, &types.Dependency{IssueID: "t-2", DependsOnID: "t-1", Type: "blocks"}, "tester")
	s.AddDependency(ctx, &types.Dependency{IssueID: "t-3", DependsOnID: "t-2", Type: "blocks"}, "tester")

	// Forward: t-3 -> t-2 -> t-1
	tree, err := s.GetDependencyTree(ctx, "t-3", 10, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree: %v", err)
	}
	if len(tree) != 3 {
		t.Errorf("tree len = %d, want 3", len(tree))
	}

	// Depth limit: traversal stops when depth reaches maxDepth, so maxDepth=1
	// yields just the root (pinned by the DependencyTree conformance case).
	tree2, _ := s.GetDependencyTree(ctx, "t-3", 1, false, false)
	if len(tree2) != 1 {
		t.Errorf("depth-1 tree len = %d, want 1 (root only)", len(tree2))
	}
}

// TestComputeBlockedSetWaitsForGate pins the waits-for branch of the SQL
// is_blocked projection (issueops waitsForGateBlockedSQL): a waiter with a
// waits-for edge is blocked while the spawner target has any active
// parent-child child; an any-children gate is satisfied by a closed
// (specifically closed, not pinned) child.
func TestComputeBlockedSetWaitsForGate(t *testing.T) {
	mk := func(id string, st types.Status, deps ...*types.Dependency) *types.Issue {
		return &types.Issue{ID: id, Title: id, Status: st, Dependencies: deps}
	}
	waits := func(target, metadata string) *types.Dependency {
		return &types.Dependency{IssueID: "w-1", DependsOnID: target, Type: types.DepWaitsFor, Metadata: metadata}
	}
	child := func(id string, st types.Status, parent string) *types.Issue {
		return mk(id, st, &types.Dependency{IssueID: id, DependsOnID: parent, Type: types.DepParentChild})
	}

	cases := []struct {
		name    string
		issues  []*types.Issue
		blocked bool
	}{
		{"active child blocks waiter", []*types.Issue{
			mk("w-1", types.StatusOpen, waits("s-1", "")),
			mk("s-1", types.StatusOpen),
			child("c-1", types.StatusOpen, "s-1"),
		}, true},
		{"all children closed unblocks", []*types.Issue{
			mk("w-1", types.StatusOpen, waits("s-1", "")),
			mk("s-1", types.StatusOpen),
			child("c-1", types.StatusClosed, "s-1"),
		}, false},
		{"all-children gate stays blocked with one closed one open", []*types.Issue{
			mk("w-1", types.StatusOpen, waits("s-1", "")),
			mk("s-1", types.StatusOpen),
			child("c-1", types.StatusClosed, "s-1"),
			child("c-2", types.StatusOpen, "s-1"),
		}, true},
		{"any-children gate satisfied by one closed child", []*types.Issue{
			mk("w-1", types.StatusOpen, waits("s-1", `{"gate":"any-children"}`)),
			mk("s-1", types.StatusOpen),
			child("c-1", types.StatusClosed, "s-1"),
			child("c-2", types.StatusOpen, "s-1"),
		}, false},
		{"any-children gate not satisfied by pinned child", []*types.Issue{
			mk("w-1", types.StatusOpen, waits("s-1", `{"gate":"any-children"}`)),
			mk("s-1", types.StatusOpen),
			child("c-1", types.StatusPinned, "s-1"),
			child("c-2", types.StatusOpen, "s-1"),
		}, true},
		{"no children means not blocked", []*types.Issue{
			mk("w-1", types.StatusOpen, waits("s-1", "")),
			mk("s-1", types.StatusOpen),
		}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeBlockedSet(tc.issues)["w-1"]
			if got != tc.blocked {
				t.Errorf("blocked[w-1] = %v, want %v", got, tc.blocked)
			}
		})
	}
}

// TestGetReadyWorkWaitsForGate checks the store-level wiring: a fanout waiter
// is excluded from ready work while its spawner has open children (matching
// Dolt/SQL, where is_blocked=1 keeps it out of bd ready) and returns once
// they close.
func TestGetReadyWorkWaitsForGate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateIssue(ctx, &types.Issue{ID: "g-spawn", Title: "Spawner"}, "tester")
	s.CreateIssue(ctx, &types.Issue{ID: "g-wait", Title: "Waiter"}, "tester")
	s.CreateIssue(ctx, &types.Issue{ID: "g-child", Title: "Child"}, "tester")
	s.AddDependency(ctx, &types.Dependency{IssueID: "g-wait", DependsOnID: "g-spawn", Type: types.DepWaitsFor}, "tester")
	s.AddDependency(ctx, &types.Dependency{IssueID: "g-child", DependsOnID: "g-spawn", Type: types.DepParentChild}, "tester")

	inReady := func() bool {
		t.Helper()
		ready, err := s.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		for _, issue := range ready {
			if issue.ID == "g-wait" {
				return true
			}
		}
		return false
	}

	if inReady() {
		t.Error("waiter in ready work while spawner has an open child; want excluded")
	}
	if err := s.CloseIssue(ctx, "g-child", "done", "tester", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	if !inReady() {
		t.Error("waiter not in ready work after all children closed; want included")
	}
}

func TestGetDependencyTreeMissingRoot(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateIssue(ctx, &types.Issue{ID: "t-1", Title: "Root"}, "tester")

	// SQL backends (buildDependencyTreeInTx -> GetIssueInTx) return a wrapped
	// storage.ErrNotFound for a mistyped root, not an empty tree.
	_, err := s.GetDependencyTree(ctx, "typo-999", 10, false, false)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetDependencyTree missing root err = %v, want storage.ErrNotFound", err)
	}

	// maxDepth <= 0 returns empty before the root lookup in the SQL walk, so
	// the missing root is not an error there.
	tree, err := s.GetDependencyTree(ctx, "typo-999", 0, false, false)
	if err != nil || len(tree) != 0 {
		t.Errorf("GetDependencyTree(maxDepth=0) = (%v, %v), want empty, nil", tree, err)
	}
}

// The SQL wrappers classify "external:" dependency targets as
// DepTargetExternal on every write path (issueops.ClassifyDepTarget checks
// the string prefix unconditionally, before any cross-prefix logic), storing
// them in depends_on_external — a column waitsForGateBlockedSQL never joins.
// A waits-for edge to an "external:" spawner therefore never gates in Dolt,
// even when local issues carry parent-child edges naming the same external
// spawner (both edge kinds are reachable via ordinary `bd dep add`, which
// skips existence validation for external targets).
func TestComputeBlockedSetExternalWaitsForNeverGates(t *testing.T) {
	mk := func(id string, st types.Status, deps ...*types.Dependency) *types.Issue {
		return &types.Issue{ID: id, Title: id, Status: st, Dependencies: deps}
	}
	edge := func(src, target string, typ types.DependencyType) *types.Dependency {
		return &types.Dependency{IssueID: src, DependsOnID: target, Type: typ}
	}

	issues := []*types.Issue{
		mk("w-1", types.StatusOpen, edge("w-1", "external:rig-7", types.DepWaitsFor)),
		mk("w-2", types.StatusOpen, edge("w-2", "external:rig-7", types.DepParentChild)),
	}
	if computeBlockedSet(issues)["w-1"] {
		t.Error("blocked[w-1] = true: waits-for edge to external: spawner gated on local children; SQL classifies the edge depends_on_external and never blocks")
	}
}

// Store-level wiring for the same rule: the waiter stays in bd ready.
func TestGetReadyWorkExternalWaitsForDoesNotBlock(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateIssue(ctx, &types.Issue{ID: "x-wait", Title: "Waiter"}, "tester")
	s.CreateIssue(ctx, &types.Issue{ID: "x-child", Title: "Child of external spawner"}, "tester")
	if err := s.AddDependency(ctx, &types.Dependency{IssueID: "x-wait", DependsOnID: "external:rig-7", Type: types.DepWaitsFor}, "tester"); err != nil {
		t.Fatalf("AddDependency waits-for external: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{IssueID: "x-child", DependsOnID: "external:rig-7", Type: types.DepParentChild}, "tester"); err != nil {
		t.Fatalf("AddDependency parent-child external: %v", err)
	}

	ready, err := s.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	found := false
	for _, issue := range ready {
		if issue.ID == "x-wait" {
			found = true
		}
	}
	if !found {
		t.Error("x-wait missing from ready work: external: waits-for edge blocked it")
	}
}
