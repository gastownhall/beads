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

// makeWispTestCmdWithType is makeWispTestCmd with --wisp-type set to wispType
// through the flag API, as a parsed command line would.
func makeWispTestCmdWithType(t *testing.T, rootOnly, dryRun bool, wispType string) *cobra.Command {
	t.Helper()
	c := makeWispTestCmd(rootOnly, dryRun)
	if err := c.Flags().Set("wisp-type", wispType); err != nil {
		t.Fatalf("set --wisp-type=%q: %v", wispType, err)
	}
	return c
}

// ephemeralIssues returns every issue in the store with Ephemeral=true.
func ephemeralIssues(t *testing.T, ctx context.Context, s *dolt.DoltStore) []*types.Issue {
	t.Helper()
	tru := true
	results, err := s.SearchIssues(ctx, "", types.IssueFilter{Ephemeral: &tru})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	return results
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

// TestWispCommandsRegisterWispTypeFlag checks the real wisp creation commands
// expose --wisp-type as an empty-by-default string, so a wisp instantiated
// from a proto can carry a type the way `bd create --ephemeral` does (GH#8209).
func TestWispCommandsRegisterWispTypeFlag(t *testing.T) {
	for _, c := range []struct {
		name string
		cmd  *cobra.Command
	}{
		{"bd mol wisp <proto>", wispCmd},
		{"bd mol wisp create <proto>", wispCreateCmd},
	} {
		f := c.cmd.Flags().Lookup("wisp-type")
		if f == nil {
			t.Errorf("%s: no --wisp-type flag", c.name)
			continue
		}
		if f.Value.Type() != "string" {
			t.Errorf("%s: --wisp-type type = %s, want string", c.name, f.Value.Type())
		}
		if f.DefValue != "" {
			t.Errorf("%s: --wisp-type default = %q, want empty", c.name, f.DefValue)
		}
	}
}

// TestWispCreateStampsWispType verifies `bd mol wisp create --wisp-type`
// stamps the type on every issue the wisp creates: the root and each child
// step (GH#8209).
func TestWispCreateStampsWispType(t *testing.T) {
	t.Run("root and children", func(t *testing.T) {
		ctx := context.Background()
		s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
		rootID := wispTestProto(t, ctx, s, 2)
		withWispTestGlobals(t, s, ctx)

		var runErr error
		_ = captureStdout(t, func() error {
			runErr = runWispCreate(makeWispTestCmdWithType(t, false, false, "patrol"), []string{rootID})
			return nil
		})
		if runErr != nil {
			t.Fatalf("runWispCreate --wisp-type patrol: %v", runErr)
		}

		got := ephemeralIssues(t, ctx, s)
		if len(got) != 3 {
			t.Fatalf("expected 3 ephemeral wisp issues (root + 2 children), got %d", len(got))
		}
		ids := map[string]bool{}
		for _, issue := range got {
			ids[issue.ID] = true
			if issue.WispType != types.WispTypePatrol {
				t.Errorf("%s (%q): wisp_type = %q, want %q", issue.ID, issue.Title, issue.WispType, types.WispTypePatrol)
			}
		}

		tru, patrol := true, types.WispTypePatrol
		typed, err := s.SearchIssues(ctx, "", types.IssueFilter{Ephemeral: &tru, WispType: &patrol})
		if err != nil {
			t.Fatalf("SearchIssues by wisp type: %v", err)
		}
		if len(typed) != len(ids) {
			t.Errorf("filter wisp_type=patrol matched %d issues, want %d", len(typed), len(ids))
		}
		for _, issue := range typed {
			if !ids[issue.ID] {
				t.Errorf("filter wisp_type=patrol matched unexpected issue %s", issue.ID)
			}
		}
	})

	t.Run("root-only", func(t *testing.T) {
		ctx := context.Background()
		s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
		rootID := wispTestProto(t, ctx, s, 2)
		withWispTestGlobals(t, s, ctx)

		var runErr error
		_ = captureStdout(t, func() error {
			runErr = runWispCreate(makeWispTestCmdWithType(t, true, false, "patrol"), []string{rootID})
			return nil
		})
		if runErr != nil {
			t.Fatalf("runWispCreate --root-only --wisp-type patrol: %v", runErr)
		}

		got := ephemeralIssues(t, ctx, s)
		if len(got) != 1 {
			t.Fatalf("expected 1 ephemeral wisp issue (root only), got %d", len(got))
		}
		if got[0].WispType != types.WispTypePatrol {
			t.Errorf("root wisp_type = %q, want %q", got[0].WispType, types.WispTypePatrol)
		}
	})

	t.Run("dry-run writes nothing", func(t *testing.T) {
		ctx := context.Background()
		s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
		rootID := wispTestProto(t, ctx, s, 2)
		withWispTestGlobals(t, s, ctx)

		var runErr error
		output := captureStdout(t, func() error {
			runErr = runWispCreate(makeWispTestCmdWithType(t, false, true, "patrol"), []string{rootID})
			return nil
		})
		if runErr != nil {
			t.Fatalf("runWispCreate --dry-run --wisp-type patrol: %v", runErr)
		}
		if !strings.Contains(output, "would create wisp with 3 issues") {
			t.Errorf("dry-run should preview 3 issues, got:\n%s", output)
		}
		if got := countEphemeral(t, ctx, s); got != 0 {
			t.Errorf("dry-run created %d ephemeral issues, want 0", got)
		}
	})
}

// TestBareWispStampsWispType drives the real bare `bd mol wisp <proto>`
// command: its own flag set must accept --wisp-type, and every issue it
// creates must carry the type (GH#8209).
func TestBareWispStampsWispType(t *testing.T) {
	ctx := context.Background()
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
	rootID := wispTestProto(t, ctx, s, 2)
	withWispTestGlobals(t, s, ctx)

	t.Cleanup(func() {
		if f := wispCmd.Flags().Lookup("wisp-type"); f != nil {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		}
	})
	if err := wispCmd.ParseFlags([]string{"--wisp-type", "heartbeat"}); err != nil {
		t.Fatalf("bd mol wisp %s --wisp-type heartbeat: %v", rootID, err)
	}

	var runErr error
	_ = captureStdout(t, func() error {
		runErr = runWisp(wispCmd, []string{rootID})
		return nil
	})
	if runErr != nil {
		t.Fatalf("runWisp --wisp-type heartbeat: %v", runErr)
	}

	got := ephemeralIssues(t, ctx, s)
	if len(got) != 3 {
		t.Fatalf("expected 3 ephemeral wisp issues (root + 2 children), got %d", len(got))
	}
	for _, issue := range got {
		if issue.WispType != types.WispTypeHeartbeat {
			t.Errorf("%s (%q): wisp_type = %q, want %q", issue.ID, issue.Title, issue.WispType, types.WispTypeHeartbeat)
		}
	}
}

// TestWispCreateRejectsInvalidWispType verifies both wisp creation entrypoints
// reject a --wisp-type outside the enum with bd create's diagnostic, before
// any preview or write, and accept every enum value and empty (GH#8209).
func TestWispCreateRejectsInvalidWispType(t *testing.T) {
	ctx := context.Background()
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
	rootID := wispTestProto(t, ctx, s, 2)
	withWispTestGlobals(t, s, ctx)

	handlers := []struct {
		name string
		run  func(*cobra.Command, []string) error
	}{
		{"bd mol wisp create", runWispCreate},
		{"bd mol wisp", runWisp},
	}

	for _, h := range handlers {
		for _, value := range []string{"bogus", "default"} {
			for _, dryRun := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/dry-run=%v", h.name, value, dryRun), func(t *testing.T) {
					var runErr error
					stdout, stderr := captureStdio(t, func() {
						runErr = h.run(makeWispTestCmdWithType(t, false, dryRun, value), []string{rootID})
					})
					if runErr == nil {
						t.Fatalf("--wisp-type %s: expected an error, got none (stdout:\n%s)", value, stdout)
					}
					want := fmt.Sprintf("invalid wisp-type %q (must be %s)", value, types.ValidWispTypeNames())
					if !strings.Contains(stderr, want) {
						t.Errorf("stderr = %q, want it to contain %q", stderr, want)
					}
					if strings.Contains(stdout, "Dry run") {
						t.Errorf("invalid --wisp-type must not print a dry-run preview, got:\n%s", stdout)
					}
					if got := countEphemeral(t, ctx, s); got != 0 {
						t.Errorf("invalid --wisp-type created %d ephemeral issues, want 0", got)
					}
				})
			}
		}
	}

	valid := []string{""}
	for _, wt := range []types.WispType{
		types.WispTypeHeartbeat, types.WispTypePing, types.WispTypePatrol, types.WispTypeGCReport,
		types.WispTypeRecovery, types.WispTypeError, types.WispTypeEscalation,
	} {
		valid = append(valid, string(wt))
	}
	for _, value := range valid {
		t.Run(fmt.Sprintf("valid/%q", value), func(t *testing.T) {
			var runErr error
			stdout, stderr := captureStdio(t, func() {
				runErr = runWispCreate(makeWispTestCmdWithType(t, false, true, value), []string{rootID})
			})
			if runErr != nil {
				t.Fatalf("--wisp-type %q rejected: %v\n%s", value, runErr, stderr)
			}
			if !strings.Contains(stdout, "Dry run") {
				t.Errorf("--wisp-type %q: expected a dry-run preview, got:\n%s", value, stdout)
			}
		})
	}
}

