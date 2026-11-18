package dolt

import (
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestChangedIssueIDsInTxContract runs the dolt_diff change-set query against
// a real dolt database on a plain *sql.DB (the DBTX surface the
// proxied-server maintenance path hands it, bd-todbq7), pinning the three
// behaviors auto-export leans on: the four-table UNION (an issue touched only
// through labels/comments/dependencies still reports), the issues-table-only
// Removed verdict, and the literal "WORKING" endpoint that makes the diff see
// uncommitted working-set writes.
func TestChangedIssueIdsInTxContract(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	create := func(id string) {
		t.Helper()
		if err := store.CreateIssue(ctx, &types.Issue{
			ID:        id,
			Title:     id,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	create("bd-ci1")
	create("bd-ci3")
	if err := store.Commit(ctx, "base"); err != nil {
		t.Fatalf("commit base: %v", err)
	}
	c1, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("current commit after base: %v", err)
	}

	// One change per unioned table between c1 and c2: an issues-table update
	// (bd-ci1), an issues-table create (bd-ci2), an issues-table delete
	// (bd-ci3), a labels row (bd-ci1), a comments row (bd-ci2), and a
	// dependencies row (bd-ci2 -> bd-ci1).
	create("bd-ci2")
	if err := store.UpdateIssue(ctx, "bd-ci1", map[string]any{"title": "renamed"}, "tester"); err != nil {
		t.Fatalf("update bd-ci1: %v", err)
	}
	if err := store.AddLabel(ctx, "bd-ci1", "probe", "tester"); err != nil {
		t.Fatalf("label bd-ci1: %v", err)
	}
	if err := store.AddComment(ctx, "bd-ci2", "tester", "a comment"); err != nil {
		t.Fatalf("comment bd-ci2: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "bd-ci2", DependsOnID: "bd-ci1", Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("dependency bd-ci2 -> bd-ci1: %v", err)
	}
	if err := store.DeleteIssue(ctx, "bd-ci3"); err != nil {
		t.Fatalf("delete bd-ci3: %v", err)
	}
	if err := store.Commit(ctx, "changes"); err != nil {
		t.Fatalf("commit changes: %v", err)
	}
	c2, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("current commit after changes: %v", err)
	}

	changed, err := issueops.ChangedIssueIDsInTx(ctx, store.db, c1, c2)
	if err != nil {
		t.Fatalf("ChangedIssueIDsInTx(%s..%s): %v", c1, c2, err)
	}
	sort.Strings(changed.Upserted)
	if strings.Join(changed.Upserted, ",") != "bd-ci1,bd-ci2" {
		t.Fatalf("Upserted = %v, want [bd-ci1 bd-ci2] (issues-table change, plus comment/dependency rows under the same ids)", changed.Upserted)
	}
	if strings.Join(changed.Removed, ",") != "bd-ci3" {
		t.Fatalf("Removed = %v, want [bd-ci3] (only the issues-table delete reports removed)", changed.Removed)
	}

	// The "WORKING" endpoint must see an uncommitted working-set write —
	// this is the leg auto-export's incremental path depends on under
	// dolt auto-commit=batch/off, where HEAD never advances.
	create("bd-ci4")
	changed, err = issueops.ChangedIssueIDsInTx(ctx, store.db, c2, "WORKING")
	if err != nil {
		t.Fatalf("ChangedIssueIDsInTx(%s..WORKING): %v", c2, err)
	}
	foundNew := false
	for _, id := range changed.Upserted {
		if id == "bd-ci4" {
			foundNew = true
		}
	}
	if !foundNew {
		t.Fatalf("WORKING diff Upserted = %v, want it to contain the uncommitted bd-ci4", changed.Upserted)
	}

	// An unsafe ref is refused before any statement runs: dolt_diff takes no
	// bind parameters, so the literal validation is the injection guard.
	if _, err := issueops.ChangedIssueIDsInTx(ctx, store.db, c1, "x'; DROP TABLE issues; --"); err == nil {
		t.Fatal("ChangedIssueIDsInTx accepted an unsafe ref; want refusal")
	}
}
