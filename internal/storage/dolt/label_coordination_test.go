package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file pins the label write coordination that closes GH#5657's silent
// lost-update: two transactions that each replace one issue's whole label set
// from the same snapshot cell-merge in Dolt — the shared old label is a
// delete-delete (no conflict) and the two new labels are disjoint insert rows
// — so both commit and the stored set is their union. Rewriting a per-issue
// coordination cell in local_metadata on the label write's OWN transaction
// turns that silent union into a commit-time serialization conflict the
// existing retry loops already replay against a fresh snapshot.
//
// Every racing test here drives the shared issueops execute body through raw
// *sql.Tx values, which are never scoped for version minting or the events
// journal: versioned history is OFF, so an issue_versions collision cannot
// mask (or stand in for) the label-row merge under test.

// labelCoordinationValue reads the coordination cell for one label tier, ""
// when the tier has no cell. The cell's shard is derived from the issue ID,
// so the tier-prefix LIKE finds whatever cell a write landed; on a test's own
// branch or database that is exactly the write under test.
func labelCoordinationValue(t *testing.T, ctx context.Context, store *DoltStore, tier string) string {
	t.Helper()
	var value string
	err := store.db.QueryRowContext(ctx,
		"SELECT value FROM local_metadata WHERE `key` LIKE ?",
		"label-coordination/v1/"+tier+"/%").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("read %s label coordination cell: %v", tier, err)
	}
	return value
}

// readIssueLabels reads one issue's label rows from a label table, sorted.
func readIssueLabels(t *testing.T, ctx context.Context, store *DoltStore, labelTable, issueID string) []string {
	t.Helper()
	rows, err := store.db.QueryContext(ctx,
		fmt.Sprintf("SELECT label FROM %s WHERE issue_id = ? ORDER BY label", labelTable), issueID)
	if err != nil {
		t.Fatalf("read %s rows for %s: %v", labelTable, issueID, err)
	}
	defer rows.Close()
	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			t.Fatalf("scan %s row for %s: %v", labelTable, issueID, err)
		}
		labels = append(labels, label)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s rows for %s: %v", labelTable, issueID, err)
	}
	return labels
}

// beginLabelReplaceTx opens a transaction on its own pooled connection, pins
// its Dolt snapshot by reading the target's seeded label row, and applies one
// whole-set Replace through the shared issueops execute body — the same SQL
// the durable and embedded stores' write paths run. Both racing transactions
// are open and pinned before either commits (the interleaving two overlapping
// `bd update --set-labels` calls hit); the commit order is the caller's, so
// the loser is deterministic, not timing-dependent.
func beginLabelReplaceTx(t *testing.T, ctx context.Context, store *DoltStore, name, labelTable, issueID string, replace []string) (*sql.Tx, func() error) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin %s transaction: %v", name, err)
	}
	// Pin the snapshot before any write: the racing writer must compute its
	// patch against the same starting label rows the winner did.
	var seeded int
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE issue_id = ?", labelTable), issueID).Scan(&seeded); err != nil {
		t.Fatalf("pin %s snapshot: %v", name, err)
	}
	if seeded != 1 {
		t.Fatalf("pin %s snapshot: %d seeded label rows, want 1", name, seeded)
	}
	if _, _, err := storageissueops.ExecuteUpdate(ctx, tx, publicops.UpdateRequest{
		Actor:   "tester",
		IssueID: issueID,
		Patch: publicops.IssuePatch{
			Labels: publicops.LabelPatch{
				Replace: publicops.Field[[]string]{Set: true, Value: replace},
			},
		},
	}); err != nil {
		t.Fatalf("%s: replace labels in transaction: %v", name, err)
	}
	return tx, func() error { return tx.Commit() }
}

// TestLabelPatchConcurrentReplaceConflicts is the durable-plane regression for
// GH#5657 (AC-1): a durable issue labeled {state:a}, versioned history off,
// two SQL transactions read it and apply Replace={state:x} and
// Replace={state:y}; x commits first; y must fail with the retryable
// serialization error and the committed labels must be exactly {state:x}.
func TestLabelPatchConcurrentReplaceConflicts(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "lc-race-durable"
	createPerm(t, ctx, store, id)
	if err := store.AddLabel(ctx, id, "state:a", "tester"); err != nil {
		t.Fatalf("seed state:a: %v", err)
	}

	txX, commitX := beginLabelReplaceTx(t, ctx, store, "winner", "labels", id, []string{"state:x"})
	defer txX.Rollback()
	txY, commitY := beginLabelReplaceTx(t, ctx, store, "loser", "labels", id, []string{"state:y"})
	defer txY.Rollback()

	if err := commitX(); err != nil {
		t.Fatalf("winner commit: %v", err)
	}
	loserErr := commitY()
	if loserErr == nil {
		t.Fatal("loser committed over the winner: two whole-set label replacements cell-merged, leaving both sets reachable")
	}
	if !isSerializationError(loserErr) {
		t.Fatalf("loser commit error = %v, want the retryable serialization conflict", loserErr)
	}

	if got := readIssueLabels(t, ctx, store, "labels", id); !slices.Equal(got, []string{"state:x"}) {
		t.Fatalf("committed labels = %v, want exactly the winner's {state:x}", got)
	}
	if labelCoordinationValue(t, ctx, store, "labels") == "" {
		t.Fatal("no coordination cell on the labels tier: the label write did not participate in its own serialization")
	}
}

