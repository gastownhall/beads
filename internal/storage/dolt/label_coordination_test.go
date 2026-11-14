package dolt

import (
	"context"
	"database/sql"
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

// TestWispLabelPatchConcurrentReplaceConflictsAtCommit is the same race on an
// ephemeral wisp, whose labels live in wisp_labels: the coordination write has
// to use that tier, on the transaction that wrote the label rows. The seed's
// cell is removed first, so this variant also covers a cell that does not
// exist until the racing transactions write it.
func TestWispLabelPatchConcurrentReplaceConflictsAtCommit(t *testing.T) {
	runLabelReplaceRace(t, "wisp_label_patch_race", true, false)
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
