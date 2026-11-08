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
	c.Flags().String("wisp-type", "", "")
	return c
}

// countEphemeral returns the number of issues in the store with Ephemeral=true.
func countEphemeral(t *testing.T, ctx context.Context, s *dolt.DoltStore) int {
	t.Helper()
	return len(ephemeralIssues(t, ctx, s))
}

// ephemeralIssues returns the store's Ephemeral=true issues, wisp_type
// included, so a test can assert both the created set and its typing.
func ephemeralIssues(t *testing.T, ctx context.Context, s *dolt.DoltStore) []*types.Issue {
	t.Helper()
	tru := true
	results, err := s.SearchIssues(ctx, "", types.IssueFilter{Ephemeral: &tru})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	return results
}

// runWispCreateTyped runs a wisp-creation handler (runWispCreate for
// `bd mol wisp create`, runWisp for the bare spelling) against args with
// --wisp-type set to wt, failing the test on a handler error. Returns the
// captured stdout.
func runWispCreateTyped(t *testing.T, handler func(*cobra.Command, []string) error, cmd *cobra.Command, args []string, wt string) string {
	t.Helper()
	if err := cmd.Flags().Set("wisp-type", wt); err != nil {
		t.Fatalf("set --wisp-type=%q: %v", wt, err)
	}
	var runErr error
	out := captureStdout(t, func() error {
		runErr = handler(cmd, args)
		return nil
	})
	if runErr != nil {
		t.Fatalf("wisp create --wisp-type %s: %v", wt, runErr)
	}
	return out
}

// assertWispRows asserts the store holds exactly want rows with Ephemeral=true,
// every one carrying wisp_type == wt, and that filtering those rows by
// IssueFilter.WispType returns the same ids.
func assertWispRows(t *testing.T, ctx context.Context, s *dolt.DoltStore, want int, wt types.WispType) {
	t.Helper()
	rows := ephemeralIssues(t, ctx, s)
	if len(rows) != want {
		t.Fatalf("expected %d ephemeral issues, got %d", want, len(rows))
	}
	wantIDs := make(map[string]bool, len(rows))
	for _, issue := range rows {
		if issue.WispType != wt {
			t.Errorf("wisp %s has wisp_type %q, want %q", issue.ID, issue.WispType, wt)
		}
		wantIDs[issue.ID] = true
	}
	if wt == "" {
		return // the zero value is not a filterable type
	}
	tru := true
	typed, err := s.SearchIssues(ctx, "", types.IssueFilter{Ephemeral: &tru, WispType: &wt})
	if err != nil {
		t.Fatalf("SearchIssues by wisp type: %v", err)
	}
	if len(typed) != want {
		t.Fatalf("wisp-type filter returned %d issues, want %d", len(typed), want)
	}
	for _, issue := range typed {
		if !wantIDs[issue.ID] {
			t.Errorf("wisp-type filter returned %s, which is not one of the %d created rows", issue.ID, want)
		}
	}
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

// TestWispCreateStampsWispType verifies that --wisp-type stamps the supplied
// type on every issue a wisp creation makes - the root and each materialized
// child step - on both spellings of the command (GH#8209: without the flag,
// wisps created through `bd mol wisp` were permanently untyped, so TTL
// compaction categories and wisp-type filters saw nothing from them).
func TestWispCreateStampsWispType(t *testing.T) {
	// newWispTypeFixture builds an isolated store holding one proto with a
	// root and two child steps, and points the package globals at it.
	newWispTypeFixture := func(t *testing.T) (context.Context, *dolt.DoltStore, string) {
		ctx := context.Background()
		s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
		rootID := wispTestProto(t, ctx, s, 2)
		withWispTestGlobals(t, s, ctx)
		return ctx, s, rootID
	}

	t.Run("create stamps root and children", func(t *testing.T) {
		ctx, s, rootID := newWispTypeFixture(t)
		runWispCreateTyped(t, runWispCreate, makeWispTestCmd(false, false), []string{rootID}, string(types.WispTypePatrol))
		assertWispRows(t, ctx, s, 3, types.WispTypePatrol)
	})

	t.Run("bare wisp command stamps root and children", func(t *testing.T) {
		ctx, s, rootID := newWispTypeFixture(t)
		runWispCreateTyped(t, runWisp, makeWispTestCmd(false, false), []string{rootID}, string(types.WispTypeHeartbeat))
		assertWispRows(t, ctx, s, 3, types.WispTypeHeartbeat)
	})

	t.Run("create root-only stamps the root", func(t *testing.T) {
		ctx, s, rootID := newWispTypeFixture(t)
		runWispCreateTyped(t, runWispCreate, makeWispTestCmd(true, false), []string{rootID}, string(types.WispTypePatrol))
		assertWispRows(t, ctx, s, 1, types.WispTypePatrol)
	})

	t.Run("typed dry run writes nothing", func(t *testing.T) {
		ctx, s, rootID := newWispTypeFixture(t)
		out := runWispCreateTyped(t, runWispCreate, makeWispTestCmd(false, true), []string{rootID}, string(types.WispTypePatrol))
		if !strings.Contains(out, "Dry run") {
			t.Errorf("expected dry-run preview, got:\n%s", out)
		}
		if got := countEphemeral(t, ctx, s); got != 0 {
			t.Errorf("dry run created %d issues, want 0", got)
		}
	})
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
