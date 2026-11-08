//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// runBatchScriptInTx is a tiny helper that mirrors what batchCmd.RunE does,
// minus the cobra/flag plumbing, so tests can drive batch execution against
// a *dolt.DoltStore without spawning a 'bd' subprocess.
func runBatchScriptInTx(t *testing.T, ctx context.Context, st storage.DoltStorage, script string) error {
	t.Helper()
	ops, err := parseBatchScript(strings.NewReader(script))
	if err != nil {
		return err
	}
	return st.RunInTransaction(ctx, "test: bd batch", func(tx storage.Transaction) error {
		for _, op := range ops {
			if _, err := runBatchOp(ctx, tx, op); err != nil {
				return err
			}
		}
		return nil
	})
}

// seedBatchTestIssues creates three open issues for batch tests to operate on.
func seedBatchTestIssues(t *testing.T, ctx context.Context, st storage.DoltStorage, ids ...string) {
	t.Helper()
	for _, id := range ids {
		issue := &types.Issue{
			ID:        id,
			Title:     "seed " + id,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := st.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("seed CreateIssue %s: %v", id, err)
		}
	}
}

// TestBatch_AppliesAllInOneTransaction verifies that a batch with several
// supported operations commits atomically and all writes are visible
// afterwards.
func TestBatch_AppliesAllInOneTransaction(t *testing.T) {
	tmpDir := t.TempDir()
	st := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "tb")
	ctx := context.Background()

	seedBatchTestIssues(t, ctx, st, "tb-1", "tb-2", "tb-3")

	script := `# batch test: close one, update one, link two
close tb-1 done in batch
update tb-2 status=in_progress priority=1
dep add tb-3 tb-2
`
	if err := runBatchScriptInTx(t, ctx, st, script); err != nil {
		t.Fatalf("batch run: %v", err)
	}

	// Verify the close
	got1, err := st.GetIssue(ctx, "tb-1")
	if err != nil {
		t.Fatalf("GetIssue tb-1: %v", err)
	}
	if got1.Status != types.StatusClosed {
		t.Errorf("tb-1 status = %q, want closed", got1.Status)
	}

	// Verify the update
	got2, err := st.GetIssue(ctx, "tb-2")
	if err != nil {
		t.Fatalf("GetIssue tb-2: %v", err)
	}
	if string(got2.Status) != "in_progress" {
		t.Errorf("tb-2 status = %q, want in_progress", got2.Status)
	}
	if got2.Priority != 1 {
		t.Errorf("tb-2 priority = %d, want 1", got2.Priority)
	}

	// Verify the dependency
	deps, err := st.GetDependencies(ctx, "tb-3")
	if err != nil {
		t.Fatalf("GetDependencies tb-3: %v", err)
	}
	found := false
	for _, d := range deps {
		if d.ID == "tb-2" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected tb-3 to depend on tb-2, got %+v", deps)
	}
}

// TestBatch_DepAddAliasGatesReadyWork pins bd-3073gm (GH#5560) on the classic
// backend: a `dep add` line spelling the documented blocked-by or depends-on
// alias stores the canonical blocks edge, which keeps its source out of ready
// work while the target is open.
func TestBatch_DepAddAliasGatesReadyWork(t *testing.T) {
	tmpDir := t.TempDir()
	st := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "tba")
	ctx := context.Background()

	seedBatchTestIssues(t, ctx, st, "tba-1", "tba-2", "tba-3")

	script := `dep add tba-1 tba-3 blocked-by
dep add tba-2 tba-3 depends-on
`
	if err := runBatchScriptInTx(t, ctx, st, script); err != nil {
		t.Fatalf("batch run: %v", err)
	}

	for _, from := range []string{"tba-1", "tba-2"} {
		recs, err := st.GetDependencyRecords(ctx, from)
		if err != nil {
			t.Fatalf("GetDependencyRecords %s: %v", from, err)
		}
		if len(recs) != 1 || recs[0].DependsOnID != "tba-3" || recs[0].Type != types.DepBlocks {
			for _, r := range recs {
				t.Logf("%s -> %s (%s)", r.IssueID, r.DependsOnID, r.Type)
			}
			t.Errorf("%s has %d dependencies, want one blocks edge to tba-3", from, len(recs))
		}
	}
	ready, err := st.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	for _, issue := range ready {
		if issue.ID == "tba-1" || issue.ID == "tba-2" {
			t.Errorf("%s is ready while its alias-spelled blocker tba-3 is open", issue.ID)
		}
	}
}

