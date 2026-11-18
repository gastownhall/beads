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
	"github.com/steveyegge/beads/internal/types"
)

// TestLabelPatchConcurrentReplaceConflictsAtCommit pins GH#5657 on the durable
// plane: two SQL transactions read the same label snapshot {a}, one replaces
// it with {x} and the other with {y}. Before the label coordination cell the
// two commits merged into {x, y}; now the later commit must fail with the
// retryable serialization error and the winner's set must stand alone. The
// seed is itself a whole-set write, which leaves a committed cell behind, so
// this variant covers racing writers that both rewrite an existing cell.
func TestLabelPatchConcurrentReplaceConflictsAtCommit(t *testing.T) {
	runLabelReplaceRace(t, "label_patch_race", false, true)
}

// TestWispLabelPatchConcurrentReplaceConflictsAtCommit is the same race on an
// ephemeral wisp, whose labels live in wisp_labels: the coordination write has
// to use that tier, on the transaction that wrote the label rows. The seed is
// a named add, which writes no cell, so this variant also covers a cell that
// does not exist until the racing transactions write it.
func TestWispLabelPatchConcurrentReplaceConflictsAtCommit(t *testing.T) {
	runLabelReplaceRace(t, "wisp_label_patch_race", true, false)
}

// TestLabelOnlyPatchLeavesTheRowAndRepeatsAsANoOp pins what the coordination
// cell must not cost (GH#5657): a label-only prefix patch is a label-table
// write plus a clone-local cell rewrite, so the issue row keeps its
// RowVersion, and a second identical patch nets to the current set, reports
// Changed=false, leaves the cell as the first patch wrote it and records no
// label events. The seed's named adds write no cell at all.
//
// updated_at is deliberately not asserted. Whether a label edit advances it
// is a separate, open decision (GH#5442: open PRs #8285 and #5793 make it
// advance); the cell is not a row write either way, and RowVersion, the
// untouched cell and the absent events are what prove that.
func TestLabelOnlyPatchLeavesTheRowAndRepeatsAsANoOp(t *testing.T) {
	skipIfNoServer(t)
	ctx, cancel := testContext(t)
	defer cancel()

	store := newMultiConnectionEpochStore(t, ctx, "label_patch_noop")
	if err := store.SetConfig(ctx, "issue_prefix", "lr"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	const id = "lr-noop"
	createPerm(t, ctx, store, id)
	for _, label := range []string{"state:a", "keep"} {
		if err := store.AddLabel(ctx, id, label, "seeder"); err != nil {
			t.Fatalf("seed label %s: %v", label, err)
		}
	}
	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	row := func() *types.Issue {
		t.Helper()
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		return issue
	}
	cell := func() string {
		t.Helper()
		var value sql.NullString
		if err := store.db.QueryRowContext(ctx,
			"SELECT MAX(value) FROM local_metadata WHERE `key` LIKE ?", "label-coordination/v1/labels/%").Scan(&value); err != nil {
			t.Fatalf("read the coordination cell: %v", err)
		}
		return value.String
	}
	events := func() int {
		t.Helper()
		got, err := store.GetEvents(ctx, id, 0)
		if err != nil {
			t.Fatalf("GetEvents: %v", err)
		}
		return len(got)
	}
	if got := cell(); got != "" {
		t.Fatalf("the seed's named adds wrote the coordination cell %q, want none", got)
	}
	patch := publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{RemovePrefixes: []string{"state:"}, Add: []string{"state:b"}},
	}}

	before := row()
	first, err := lifecycle.Update(ctx, patch)
	if err != nil || !first.Changed {
		t.Fatalf("first label patch = (changed %v, %v), want a committed edit", first.Changed, err)
	}
	afterFirst := row()
	labels := append([]string(nil), afterFirst.Labels...)
	sort.Strings(labels)
	if !reflect.DeepEqual(labels, []string{"keep", "state:b"}) {
		t.Fatalf("labels after the first patch = %v, want [keep state:b]", labels)
	}
	if afterFirst.RowVersion != before.RowVersion {
		t.Fatalf("label-only patch reminted the issue row: RowVersion %d -> %d, want it unchanged",
			before.RowVersion, afterFirst.RowVersion)
	}
	written := cell()
	if written == "" {
		t.Fatalf("the first label patch wrote no coordination cell")
	}
	eventsAfterFirst := events()

	second, err := lifecycle.Update(ctx, patch)
	if err != nil {
		t.Fatalf("identical label patch: %v", err)
	}
	if second.Changed {
		t.Errorf("identical label patch reported Changed = true, want a no-op")
	}
	if got := cell(); got != written {
		t.Errorf("identical label patch rewrote the coordination cell %q -> %q, want it untouched", written, got)
	}
	if got := events(); got != eventsAfterFirst {
		t.Errorf("identical label patch recorded %d new events, want none", got-eventsAfterFirst)
	}
	if afterSecond := row(); afterSecond.RowVersion != before.RowVersion {
		t.Errorf("identical label patch reminted the issue row: RowVersion %d -> %d, want it unchanged",
			before.RowVersion, afterSecond.RowVersion)
	}
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
	if preseeded {
		// A whole-set write reads the set, so it leaves the cell behind.
		lifecycle, err := store.IssueLifecycle()
		if err != nil {
			t.Fatalf("IssueLifecycle(): %v", err)
		}
		if _, err := lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "seeder", IssueID: id, Patch: publicops.IssuePatch{
			Labels: publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"a"}}},
		}}); err != nil {
			t.Fatalf("seed label: %v", err)
		}
	} else if err := store.AddLabel(ctx, id, "a", "seeder"); err != nil {
		// A named add reads nothing it acts on and writes no cell.
		t.Fatalf("seed label: %v", err)
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

// TestLabelCoordinationCellOnlyForEditsThatReadTheSet pins which label writes
// rewrite the coordination cell (GH#5657). Named adds and removes never do:
// AddLabelInTx and RemoveLabelInTx, which `bd label propagate`, `bd cook
// --persist` and the tracker's direct-route sync call inside transactions that
// never replay, and a label patch of only Add and Remove. They read nothing
// they act on, so they must not be able to fail on a conflict through the
// cell. A replacement or a prefix removal that changes the set creates the
// cell when it is absent and rewrites it when it exists, and one that leaves
// the set as it was does not touch it, so a semantic no-op never serializes
// against real writers (AC-6).
func TestLabelCoordinationCellOnlyForEditsThatReadTheSet(t *testing.T) {
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
	removeLabelInCommittedTx(t, ctx, store, id, "area:z")
	addLabelInCommittedTx(t, ctx, store, id, "state:a")
	if !patchLabelsInCommittedTx(t, ctx, store, id, publicops.LabelPatch{Add: []string{"keep"}, Remove: []string{"state:gone"}}) {
		t.Fatal("named add/remove patch reported no change, want keep added")
	}
	if got := labelCoordinationValue(t, ctx, store, "labels"); got != "" {
		t.Fatalf("named label adds and removes wrote the coordination cell %q, want none", got)
	}

	replace := publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"state:a", "keep", "area:x"}}}
	if !patchLabelsInCommittedTx(t, ctx, store, id, replace) {
		t.Fatal("replacement reported no change, want area:x added")
	}
	if got := labelCoordinationValue(t, ctx, store, "labels"); got == "" {
		t.Fatal("a replacement that changed the set did not create the coordination cell")
	}

	seedLabelCoordinationSentinel(t, ctx, store, "labels", "sentinel")
	setState := publicops.LabelPatch{RemovePrefixes: []string{"state:"}, Add: []string{"state:b"}}
	if !patchLabelsInCommittedTx(t, ctx, store, id, setState) {
		t.Fatal("prefix removal reported no change, want state:a swapped for state:b")
	}
	if got := labelCoordinationValue(t, ctx, store, "labels"); got == "" || got == "sentinel" {
		t.Fatalf("a prefix removal that changed the set left the preseeded cell at %q, want a rewrite", got)
	}

	seedLabelCoordinationSentinel(t, ctx, store, "labels", "sentinel-2")
	if patchLabelsInCommittedTx(t, ctx, store, id, setState) {
		t.Error("repeated prefix removal reported a change, want a no-op")
	}
	sameSet := publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"state:b", "keep", "area:x"}}}
	if patchLabelsInCommittedTx(t, ctx, store, id, sameSet) {
		t.Error("replacement with the held set reported a change, want a no-op")
	}
	addLabelInCommittedTx(t, ctx, store, id, "area:y")
	removeLabelInCommittedTx(t, ctx, store, id, "keep")
	if got := labelCoordinationValue(t, ctx, store, "labels"); got != "sentinel-2" {
		t.Fatalf("no-op set-reading patches and named writes rewrote the coordination cell = %q, want sentinel-2", got)
	}
}

