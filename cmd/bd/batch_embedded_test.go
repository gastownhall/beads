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