// TestBatch_DepAddUnknownTypeRollsBack pins bd-3073gm (GH#5560): a `dep add`
// line naming a type outside `bd dep add`'s vocabulary (a typo, or a custom
// type) is refused with the accepted types, and the earlier lines of the
// batch are rolled back with it.
func TestBatch_DepAddUnknownTypeRollsBack(t *testing.T) {
	for _, bad := range []string{"blcoks", "owned-by"} {
		t.Run(bad, func(t *testing.T) {
			tmpDir := t.TempDir()
			st := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "tbu")
			ctx := context.Background()

			seedBatchTestIssues(t, ctx, st, "tbu-1", "tbu-2")

			err := runBatchScriptInTx(t, ctx, st, "update tbu-1 priority=0\ndep add tbu-1 tbu-2 "+bad+"\n")
			if err == nil {
				t.Fatalf("batch with dep add type %q succeeded, want a refusal", bad)
			}
			if !strings.Contains(err.Error(), `"`+bad+`"`) || !strings.Contains(err.Error(), "valid types:") {
				t.Errorf("refusal = %v, want it to name %q and the accepted types", err, bad)
			}
			got, gerr := st.GetIssue(ctx, "tbu-1")
			if gerr != nil {
				t.Fatalf("GetIssue tbu-1: %v", gerr)
			}
			if got.Priority != 2 {
				t.Errorf("tbu-1 priority = %d, want 2: the refused batch must roll back", got.Priority)
			}
			recs, rerr := st.GetDependencyRecords(ctx, "tbu-1")
			if rerr != nil {
				t.Fatalf("GetDependencyRecords tbu-1: %v", rerr)
			}
			for _, r := range recs {
				t.Errorf("refused batch left %s -> %s (%s)", r.IssueID, r.DependsOnID, r.Type)
			}
		})
	}
}

// TestBatch_RollbackOnError verifies that if any op in the batch fails the
// entire transaction is rolled back and earlier writes are not visible.
//
// The trigger here is `dep add` referencing nonexistent issue IDs, which
// fails the foreign-key constraint on the dependencies table.
func TestBatch_RollbackOnError(t *testing.T) {
	tmpDir := t.TempDir()
	st := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "tbr")
	ctx := context.Background()

	seedBatchTestIssues(t, ctx, st, "tbr-1", "tbr-2")

	// Op 1 succeeds (close tbr-1), op 2 succeeds (update tbr-2), op 3
	// references nonexistent IDs and must fail (FK violation). The whole tx
	// should roll back; tbr-1 must remain open and tbr-2 must remain P2.
	script := `close tbr-1 should-roll-back
update tbr-2 priority=0
dep add tbr-DOES-NOT-EXIST tbr-ALSO-MISSING
`
	err := runBatchScriptInTx(t, ctx, st, script)
	if err == nil {
		t.Fatal("expected batch to fail because of foreign key violation")
	}

	// tbr-1 should still be open
	got1, gerr := st.GetIssue(ctx, "tbr-1")
	if gerr != nil {
		t.Fatalf("GetIssue tbr-1: %v", gerr)
	}
	if got1.Status == types.StatusClosed {
		t.Errorf("tbr-1 was closed despite rollback (status=%q)", got1.Status)
	}

	// tbr-2 should still be P2
	got2, gerr := st.GetIssue(ctx, "tbr-2")
	if gerr != nil {
		t.Fatalf("GetIssue tbr-2: %v", gerr)
	}
	if got2.Priority != 2 {
		t.Errorf("tbr-2 priority = %d, want 2 (rollback)", got2.Priority)
	}
}