// TestNamedLabelWritesOverlapWithoutConflict is the other half of the rule
// above. Two named label adds on one issue and a replacement of its set, all
// three reading the same snapshot before any of them writes, commit together:
// a named add never writes the cell, so it never meets the replacement's or
// another add's cell write, and the transactions that add labels without a
// replay (`bd label propagate`, `bd cook --persist`) cannot fail on it. The
// rows merge into what running the adds after the replacement leaves.
func TestNamedLabelWritesOverlapWithoutConflict(t *testing.T) {
	skipIfNoServer(t)
	ctx, cancel := testContext(t)
	defer cancel()

	store := newMultiConnectionEpochStore(t, ctx, "named_label_overlap")
	// Versioned history off, as in runLabelReplaceRace: issue_versions rows
	// minted by every writer would conflict on their own.
	store.SetVersionedHistoryEnabled(false)
	if err := store.SetConfig(ctx, "issue_prefix", "lr"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	const id = "lr-named"
	createPerm(t, ctx, store, id)
	if err := store.AddLabel(ctx, id, "a", "seeder"); err != nil {
		t.Fatalf("seed label: %v", err)
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
	txR, txY, txZ := begin(), begin(), begin()
	// Establish all three snapshots before any transaction writes.
	currentR, err := issueops.GetIssueInTx(ctx, txR, id)
	if err != nil {
		t.Fatalf("read in replacing transaction: %v", err)
	}
	for name, tx := range map[string]*sql.Tx{"y": txY, "z": txZ} {
		labels, err := issueops.GetLabelsInTx(ctx, tx, "labels", id)
		if err != nil {
			t.Fatalf("read in %s transaction: %v", name, err)
		}
		if !reflect.DeepEqual(labels, []string{"a"}) {
			t.Fatalf("%s snapshot = %v, want [a]", name, labels)
		}
	}

	changed, err := issueops.ApplyLabelPatch(ctx, txR, currentR, publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"x"}}}, "writer-r")
	if err != nil || !changed {
		t.Fatalf("apply replacement = (%v, %v), want (true, nil)", changed, err)
	}
	if err := issueops.AddLabelInTx(ctx, txY, "", "", id, "y", "writer-y"); err != nil {
		t.Fatalf("add y: %v", err)
	}
	if err := issueops.AddLabelInTx(ctx, txZ, "", "", id, "z", "writer-z"); err != nil {
		t.Fatalf("add z: %v", err)
	}

	if err := txR.Commit(); err != nil {
		t.Fatalf("commit replacement: %v", err)
	}
	if err := txY.Commit(); err != nil {
		t.Fatalf("commit add y after the replacement: %v, want it to merge (a named add must not conflict)", err)
	}
	if err := txZ.Commit(); err != nil {
		t.Fatalf("commit add z after add y: %v, want it to merge (a named add must not conflict)", err)
	}

	got, err := store.GetLabels(ctx, id)
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"x", "y", "z"}) {
		t.Fatalf("labels after the overlap = %v, want [x y z]: the replacement, then both adds", got)
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

// patchLabelsInCommittedTx applies patch to issueID's labels in its own
// committed transaction, as ExecuteUpdate does inside an update, and reports
// whether the set changed.
func patchLabelsInCommittedTx(t *testing.T, ctx context.Context, store *DoltStore, issueID string, patch publicops.LabelPatch) bool {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin label-patch transaction: %v", err)
	}
	defer tx.Rollback()
	current, err := issueops.GetIssueInTx(ctx, tx, issueID)
	if err != nil {
		t.Fatalf("read %s: %v", issueID, err)
	}
	changed, err := issueops.ApplyLabelPatch(ctx, tx, current, patch, "tester")
	if err != nil {
		t.Fatalf("ApplyLabelPatch(%s, %+v): %v", issueID, patch, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit label-patch transaction: %v", err)
	}
	return changed
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
