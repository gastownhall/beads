//go:build cgo

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// wispTestProto creates a proto epic with the given number of parent-child
// children in the store, returning the root proto's ID. Used to exercise
// wisp DAG fanout.
func wispTestProto(t *testing.T, ctx context.Context, s *dolt.DoltStore, numChildren int) string {
	t.Helper()
	root := &types.Issue{
		Title:     "Proto Root",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		Labels:    []string{MoleculeLabel},
	}
	if err := s.CreateIssue(ctx, root, "test"); err != nil {
		t.Fatalf("Failed to create proto root: %v", err)
	}
	for i := 1; i <= numChildren; i++ {
		child := &types.Issue{
			Title:     fmt.Sprintf("Step %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := s.CreateIssue(ctx, child, "test"); err != nil {
			t.Fatalf("Failed to create step %d: %v", i, err)
		}
		if err := s.AddDependency(ctx, &types.Dependency{
			IssueID:     child.ID,
			DependsOnID: root.ID,
			Type:        types.DepParentChild,
		}, "test"); err != nil {
			t.Fatalf("Failed to add dependency for step %d: %v", i, err)
		}
	}
	return root.ID
}

// makeWispTestCmd builds a cobra.Command with the same flag schema as the
// real `bd mol wisp` command, with the given flag values set as defaults
// (so runWispCreate reads them back without needing arg parsing).
func makeWispTestCmd(rootOnly, dryRun bool) *cobra.Command {
	c := &cobra.Command{Use: "wisp"}
	c.Flags().StringArray("var", []string{}, "")
	c.Flags().Bool("dry-run", dryRun, "")
	c.Flags().Bool("root-only", rootOnly, "")
	return c
}

// countEphemeral returns the number of issues in the store with Ephemeral=true.
func countEphemeral(t *testing.T, ctx context.Context, s *dolt.DoltStore) int {
	t.Helper()
	tru := true
	results, err := s.SearchIssues(ctx, "", types.IssueFilter{Ephemeral: &tru})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	return len(results)
}

// withWispTestGlobals saves and restores the package-level store/rootCtx/actor
// globals around a test, since runWispCreate reads them directly.
func withWispTestGlobals(t *testing.T, s *dolt.DoltStore, ctx context.Context) {
	t.Helper()
	oldStore, oldCtx, oldActor := store, rootCtx, actor
	t.Cleanup(func() { store, rootCtx, actor = oldStore, oldCtx, oldActor })
	store, rootCtx, actor = s, ctx, "test"
}

// TestWispCreateMaterializesChildDAG is the regression test for GH#3872.
// Before the fix, wisps were silently forced to root-only unless the
// formula set pour=true, making --root-only a no-op flag and breaking
// ephemeral lifecycle testing of multi-step formulas. After the fix,
// `bd mol wisp <proto>` materializes the full child DAG by default,
// just marked Ephemeral=true so it doesn't sync via git.
func TestWispCreateMaterializesChildDAG(t *testing.T) {
	ctx := context.Background()
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	rootID := wispTestProto(t, ctx, s, 2)

	withWispTestGlobals(t, s, ctx)

	_ = captureStdout(t, func() error {
		runWispCreate(makeWispTestCmd(false, false), []string{rootID})
		return nil
	})

	// Proto root + 2 children = 3 source issues, all materialized as wisp
	// copies with Ephemeral=true. The original proto issues are persistent
	// (Ephemeral=false), so counting ephemeral issues gives us exactly the
	// wisp set.
	if got := countEphemeral(t, ctx, s); got != 3 {
		t.Errorf("expected 3 ephemeral wisp issues (root + 2 children), got %d", got)
	}
}

// TestWispCreateRootOnly verifies that --root-only opts out of child
// materialization while still creating the root as ephemeral. Before the
// GH#3872 fix this was the silent default for all vapor formulas; after
// the fix it must be an explicit opt-in via the flag.
func TestWispCreateRootOnly(t *testing.T) {
	ctx := context.Background()
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	rootID := wispTestProto(t, ctx, s, 2)

	withWispTestGlobals(t, s, ctx)

	_ = captureStdout(t, func() error {
		runWispCreate(makeWispTestCmd(true, false), []string{rootID})
		return nil
	})

	if got := countEphemeral(t, ctx, s); got != 1 {
		t.Errorf("expected 1 ephemeral wisp issue (root only), got %d", got)
	}
}

// TestWispCreateDryRunFanoutMessage verifies the dry-run printout reflects
// full DAG materialization by default and switches to "root only" wording
// only under --root-only. Catches regressions in user-facing messaging.
func TestWispCreateDryRunFanoutMessage(t *testing.T) {
	ctx := context.Background()
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	rootID := wispTestProto(t, ctx, s, 2)

	withWispTestGlobals(t, s, ctx)

	t.Run("default fans out", func(t *testing.T) {
		output := captureStdout(t, func() error {
			runWispCreate(makeWispTestCmd(false, true), []string{rootID})
			return nil
		})
		if !strings.Contains(output, "would create wisp with 3 issues") {
			t.Errorf("dry-run should mention 3 issues (root + 2 children), got:\n%s", output)
		}
		if strings.Contains(output, "root only") {
			t.Errorf("dry-run without --root-only should NOT say 'root only', got:\n%s", output)
		}
	})

	t.Run("root-only shows opt-out wording", func(t *testing.T) {
		output := captureStdout(t, func() error {
			runWispCreate(makeWispTestCmd(true, true), []string{rootID})
			return nil
		})
		if !strings.Contains(output, "1 issue (root only)") {
			t.Errorf("dry-run with --root-only should mention 1 root issue, got:\n%s", output)
		}
		if !strings.Contains(output, "--root-only") {
			t.Errorf("skip message should reference --root-only flag, got:\n%s", output)
		}
	})
}
