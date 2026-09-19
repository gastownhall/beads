//go:build cgo

package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The bulk `bd dep add --file` route asserts its edges through the
// DependencyEditor role, so the invariants the hand-rolled bulk transaction
// used to carry are pinned here against the role the route now calls — entered
// through the store's own accessor, exactly as addDependencyEdgesDirect enters
// it.

// bd-wg7ve: parent-child edges are applied before blocking ones regardless of
// request order, so the complete planned hierarchy is visible when a blocking
// edge is validated against it.
//
// The ordering is what decides the ANSWER here. Applied hierarchy-first, the
// grandparent is an ancestor of the child by the time `child blocks grand` is
// checked, so the whole request is refused and nothing is written. Applied in
// request order, that blocking edge lands before any hierarchy exists, nothing
// notices, and all three edges commit.
func TestBulkDepAddRoleAppliesHierarchyBeforeBlockingEdges(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	s := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	const grand, parent, child = "test-bulk-order-grand", "test-bulk-order-parent", "test-bulk-order-child"
	seedBulkRoleIssues(ctx, t, s, grand, parent, child)

	editor, err := s.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	_, err = editor.AddDependencies(ctx, issueops.AddDependenciesRequest{
		Actor: "tester",
		Edges: []issueops.DependencyEdge{
			{IssueID: child, DependsOnID: grand, Type: types.DepBlocks},
			{IssueID: child, DependsOnID: parent, Type: types.DepParentChild},
			{IssueID: parent, DependsOnID: grand, Type: types.DepParentChild},
		},
	})
	var conflict *domain.DependencyHierarchyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want *domain.DependencyHierarchyConflictError: the hierarchy is applied first", err)
	}
	if !conflict.BlockerIsAncestor {
		t.Fatalf("conflict = %#v, want the blocker reported as the child's ancestor", conflict)
	}

	for _, id := range []string{child, parent} {
		deps, err := s.GetDependencyRecords(ctx, id)
		if err != nil {
			t.Fatalf("GetDependencyRecords(%s): %v", id, err)
		}
		if len(deps) != 0 {
			t.Fatalf("%s edges after a refused request = %#v, want none: the request is all-or-nothing", id, deps)
		}
	}
}

// bd-6dnrw.8: with the per-edge probe off the whole-graph gate is the check
// that actually holds the invariant. Its refusal must roll the request back —
// no cycle may ever commit — and must render the message the bulk route has
// always printed while still matching the typed sentinel.
func TestBulkDepAddRoleWholeGraphGateRollsBack(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	s := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	const a, b = "test-cyc-a", "test-cyc-b"
	seedBulkRoleIssues(ctx, t, s, a, b)
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID: a, DependsOnID: b, Type: types.DepBlocks,
	}, "test"); err != nil {
		t.Fatalf("seed dependency: %v", err)
	}

	editor, err := s.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	_, err = editor.AddDependencies(ctx, issueops.AddDependenciesRequest{
		Actor:                 "tester",
		SkipPerEdgeCycleCheck: true,
		Edges: []issueops.DependencyEdge{
			{IssueID: b, DependsOnID: a, Type: types.DepBlocks},
		},
	})
	if !errors.Is(err, domain.ErrDependencyCycle) {
		t.Fatalf("final gate error = %v, want errors.Is domain.ErrDependencyCycle", err)
	}
	// Typing must not alter the rendered text: no sentinel string appended.
	const wantMsg = "dependency cycle would be created: test-cyc-b → test-cyc-a → test-cyc-b (no edges added; run 'bd dep cycles' for analysis)"
	if err.Error() != wantMsg {
		t.Fatalf("final gate message = %q, want byte-identical %q", err.Error(), wantMsg)
	}

	deps, err := s.GetDependencyRecords(ctx, b)
	if err != nil {
		t.Fatalf("GetDependencyRecords: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("cycle edge was committed despite gate: %#v", deps)
	}
}

// bd-578h9.9: a pre-existing committed cycle touching an endpoint of the
// request must not block unrelated bulk wiring — only cycles that traverse a
// new edge gate the commit.
func TestBulkDepAddRoleIgnoresPreexistingCycleAtEndpoint(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	s := newTestStore(t, filepath.Join(tmpDir, ".beads", "beads.db"))
	ctx := context.Background()

	const a, b, c = "test-pre-a", "test-pre-b", "test-pre-c"
	seedBulkRoleIssues(ctx, t, s, a, b, c)

	// Commit the cycle a <-> b first (SkipCycleCheck stands in for legacy data
	// that predates cycle validation).
	if err := s.RunInTransaction(ctx, "test: seed cycle", func(tx storage.Transaction) error {
		for _, pair := range [][2]string{{a, b}, {b, a}} {
			dep := &types.Dependency{IssueID: pair[0], DependsOnID: pair[1], Type: types.DepBlocks}
			if err := tx.AddDependencyWithOptions(ctx, dep, "test", storage.DependencyAddOptions{SkipCycleCheck: true}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed cycle: %v", err)
	}

	editor, err := s.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	if _, err := editor.AddDependencies(ctx, issueops.AddDependenciesRequest{
		Actor: "tester",
		Edges: []issueops.DependencyEdge{
			{IssueID: a, DependsOnID: c, Type: types.DepBlocks},
		},
	}); err != nil {
		t.Fatalf("unrelated bulk edge was blocked: %v", err)
	}

	deps, err := s.GetDependencyRecords(ctx, a)
	if err != nil {
		t.Fatalf("GetDependencyRecords: %v", err)
	}
	var foundC bool
	for _, dep := range deps {
		if dep.DependsOnID == c {
			foundC = true
		}
	}
	if !foundC {
		t.Fatalf("edge a -> c did not commit: %#v", deps)
	}
}

func seedBulkRoleIssues(ctx context.Context, t *testing.T, s *dolt.DoltStore, ids ...string) {
	t.Helper()
	for _, id := range ids {
		issue := &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen,
			Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now(),
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
}
