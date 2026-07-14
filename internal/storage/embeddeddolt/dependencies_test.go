//go:build cgo

package embeddeddolt_test

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestAddDependency(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("basic_blocks", func(t *testing.T) {
		te := newTestEnv(t, "db")
		ctx := t.Context()

		a := &types.Issue{ID: "db-a", Title: "A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		b := &types.Issue{ID: "db-b", Title: "B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, a, "tester"); err != nil {
			t.Fatalf("CreateIssue A: %v", err)
		}
		if err := te.store.CreateIssue(ctx, b, "tester"); err != nil {
			t.Fatalf("CreateIssue B: %v", err)
		}

		dep := &types.Dependency{IssueID: "db-a", DependsOnID: "db-b", Type: types.DepBlocks}
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
	})

	t.Run("cycle_detection", func(t *testing.T) {
		te := newTestEnv(t, "cy")
		ctx := t.Context()

		a := &types.Issue{ID: "cy-a", Title: "A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		b := &types.Issue{ID: "cy-b", Title: "B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, a, "tester"); err != nil {
			t.Fatalf("CreateIssue A: %v", err)
		}
		if err := te.store.CreateIssue(ctx, b, "tester"); err != nil {
			t.Fatalf("CreateIssue B: %v", err)
		}

		// A blocks B
		dep1 := &types.Dependency{IssueID: "cy-a", DependsOnID: "cy-b", Type: types.DepBlocks}
		if err := te.store.AddDependency(ctx, dep1, "tester"); err != nil {
			t.Fatalf("AddDependency A->B: %v", err)
		}
		if err := te.store.Commit(ctx, "dep1"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		// B blocks A should fail (cycle)
		dep2 := &types.Dependency{IssueID: "cy-b", DependsOnID: "cy-a", Type: types.DepBlocks}
		err := te.store.AddDependency(ctx, dep2, "tester")
		if err == nil {
			t.Fatal("expected cycle detection error")
		}
		if !strings.Contains(err.Error(), "cycle") {
			t.Errorf("expected cycle error, got: %v", err)
		}
	})

	t.Run("mixed_table_cycle_permanent_endpoints", func(t *testing.T) {
		te := newTestEnv(t, "mp")
		ctx := t.Context()

		for _, issue := range []*types.Issue{
			{ID: "mp-a", Title: "A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "mp-x", Title: "X", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "mp-wisp-w", Title: "W", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		} {
			if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", issue.ID, err)
			}
		}

		for _, dep := range []*types.Dependency{
			{IssueID: "mp-x", DependsOnID: "mp-wisp-w", Type: types.DepBlocks},
			{IssueID: "mp-wisp-w", DependsOnID: "mp-a", Type: types.DepBlocks},
		} {
			if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
				t.Fatalf("AddDependency %s->%s: %v", dep.IssueID, dep.DependsOnID, err)
			}
		}

		err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "mp-a", DependsOnID: "mp-x", Type: types.DepBlocks}, "tester")
		if err == nil {
			t.Fatal("expected mixed-table cycle detection error")
		}
		if !strings.Contains(err.Error(), "cycle") {
			t.Errorf("expected cycle error, got: %v", err)
		}
	})

	t.Run("mixed_table_cycle_wisp_endpoints", func(t *testing.T) {
		te := newTestEnv(t, "mw")
		ctx := t.Context()

		for _, issue := range []*types.Issue{
			{ID: "mw-wisp-a", Title: "A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
			{ID: "mw-wisp-x", Title: "X", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
			{ID: "mw-b", Title: "B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		} {
			if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", issue.ID, err)
			}
		}

		for _, dep := range []*types.Dependency{
			{IssueID: "mw-wisp-x", DependsOnID: "mw-b", Type: types.DepBlocks},
			{IssueID: "mw-b", DependsOnID: "mw-wisp-a", Type: types.DepBlocks},
		} {
			if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
				t.Fatalf("AddDependency %s->%s: %v", dep.IssueID, dep.DependsOnID, err)
			}
		}

		err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "mw-wisp-a", DependsOnID: "mw-wisp-x", Type: types.DepBlocks}, "tester")
		if err == nil {
			t.Fatal("expected mixed-table cycle detection error")
		}
		if !strings.Contains(err.Error(), "cycle") {
			t.Errorf("expected cycle error, got: %v", err)
		}
	})

	t.Run("cross_type_unrelated_allowed", func(t *testing.T) {
		te := newTestEnv(t, "ct")
		ctx := t.Context()

		epic := &types.Issue{ID: "ct-epic", Title: "Epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
		task := &types.Issue{ID: "ct-task", Title: "Task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, epic, "tester"); err != nil {
			t.Fatalf("CreateIssue epic: %v", err)
		}
		if err := te.store.CreateIssue(ctx, task, "tester"); err != nil {
			t.Fatalf("CreateIssue task: %v", err)
		}

		// Task blocking an unrelated epic should succeed; only parent-child
		// shadow edges are rejected.
		dep := &types.Dependency{IssueID: "ct-epic", DependsOnID: "ct-task", Type: types.DepBlocks}
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("task blocking unrelated epic should succeed: %v", err)
		}
	})

	t.Run("parent_child_shadow_rejected", func(t *testing.T) {
		te := newTestEnv(t, "sh")
		ctx := t.Context()

		epic := &types.Issue{ID: "sh-epic", Title: "Epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
		task := &types.Issue{ID: "sh-task", Title: "Task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, epic, "tester"); err != nil {
			t.Fatalf("CreateIssue epic: %v", err)
		}
		if err := te.store.CreateIssue(ctx, task, "tester"); err != nil {
			t.Fatalf("CreateIssue task: %v", err)
		}

		// Establish parent-child: task is a child of epic.
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "sh-task", DependsOnID: "sh-epic", Type: types.DepParentChild}, "tester"); err != nil {
			t.Fatalf("parent-child setup: %v", err)
		}

		// Epic gated on its own child task -> livelock via blocked-state
		// cascade, rejected.
		dep := &types.Dependency{IssueID: "sh-epic", DependsOnID: "sh-task", Type: types.DepBlocks}
		err := te.store.AddDependency(ctx, dep, "tester")
		if err == nil {
			t.Fatal("expected rejection of blocks edge within a hierarchy line")
		}
		if !strings.Contains(err.Error(), "descendant") {
			t.Errorf("expected descendant-livelock message, got: %v", err)
		}

		// Child task gated on its own ancestor epic -> deadlock, rejected.
		err = te.store.AddDependency(ctx, &types.Dependency{IssueID: "sh-task", DependsOnID: "sh-epic", Type: types.DepBlocks}, "tester")
		if err == nil || !strings.Contains(err.Error(), "ancestor") {
			t.Errorf("expected ancestor-deadlock message, got: %v", err)
		}

		// Siblings share a hierarchy component but not a hierarchy line:
		// ordering blocks edges between them stay allowed (the guard walks
		// child -> parent only, never back down).
		sib := &types.Issue{ID: "sh-sib", Title: "Sibling", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, sib, "tester"); err != nil {
			t.Fatalf("CreateIssue sibling: %v", err)
		}
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "sh-sib", DependsOnID: "sh-epic", Type: types.DepParentChild}, "tester"); err != nil {
			t.Fatalf("parent-child setup for sibling: %v", err)
		}
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "sh-sib", DependsOnID: "sh-task", Type: types.DepBlocks}, "tester"); err != nil {
			t.Errorf("sibling ordering blocks edge should succeed: %v", err)
		}
	})

	t.Run("wisp_parent_child_shadow_rejected", func(t *testing.T) {
		te := newTestEnv(t, "wh")
		ctx := t.Context()

		for _, issue := range []*types.Issue{
			{ID: "wh-parent", Title: "Parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic, Ephemeral: true},
			{ID: "wh-child", Title: "Child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		} {
			if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", issue.ID, err)
			}
		}
		if err := te.store.AddDependency(ctx, &types.Dependency{
			IssueID: "wh-child", DependsOnID: "wh-parent", Type: types.DepParentChild,
		}, "tester"); err != nil {
			t.Fatalf("parent-child setup: %v", err)
		}

		err := te.store.AddDependency(ctx, &types.Dependency{
			IssueID: "wh-parent", DependsOnID: "wh-child", Type: types.DepBlocks,
		}, "tester")
		if err == nil || !strings.Contains(err.Error(), "descendant") {
			t.Fatalf("wisp descendant block error = %v, want hierarchy rejection", err)
		}

		// Hierarchy validation runs before the existing-edge type conflict, so a
		// conditional gate over the child->parent pair reports the real deadlock.
		err = te.store.AddDependency(ctx, &types.Dependency{
			IssueID: "wh-child", DependsOnID: "wh-parent", Type: types.DepConditionalBlocks,
		}, "tester")
		if err == nil || !strings.Contains(err.Error(), "ancestor") {
			t.Fatalf("wisp ancestor block error = %v, want hierarchy rejection", err)
		}
	})

	t.Run("idempotent_same_type", func(t *testing.T) {
		te := newTestEnv(t, "id")
		ctx := t.Context()

		a := &types.Issue{ID: "id-a", Title: "A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		b := &types.Issue{ID: "id-b", Title: "B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, a, "tester"); err != nil {
			t.Fatalf("CreateIssue A: %v", err)
		}
		if err := te.store.CreateIssue(ctx, b, "tester"); err != nil {
			t.Fatalf("CreateIssue B: %v", err)
		}

		dep := &types.Dependency{IssueID: "id-a", DependsOnID: "id-b", Type: types.DepBlocks}
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency (first): %v", err)
		}
		if err := te.store.Commit(ctx, "dep"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		// Same dep again should succeed (idempotent).
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency (second): %v", err)
		}
	})

	t.Run("type_conflict", func(t *testing.T) {
		te := newTestEnv(t, "tc")
		ctx := t.Context()

		a := &types.Issue{ID: "tc-a", Title: "A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		b := &types.Issue{ID: "tc-b", Title: "B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, a, "tester"); err != nil {
			t.Fatalf("CreateIssue A: %v", err)
		}
		if err := te.store.CreateIssue(ctx, b, "tester"); err != nil {
			t.Fatalf("CreateIssue B: %v", err)
		}

		dep1 := &types.Dependency{IssueID: "tc-a", DependsOnID: "tc-b", Type: types.DepBlocks}
		if err := te.store.AddDependency(ctx, dep1, "tester"); err != nil {
			t.Fatalf("AddDependency blocks: %v", err)
		}
		if err := te.store.Commit(ctx, "dep"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		// Same pair, different type should error.
		dep2 := &types.Dependency{IssueID: "tc-a", DependsOnID: "tc-b", Type: types.DepParentChild}
		err := te.store.AddDependency(ctx, dep2, "tester")
		if err == nil {
			t.Fatal("expected type conflict error")
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Errorf("expected 'already exists' error, got: %v", err)
		}
	})

	t.Run("source_not_found", func(t *testing.T) {
		te := newTestEnv(t, "sn")
		ctx := t.Context()

		b := &types.Issue{ID: "sn-b", Title: "B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, b, "tester"); err != nil {
			t.Fatalf("CreateIssue B: %v", err)
		}

		dep := &types.Dependency{IssueID: "sn-ghost", DependsOnID: "sn-b", Type: types.DepBlocks}
		err := te.store.AddDependency(ctx, dep, "tester")
		if err == nil {
			t.Fatal("expected source not found error")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found' error, got: %v", err)
		}
	})

	t.Run("target_not_found", func(t *testing.T) {
		te := newTestEnv(t, "tn")
		ctx := t.Context()

		a := &types.Issue{ID: "tn-a", Title: "A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, a, "tester"); err != nil {
			t.Fatalf("CreateIssue A: %v", err)
		}

		dep := &types.Dependency{IssueID: "tn-a", DependsOnID: "tn-ghost", Type: types.DepBlocks}
		err := te.store.AddDependency(ctx, dep, "tester")
		if err == nil {
			t.Fatal("expected target not found error")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found' error, got: %v", err)
		}
	})

	t.Run("external_ref_skips_target_validation", func(t *testing.T) {
		te := newTestEnv(t, "er")
		ctx := t.Context()

		a := &types.Issue{ID: "er-a", Title: "A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, a, "tester"); err != nil {
			t.Fatalf("CreateIssue A: %v", err)
		}

		// external: prefix should skip target existence check.
		dep := &types.Dependency{IssueID: "er-a", DependsOnID: "external:other-repo/issue-1", Type: types.DepBlocks}
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency with external ref: %v", err)
		}
	})

	t.Run("cross_prefix_skips_target_validation", func(t *testing.T) {
		te := newTestEnv(t, "cp")
		ctx := t.Context()

		a := &types.Issue{ID: "cp-a", Title: "A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, a, "tester"); err != nil {
			t.Fatalf("CreateIssue A: %v", err)
		}

		// Target has a different prefix — lives in another rig's database.
		dep := &types.Dependency{IssueID: "cp-a", DependsOnID: "other-xyz", Type: types.DepBlocks}
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency cross-prefix: %v", err)
		}
	})

	t.Run("parent_child_cross_type_allowed", func(t *testing.T) {
		te := newTestEnv(t, "pc")
		ctx := t.Context()

		epic := &types.Issue{ID: "pc-epic", Title: "Epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
		task := &types.Issue{ID: "pc-task", Title: "Task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, epic, "tester"); err != nil {
			t.Fatalf("CreateIssue epic: %v", err)
		}
		if err := te.store.CreateIssue(ctx, task, "tester"); err != nil {
			t.Fatalf("CreateIssue task: %v", err)
		}

		// Parent-child between epic and task should succeed (the hierarchy
		// deadlock guard only applies to blocking deps).
		dep := &types.Dependency{IssueID: "pc-task", DependsOnID: "pc-epic", Type: types.DepParentChild}
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency parent-child cross-type: %v", err)
		}
	})

	t.Run("same_type_blocks_succeeds", func(t *testing.T) {
		te := newTestEnv(t, "ss")
		ctx := t.Context()

		e1 := &types.Issue{ID: "ss-e1", Title: "Epic 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
		e2 := &types.Issue{ID: "ss-e2", Title: "Epic 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
		if err := te.store.CreateIssue(ctx, e1, "tester"); err != nil {
			t.Fatalf("CreateIssue E1: %v", err)
		}
		if err := te.store.CreateIssue(ctx, e2, "tester"); err != nil {
			t.Fatalf("CreateIssue E2: %v", err)
		}

		// Epic blocking epic should succeed.
		dep := &types.Dependency{IssueID: "ss-e1", DependsOnID: "ss-e2", Type: types.DepBlocks}
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency epic-blocks-epic: %v", err)
		}
	})

	t.Run("epic_blocks_unrelated_task_succeeds", func(t *testing.T) {
		te := newTestEnv(t, "et")
		ctx := t.Context()

		epic := &types.Issue{ID: "et-epic", Title: "Epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
		task := &types.Issue{ID: "et-task", Title: "Task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, epic, "tester"); err != nil {
			t.Fatalf("CreateIssue epic: %v", err)
		}
		if err := te.store.CreateIssue(ctx, task, "tester"); err != nil {
			t.Fatalf("CreateIssue task: %v", err)
		}

		// Epic blocking an unrelated task is allowed; only parent-child shadow
		// blocks edges are rejected.
		dep := &types.Dependency{IssueID: "et-task", DependsOnID: "et-epic", Type: types.DepBlocks}
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("epic blocking unrelated task should succeed: %v", err)
		}
	})

	t.Run("combined_graph_cycle_blocks_closes_loop", func(t *testing.T) {
		// Construct the livelock scenario that PR1's relaxed cross-type rule
		// could otherwise admit:
		//   T0 blocks E2, T0 is a child of E1, T1 is a child of E2,
		//   then adding T1 blocks E1 closes a cycle in the combined
		//   blocks + parent-child graph and must be rejected.
		te := newTestEnv(t, "cc")
		ctx := t.Context()

		issues := []*types.Issue{
			{ID: "cc-e1", Title: "Epic 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic},
			{ID: "cc-e2", Title: "Epic 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic},
			{ID: "cc-t0", Title: "Task 0", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "cc-t1", Title: "Task 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		}
		for _, iss := range issues {
			if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", iss.ID, err)
			}
		}

		setup := []*types.Dependency{
			{IssueID: "cc-t0", DependsOnID: "cc-e1", Type: types.DepParentChild},
			{IssueID: "cc-t1", DependsOnID: "cc-e2", Type: types.DepParentChild},
			{IssueID: "cc-e2", DependsOnID: "cc-t0", Type: types.DepBlocks},
		}
		for _, dep := range setup {
			if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
				t.Fatalf("setup dep %+v: %v", dep, err)
			}
		}

		// Closing edge: T1 blocks E1 -> would form a cycle via
		// E1 -[blocks]-> T1 -[child-of]-> E2 -[blocks]-> T0 -[child-of]-> E1.
		err := te.store.AddDependency(ctx, &types.Dependency{
			IssueID:     "cc-e1",
			DependsOnID: "cc-t1",
			Type:        types.DepBlocks,
		}, "tester")
		if err == nil {
			t.Fatal("expected cycle rejection for combined-graph loop")
		}
		if !strings.Contains(err.Error(), "cycle") {
			t.Errorf("expected cycle error, got: %v", err)
		}
	})

	t.Run("combined_graph_cycle_parent_child_closes_loop", func(t *testing.T) {
		// Same livelock, but inserted in an order where the closing edge is
		// the parent-child link rather than the blocks link.
		te := newTestEnv(t, "cp")
		ctx := t.Context()

		issues := []*types.Issue{
			{ID: "cp-e1", Title: "Epic 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic},
			{ID: "cp-e2", Title: "Epic 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic},
			{ID: "cp-t0", Title: "Task 0", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "cp-t1", Title: "Task 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		}
		for _, iss := range issues {
			if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", iss.ID, err)
			}
		}

		setup := []*types.Dependency{
			{IssueID: "cp-t0", DependsOnID: "cp-e1", Type: types.DepParentChild},
			{IssueID: "cp-e2", DependsOnID: "cp-t0", Type: types.DepBlocks},
			{IssueID: "cp-e1", DependsOnID: "cp-t1", Type: types.DepBlocks},
		}
		for _, dep := range setup {
			if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
				t.Fatalf("setup dep %+v: %v", dep, err)
			}
		}

		// Closing edge: T1 is a child of E2 -> would form the same combined
		// cycle. The cycle detector now also runs on parent-child inserts.
		err := te.store.AddDependency(ctx, &types.Dependency{
			IssueID:     "cp-t1",
			DependsOnID: "cp-e2",
			Type:        types.DepParentChild,
		}, "tester")
		if err == nil {
			t.Fatal("expected cycle rejection for combined-graph loop via parent-child closing edge")
		}
		if !strings.Contains(err.Error(), "cycle") {
			t.Errorf("expected cycle error, got: %v", err)
		}
	})

	t.Run("deep_linear_chain_allowed", func(t *testing.T) {
		// Linear chain: T_i blocks E_i and T_i is a child of E_{i+1}.
		// Each blocks edge and each parent-child edge introduces an unrelated
		// pair; the combined graph stays acyclic so every insert succeeds.
		te := newTestEnv(t, "dl")
		ctx := t.Context()

		const levels = 5
		for i := 1; i <= levels; i++ {
			epic := &types.Issue{
				ID: "dl-e" + string(rune('0'+i)), Title: "E", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic,
			}
			task := &types.Issue{
				ID: "dl-t" + string(rune('0'+i)), Title: "T", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			}
			if err := te.store.CreateIssue(ctx, epic, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", epic.ID, err)
			}
			if err := te.store.CreateIssue(ctx, task, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", task.ID, err)
			}
		}
		for i := 1; i <= levels; i++ {
			ei := "dl-e" + string(rune('0'+i))
			ti := "dl-t" + string(rune('0'+i))
			if err := te.store.AddDependency(ctx, &types.Dependency{
				IssueID: ei, DependsOnID: ti, Type: types.DepBlocks,
			}, "tester"); err != nil {
				t.Fatalf("blocks %s<-%s: %v", ei, ti, err)
			}
			if i+1 <= levels {
				parent := "dl-e" + string(rune('0'+i+1))
				if err := te.store.AddDependency(ctx, &types.Dependency{
					IssueID: ti, DependsOnID: parent, Type: types.DepParentChild,
				}, "tester"); err != nil {
					t.Fatalf("parent-child %s->%s: %v", ti, parent, err)
				}
			}
		}
	})
}