// TestBatch_EmptyScriptIsNoOp verifies that an empty input is treated as a
// successful no-op (matches `bd list ... | bd batch` with an empty pipeline).
func TestBatch_EmptyScriptIsNoOp(t *testing.T) {
	tmpDir := t.TempDir()
	st := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "tbe")
	ctx := context.Background()

	if err := runBatchScriptInTx(t, ctx, st, ""); err != nil {
		t.Errorf("empty script: %v", err)
	}
	if err := runBatchScriptInTx(t, ctx, st, "# only a comment\n\n"); err != nil {
		t.Errorf("comment-only script: %v", err)
	}
}

// TestBatch_UnsupportedCommandFailsBeforeWrites verifies that an unknown
// command anywhere in the input causes a parse-time failure with a clear
// message and no operations are executed (the test confirms the error
// surfaces before any writes hit the database).
func TestBatch_UnsupportedCommandFailsBeforeWrites(t *testing.T) {
	tmpDir := t.TempDir()
	st := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "tbu")
	ctx := context.Background()

	seedBatchTestIssues(t, ctx, st, "tbu-1")

	script := `close tbu-1 will-not-happen
show tbu-1
`
	err := runBatchScriptInTx(t, ctx, st, script)
	if err == nil {
		t.Fatal("expected error for unsupported command")
	}
	if !strings.Contains(err.Error(), "unsupported batch command") {
		t.Errorf("error should mention unsupported command, got: %v", err)
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error should reference the offending line, got: %v", err)
	}

	// Confirm tbu-1 is still open (parse failed before any tx ran).
	got, gerr := st.GetIssue(ctx, "tbu-1")
	if gerr != nil {
		t.Fatalf("GetIssue tbu-1: %v", gerr)
	}
	if got.Status == types.StatusClosed {
		t.Errorf("tbu-1 was closed even though parse should have failed first")
	}
}

// TestBatch_DepRemoveInBatch verifies the dep.remove path inside a batch.
func TestBatch_DepRemoveInBatch(t *testing.T) {
	tmpDir := t.TempDir()
	st := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "tbd")
	ctx := context.Background()

	seedBatchTestIssues(t, ctx, st, "tbd-1", "tbd-2")
	if err := st.AddDependency(ctx, &types.Dependency{
		IssueID: "tbd-1", DependsOnID: "tbd-2", Type: types.DepBlocks,
	}, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	if err := runBatchScriptInTx(t, ctx, st, "dep remove tbd-1 tbd-2\n"); err != nil {
		t.Fatalf("batch dep remove: %v", err)
	}

	deps, err := st.GetDependencies(ctx, "tbd-1")
	if err != nil {
		t.Fatalf("GetDependencies: %v", err)
	}
	for _, d := range deps {
		if d.ID == "tbd-2" {
			t.Errorf("dependency tbd-1 -> tbd-2 still present after dep remove")
		}
	}
}

// TestBatch_RollbackTriggerStillFailsAtStorageLayer is a guard against the
// rollback test silently passing if a future change makes
// tx.AddDependency tolerate missing issue IDs. If this test fails (i.e.
// AddDependency stops returning an error for unknown IDs), the rollback test
// must be rewritten to use a different failure trigger.
func TestBatch_RollbackTriggerStillFailsAtStorageLayer(t *testing.T) {
	tmpDir := t.TempDir()
	st := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "tbg")
	ctx := context.Background()

	err := st.RunInTransaction(ctx, "test: trigger guard", func(tx storage.Transaction) error {
		return tx.AddDependency(ctx, &types.Dependency{
			IssueID:     "tbg-MISSING-A",
			DependsOnID: "tbg-MISSING-B",
			Type:        types.DepBlocks,
		}, "test")
	})
	if err == nil {
		t.Fatal("expected AddDependency on missing IDs to fail; if not, rewrite TestBatch_RollbackOnError")
	}
}