// TestWispCreateWithoutWispTypeStaysUntyped is the regression guard for
// callers that do not pass --wisp-type: the same root and child issues are
// created as before, each with an empty wisp_type (GH#8209).
func TestWispCreateWithoutWispTypeStaysUntyped(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(*cobra.Command, []string) error
		cmd  func(*testing.T) *cobra.Command
	}{
		{"bd mol wisp create omitted", runWispCreate, func(*testing.T) *cobra.Command { return makeWispTestCmd(false, false) }},
		{"bd mol wisp omitted", runWisp, func(*testing.T) *cobra.Command { return makeWispTestCmd(false, false) }},
		{"bd mol wisp create explicit empty", runWispCreate, func(t *testing.T) *cobra.Command { return makeWispTestCmdWithType(t, false, false, "") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
			rootID := wispTestProto(t, ctx, s, 2)
			withWispTestGlobals(t, s, ctx)

			var runErr error
			_ = captureStdout(t, func() error {
				runErr = c.run(c.cmd(t), []string{rootID})
				return nil
			})
			if runErr != nil {
				t.Fatalf("%s: %v", c.name, runErr)
			}

			got := ephemeralIssues(t, ctx, s)
			titles := map[string]bool{}
			for _, issue := range got {
				titles[issue.Title] = true
				if issue.WispType != "" {
					t.Errorf("%s (%q): wisp_type = %q, want empty", issue.ID, issue.Title, issue.WispType)
				}
			}
			if len(got) != 3 || !titles["Proto Root"] || !titles["Step 1"] || !titles["Step 2"] {
				t.Errorf("created %d ephemeral issues %v, want the root and both steps", len(got), titles)
			}
		})
	}
}