// TestWispLabelPatchConcurrentReplaceConflicts is AC-7, the ephemeral twin of
// TestLabelPatchConcurrentReplaceConflicts: an ephemeral wisp labeled
// {state:a}, two server transactions apply conflicting whole-set replacements
// from the same snapshot; the later commit fails with the retryable
// serialization error, the winner's set remains, and the coordination write
// used the wisp_labels tier of the label transaction — a wisp's labels live
// in wisp_labels, so a cell on the durable tier would serialize nothing.
func TestWispLabelPatchConcurrentReplaceConflicts(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "lc-race-wisp"
	createWisp(t, ctx, store, id)
	if err := store.AddLabel(ctx, id, "state:a", "tester"); err != nil {
		t.Fatalf("seed state:a: %v", err)
	}

	txX, commitX := beginLabelReplaceTx(t, ctx, store, "winner", "wisp_labels", id, []string{"state:x"})
	defer txX.Rollback()
	txY, commitY := beginLabelReplaceTx(t, ctx, store, "loser", "wisp_labels", id, []string{"state:y"})
	defer txY.Rollback()

	if err := commitX(); err != nil {
		t.Fatalf("winner commit: %v", err)
	}
	loserErr := commitY()
	if loserErr == nil {
		t.Fatal("loser committed over the winner: two wisp label replacements cell-merged, leaving both sets reachable")
	}
	if !isSerializationError(loserErr) {
		t.Fatalf("loser commit error = %v, want the retryable serialization conflict", loserErr)
	}

	if got := readIssueLabels(t, ctx, store, "wisp_labels", id); !slices.Equal(got, []string{"state:x"}) {
		t.Fatalf("committed wisp labels = %v, want exactly the winner's {state:x}", got)
	}
	if labelCoordinationValue(t, ctx, store, "wisp_labels") == "" {
		t.Fatal("no coordination cell on the wisp_labels tier: the wisp label write did not participate in its own serialization")
	}
	if durable := labelCoordinationValue(t, ctx, store, "labels"); durable != "" {
		t.Fatalf("wisp label write touched the durable tier cell = %q, want the wisp_labels tier only", durable)
	}
}

// addLabelInCommittedTx lands one label write through the shared helper in
// its own committed transaction, the way the store's write paths do.
func addLabelInCommittedTx(t *testing.T, ctx context.Context, store *DoltStore, issueID, label string) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin add-label transaction: %v", err)
	}
	defer tx.Rollback()
	if err := storageissueops.AddLabelInTx(ctx, tx, "", "", issueID, label, "tester"); err != nil {
		t.Fatalf("AddLabelInTx(%s, %s): %v", issueID, label, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit add-label transaction: %v", err)
	}
}

// removeLabelInCommittedTx is addLabelInCommittedTx's remove twin.
func removeLabelInCommittedTx(t *testing.T, ctx context.Context, store *DoltStore, issueID, label string) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin remove-label transaction: %v", err)
	}
	defer tx.Rollback()
	if err := storageissueops.RemoveLabelInTx(ctx, tx, "", "", issueID, label, "tester"); err != nil {
		t.Fatalf("RemoveLabelInTx(%s, %s): %v", issueID, label, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit remove-label transaction: %v", err)
	}
}

// seedLabelCoordinationSentinel overwrites the tier's coordination cell with
// a recognizable value, so the next write's effect on the cell is visible.
func seedLabelCoordinationSentinel(t *testing.T, ctx context.Context, store *DoltStore, tier, sentinel string) {
	t.Helper()
	res, err := store.db.ExecContext(ctx,
		"UPDATE local_metadata SET value = ? WHERE `key` LIKE ?",
		sentinel, "label-coordination/v1/"+tier+"/%")
	if err != nil {
		t.Fatalf("seed %s coordination sentinel: %v", tier, err)
	}
	touched, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("seed %s coordination sentinel: rows affected: %v", tier, err)
	}
	if touched != 1 {
		t.Fatalf("seed %s coordination sentinel: %d rows, want the one cell", tier, touched)
	}
}

