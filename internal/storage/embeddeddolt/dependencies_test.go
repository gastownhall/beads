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

	t.Run("cross_type_validation", func(t *testing.T) {
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

		// Task blocking epic should fail.
		dep := &types.Dependency{IssueID: "ct-task", DependsOnID: "ct-epic", Type: types.DepBlocks}
		err := te.store.AddDependency(ctx, dep, "tester")
		if err == nil {
			t.Fatal("expected cross-type error")
		}
		if !strings.Contains(err.Error(), "epics") {
			t.Errorf("expected cross-type error message, got: %v", err)
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

		// Parent-child between epic and task should succeed (cross-type restriction
		// only applies to blocks deps).
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

	t.Run("epic_blocks_task_fails", func(t *testing.T) {
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

		// Epic blocking task should fail (reverse direction of existing cross_type_validation test).
		dep := &types.Dependency{IssueID: "et-epic", DependsOnID: "et-task", Type: types.DepBlocks}
		err := te.store.AddDependency(ctx, dep, "tester")
		if err == nil {
			t.Fatal("expected cross-type error")
		}
		if !strings.Contains(err.Error(), "epics") {
			t.Errorf("expected cross-type error message, got: %v", err)
		}
	})
}
