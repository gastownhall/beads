package dolt

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sort"
	"testing"

	publicops "github.com/steveyegge/beads/issueops"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// TestLabelPatchConcurrentReplaceConflictsAtCommit pins GH#5657 on the durable
// plane: two SQL transactions read the same label snapshot {a}, one replaces
// it with {x} and the other with {y}. Before the label coordination cell the
// two commits merged into {x, y}; now the later commit must fail with the
// retryable serialization error and the winner's set must stand alone. The
// seed label leaves a committed cell behind, so this variant covers racing
// writers that both rewrite an existing cell.
func TestLabelPatchConcurrentReplaceConflictsAtCommit(t *testing.T) {
	runLabelReplaceRace(t, "label_patch_race", false, true)
}

func runLabelReplaceRace(t *testing.T, dbName string, wisp, preseeded bool) {
	t.Helper()
	skipIfNoServer(t)
	ctx, cancel := testContext(t)
	defer cancel()

	store := newMultiConnectionEpochStore(t, ctx, dbName)
	// Versioned history off: issue_versions rows minted by both writers would
	// otherwise be a second, accidental conflict that masks the label defect.
	store.SetVersionedHistoryEnabled(false)
	if err := store.SetConfig(ctx, "issue_prefix", "lr"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}

	const id = "lr-race"
	tier, otherTier := "labels", "wisp_labels"
	if wisp {
		createWisp(t, ctx, store, id)
		tier, otherTier = otherTier, tier
	} else {
		createPerm(t, ctx, store, id)
	}
	if err := store.AddLabel(ctx, id, "a", "seeder"); err != nil {
		t.Fatalf("seed label: %v", err)
	}
	cellPattern := "label-coordination/v1/" + tier + "/%"
	if !preseeded {
		if _, err := store.db.ExecContext(ctx, "DELETE FROM local_metadata WHERE `key` LIKE ?", cellPattern); err != nil {
			t.Fatalf("drop the seed's coordination cell: %v", err)
		}
	}
	type querier interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}
	// cells returns the coordination cell values q sees for one tier.
	cells := func(q querier, tier string) []string {
		t.Helper()
		var value sql.NullString
		var n int
		if err := q.QueryRowContext(ctx,
			"SELECT COUNT(*), MAX(value) FROM local_metadata WHERE `key` LIKE ?", "label-coordination/v1/"+tier+"/%").Scan(&n, &value); err != nil {
			t.Fatalf("read %s coordination cells: %v", tier, err)
		}
		if n == 0 {
			return nil
		}
		return []string{value.String}
	}
	seeded := cells(store.db, tier)
	if preseeded != (len(seeded) == 1) {
		t.Fatalf("%s coordination cells before the race = %v, want preseeded=%v", tier, seeded, preseeded)
	}

	begin := func() *sql.Tx {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		t.Cleanup(func() { _ = tx.Rollback() })
		return tx
	}
	txX, txY := begin(), begin()
	// Establish both snapshots before either transaction writes.
	currentX, err := issueops.GetIssueInTx(ctx, txX, id)
	if err != nil {
		t.Fatalf("read in x transaction: %v", err)
	}
	currentY, err := issueops.GetIssueInTx(ctx, txY, id)
	if err != nil {
		t.Fatalf("read in y transaction: %v", err)
	}
	if !reflect.DeepEqual(currentX.Labels, []string{"a"}) || !reflect.DeepEqual(currentY.Labels, []string{"a"}) {
		t.Fatalf("snapshots = %v / %v, want [a] in both", currentX.Labels, currentY.Labels)
	}

	changed, err := issueops.ApplyLabelPatch(ctx, txX, currentX, publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"x"}}}, "writer-x")
	if err != nil || !changed {
		t.Fatalf("apply x = (%v, %v), want (true, nil)", changed, err)
	}
	changed, err = issueops.ApplyLabelPatch(ctx, txY, currentY, publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"y"}}}, "writer-y")
	if err != nil || !changed {
		t.Fatalf("apply y = (%v, %v), want (true, nil)", changed, err)
	}

	// The coordination write lands on the label transaction itself, in the
	// tier the labels went to, and no other session sees it before commit.
	inTier, inOtherTier, outside := cells(txX, tier), cells(txX, otherTier), cells(store.db, tier)

	if err := txX.Commit(); err != nil {
		t.Fatalf("commit x: %v", err)
	}
	commitY := txY.Commit()

	got, err := store.GetLabels(ctx, id)
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	sort.Strings(got)
	if commitY == nil {
		t.Fatalf("y committed over x: the label edits merged instead of conflicting, leaving %v", got)
	}
	if !isSerializationError(commitY) {
		t.Fatalf("commit y = %v, want the retryable serialization error (1213)", commitY)
	}
	if !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("labels after the race = %v, want exactly [x] (the committed winner)", got)
	}

	if len(inTier) != 1 || reflect.DeepEqual(inTier, seeded) {
		t.Fatalf("%s coordination cell inside the x transaction = %v (before the race %v), want one freshly rewritten cell", tier, inTier, seeded)
	}
	if len(inOtherTier) != 0 {
		t.Fatalf("%s coordination cells inside the x transaction = %v, want none: the label rows went to %s", otherTier, inOtherTier, tier)
	}
	if !reflect.DeepEqual(outside, seeded) {
		t.Fatalf("%s coordination cells outside the x transaction before it committed = %v, want the pre-race %v: the cell must ride the label transaction", tier, outside, seeded)
	}
	if after := cells(store.db, tier); !reflect.DeepEqual(after, inTier) {
		t.Fatalf("%s coordination cell after commit = %v, want the x transaction's %v", tier, after, inTier)
	}
}

// TestLabelWriteCoordinationCellAbsentAndPreseeded covers the coordination
// cell's two landing states: the first landed label row CREATES it (initially
// absent), a later landed row REWRITES it (preseeded), and the idempotent
// no-ops (an INSERT IGNORE that landed nothing, a DELETE that matched nothing)
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

// labelCoordinationValue reads the coordination cell for one label tier, ""
// when the tier has no cell. The shard is derived from the issue ID, so on a
// test's own database the tier-prefix LIKE finds exactly the write under test.
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

func addLabelInCommittedTx(t *testing.T, ctx context.Context, store *DoltStore, issueID, label string) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin add-label transaction: %v", err)
	}
	defer tx.Rollback()
	if err := issueops.AddLabelInTx(ctx, tx, "", "", issueID, label, "tester"); err != nil {
		t.Fatalf("AddLabelInTx(%s, %s): %v", issueID, label, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit add-label transaction: %v", err)
	}
}

func removeLabelInCommittedTx(t *testing.T, ctx context.Context, store *DoltStore, issueID, label string) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin remove-label transaction: %v", err)
	}
	defer tx.Rollback()
	if err := issueops.RemoveLabelInTx(ctx, tx, "", "", issueID, label, "tester"); err != nil {
		t.Fatalf("RemoveLabelInTx(%s, %s): %v", issueID, label, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit remove-label transaction: %v", err)
	}
}

// seedLabelCoordinationSentinel overwrites the tier's one coordination cell
// with a known value, so a later write can be seen to rewrite it or not.
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