// TestLabelWriteCoordinationCellAbsentAndPreseeded covers the coordination
// cell's two landing states: the first landed label row CREATES it (initially
// absent), a later landed row REWRITES it (preseeded), and the idempotent
// no-ops — INSERT IGNORE that landed nothing, DELETE that matched nothing —
// leave it exactly where it was. That rows-affected gate is what keeps a
// semantic no-op from serializing against real writers (AC-6).
func TestLabelWriteCoordinationCellAbsentAndPreseeded(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "lc-cell"
	createPerm(t, ctx, store, id)
	if got := labelCoordinationValue(t, ctx, store, "labels"); got != "" {
		t.Fatalf("coordination cell exists before any label write: %q", got)
	}

	addLabelInCommittedTx(t, ctx, store, id, "area:z")
	if got := labelCoordinationValue(t, ctx, store, "labels"); got == "" {
		t.Fatal("first landed label row did not create the coordination cell")
	}

	seedLabelCoordinationSentinel(t, ctx, store, "labels", "sentinel")
	addLabelInCommittedTx(t, ctx, store, id, "area:w")
	if got := labelCoordinationValue(t, ctx, store, "labels"); got == "" || got == "sentinel" {
		t.Fatalf("landed label row left the preseeded cell at %q, want a rewrite", got)
	}

	seedLabelCoordinationSentinel(t, ctx, store, "labels", "sentinel-2")
	addLabelInCommittedTx(t, ctx, store, id, "area:w") // INSERT IGNORE: lands nothing
	if got := labelCoordinationValue(t, ctx, store, "labels"); got != "sentinel-2" {
		t.Fatalf("idempotent re-add rewrote the coordination cell = %q, want sentinel-2", got)
	}
	removeLabelInCommittedTx(t, ctx, store, id, "nope:absent") // DELETE: matches nothing
	if got := labelCoordinationValue(t, ctx, store, "labels"); got != "sentinel-2" {
		t.Fatalf("no-op remove rewrote the coordination cell = %q, want sentinel-2", got)
	}
}

// TestLabelPatchNoOpKeepsRowStampAndCoordinationCell is AC-6: with the issue
// at RowVersion R and updated_at U, a label-only patch that lands changes the
// label set but not R or U (the revision token excludes labels by contract),
// and the identical replay is a semantic no-op — Changed=false, no new label
// events, coordination cell untouched.
func TestLabelPatchNoOpKeepsRowStampAndCoordinationCell(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "lc-noop"
	createPerm(t, ctx, store, id)

	replaceLabels := func(labels ...string) publicops.UpdateResult {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin label patch transaction: %v", err)
		}
		defer tx.Rollback()
		result, _, err := storageissueops.ExecuteUpdate(ctx, tx, publicops.UpdateRequest{
			Actor:   "tester",
			IssueID: id,
			Patch: publicops.IssuePatch{
				Labels: publicops.LabelPatch{
					Replace: publicops.Field[[]string]{Set: true, Value: labels},
				},
			},
		})
		if err != nil {
			t.Fatalf("replace labels in transaction: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit label patch: %v", err)
		}
		return result
	}
	stamp := func() (rowVersion int64, updatedAt string, events int) {
		t.Helper()
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("read issue %s: %v", id, err)
		}
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM events WHERE issue_id = ?", id).Scan(&events); err != nil {
			t.Fatalf("count events for %s: %v", id, err)
		}
		return issue.RowVersion, issue.UpdatedAt.UTC().Format(time.RFC3339Nano), events
	}

	rowVersion0, updatedAt0, _ := stamp()
	first := replaceLabels("state:x")
	if !first.Changed {
		t.Fatal("the first label patch must report a change")
	}
	rowVersion1, updatedAt1, events1 := stamp()
	if rowVersion1 != rowVersion0 || updatedAt1 != updatedAt0 {
		t.Fatalf("landed label-only patch moved the row stamp: row_lock %d->%d updated_at %s->%s", rowVersion0, rowVersion1, updatedAt0, updatedAt1)
	}
	cellAfterFirst := labelCoordinationValue(t, ctx, store, "labels")
	if cellAfterFirst == "" {
		t.Fatal("the landed label patch wrote no coordination cell")
	}

	replay := replaceLabels("state:x")
	if replay.Changed {
		t.Fatal("identical label replay reported Changed=true")
	}
	rowVersion2, updatedAt2, events2 := stamp()
	if rowVersion2 != rowVersion1 || updatedAt2 != updatedAt1 {
		t.Fatalf("no-op label replay moved the row stamp: row_lock %d->%d updated_at %s->%s", rowVersion1, rowVersion2, updatedAt1, updatedAt2)
	}
	if events2 != events1 {
		t.Fatalf("no-op label replay recorded %d new events (had %d)", events2-events1, events1)
	}
	if cell := labelCoordinationValue(t, ctx, store, "labels"); cell != cellAfterFirst {
		t.Fatalf("no-op label replay rewrote the coordination cell %q -> %q", cellAfterFirst, cell)
	}
}
