package dolt

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/types"
)

// TestIsBlockedBatch is the batch-is_blocked regression on the reference Dolt
// backend: the batch transitive is_blocked read agrees with per-row IsBlocked
// for every id (direct blocker, inherited parent-child block, unblocked control), and
// reflects inherited blockedness with an EMPTY direct-blocker set — the same
// denormalized column IsBlocked reads, in one round-trip, with no recompute.
func TestIsBlockedBatch(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	mk := func(id string) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	add := func(src, tgt string, typ types.DependencyType) {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ}, "tester"); err != nil {
			t.Fatalf("add dep %s->%s: %v", src, tgt, err)
		}
	}
	// ib-blk (open) blocks ib-parent; ib-child is a parent-child of ib-parent so
	// it inherits is_blocked with no direct blocking edge; ib-free is unblocked.
	mk("ib-blk")
	mk("ib-parent")
	add("ib-parent", "ib-blk", types.DepBlocks)
	mk("ib-child")
	add("ib-child", "ib-parent", types.DepParentChild)
	mk("ib-free")

	ids := []string{"ib-blk", "ib-parent", "ib-child", "ib-free"}
	batch, err := store.IsBlockedBatch(ctx, ids)
	if err != nil {
		t.Fatalf("IsBlockedBatch: %v", err)
	}

	// Batch value must equal per-row IsBlocked for every id.
	for _, id := range ids {
		want, _, err := store.IsBlocked(ctx, id)
		if err != nil {
			t.Fatalf("IsBlocked(%s): %v", id, err)
		}
		if batch[id] != want {
			t.Fatalf("IsBlockedBatch[%s] = %v, want %v (per-row IsBlocked)", id, batch[id], want)
		}
	}

	// Inherited block: the child is transitively blocked with an EMPTY direct
	// blocker set, and the batch reflects it.
	blocked, blockers, err := store.IsBlocked(ctx, "ib-child")
	if err != nil {
		t.Fatalf("IsBlocked(ib-child): %v", err)
	}
	if !blocked || len(blockers) != 0 {
		t.Fatalf("ib-child IsBlocked = (%v, %v), want (true, []) — inherited block, empty direct blockers", blocked, blockers)
	}
	if !batch["ib-child"] || !batch["ib-parent"] {
		t.Fatalf("IsBlockedBatch child=%v parent=%v, want both true", batch["ib-child"], batch["ib-parent"])
	}
	if batch["ib-free"] {
		t.Fatalf("IsBlockedBatch[ib-free] = true, want false")
	}

	// Missing id is absent from the map (callers treat absent as not-blocked).
	miss, err := store.IsBlockedBatch(ctx, []string{"ib-nope"})
	if err != nil {
		t.Fatalf("IsBlockedBatch(missing): %v", err)
	}
	if _, ok := miss["ib-nope"]; ok {
		t.Fatalf("IsBlockedBatch returned an entry for a missing id: %v", miss)
	}
}

// TestIsBlockedBatchCrossTableCollisionMatchesSingle locks in the single/batch
// resolution parity: when the SAME id lives in BOTH the issues and wisps tables
// with DIFFERENT is_blocked values (a data anomaly), the batch read
// (IsBlockedBatch) must resolve the shared is_blocked field the same
// way the per-row read (IsBlocked) does. IsBlockedInTx scans
// issues→wisps and breaks on the first table that has the id, so ISSUES wins;
// the batch must agree. Before the fix the batch preferred the wisps row and the
// two reads disagreed on the same stored flag.
func TestIsBlockedBatchCrossTableCollisionMatchesSingle(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "ib-collide"

	// issues row: is_blocked = 1 (blocked).
	createPerm(t, ctx, store, id)
	if _, err := store.execContext(ctx, "UPDATE issues SET is_blocked = 1 WHERE id = ?", id); err != nil {
		t.Fatalf("set issues.is_blocked: %v", err)
	}

	// wisps row with the SAME id but is_blocked = 0 (not blocked) — the collision.
	if _, err := store.execContext(ctx, `
		INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral, is_blocked)
		VALUES (?, ?, '', '', '', '', ?, ?, ?, ?, ?)
	`, id, "wisp collide", types.StatusOpen, 2, types.TypeTask, true, 0); err != nil {
		t.Fatalf("insert colliding wisp row: %v", err)
	}

	single, _, err := store.IsBlocked(ctx, id)
	if err != nil {
		t.Fatalf("IsBlocked(%s): %v", id, err)
	}
	batch, err := store.IsBlockedBatch(ctx, []string{id})
	if err != nil {
		t.Fatalf("IsBlockedBatch(%s): %v", id, err)
	}

	// The two reads must agree on the shared is_blocked field.
	if single != batch[id] {
		t.Fatalf("collision divergence: IsBlocked(%s) = %v, IsBlockedBatch[%s] = %v — must agree", id, single, id, batch[id])
	}
	// And the agreed value must be the ISSUES-table value (issues-wins), matching
	// IsBlockedInTx's issues→wisps break order.
	if !single {
		t.Fatalf("IsBlocked(%s) = false, want true (issues-wins: issues.is_blocked = 1)", id)
	}
}

// TestIsBlockedWithBlockersBatchMatchesPerRow pins the server-mode
// IsBlockedWithBlockersBatch (storage.BlockersBatchStore, the read the
// external-dependency policy's BlockingAnnotator takes for a page) against
// per-row IsBlocked: an id is present exactly when IsBlocked reports it
// blocked, mapped to the blocker list IsBlocked returns. It covers the shapes
// that decide the answer (live blockers of each blocking type on both planes,
// closed and pinned ones that do not count, a block inherited through the
// parent chain, external refs and a non-blocking edge, an unblocked row, an
// id with no row), an id stored in both the issues and wisps tables, and a
// page past the 200-id read batch, so the edge read and the blocker-status
// read each take more than one batch and a repeated id spans two of them.
func TestIsBlockedWithBlockersBatchMatchesPerRow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	mk := func(id string, status types.Status, ephemeral bool) {
		t.Helper()
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		switch status {
		case types.StatusClosed:
			if err := store.CloseIssue(ctx, id, "done", "tester", ""); err != nil {
				t.Fatalf("close %s: %v", id, err)
			}
		case types.StatusPinned:
			if err := store.UpdateIssue(ctx, id, map[string]interface{}{"status": string(types.StatusPinned)}, "tester"); err != nil {
				t.Fatalf("pin %s: %v", id, err)
			}
		}
	}
	add := func(src, tgt string, typ types.DependencyType) {
		t.Helper()
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ}, "tester"); err != nil {
			t.Fatalf("add dep %s->%s %s: %v", src, tgt, typ, err)
		}
	}
	addWispRow := func(id string, status types.Status) {
		t.Helper()
		if _, err := store.execContext(ctx, `
			INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral, is_blocked)
			VALUES (?, ?, '', '', '', '', ?, ?, ?, ?, ?)
		`, id, "wisp "+id, status, 2, types.TypeTask, true, 0); err != nil {
			t.Fatalf("insert colliding wisp row %s: %v", id, err)
		}
	}

	mk("bb-x", types.StatusOpen, false)
	mk("bb-y", types.StatusOpen, false)
	mk("bb-done", types.StatusClosed, false)
	mk("bb-pin", types.StatusPinned, false)
	mk("bb-spawner", types.StatusOpen, false)
	mk("bb-spawn-child", types.StatusOpen, false)
	mk("bb-wx", types.StatusOpen, true)
	for _, id := range []string{"bb-blk", "bb-two", "bb-blk-done", "bb-blk-pin", "bb-parent", "bb-child", "bb-grandchild",
		"bb-wait", "bb-ext", "bb-mixed", "bb-related", "bb-free", "bb-collide", "bb-dup-blocker", "bb-on-dup"} {
		mk(id, types.StatusOpen, false)
	}
	mk("bb-cond", types.StatusOpen, true)
	mk("bb-wisp-blk", types.StatusOpen, true)

	add("bb-blk", "bb-x", types.DepBlocks)
	add("bb-two", "bb-y", types.DepBlocks)
	add("bb-two", "bb-x", types.DepBlocks)
	add("bb-blk-done", "bb-done", types.DepBlocks)
	add("bb-blk-pin", "bb-pin", types.DepBlocks)
	add("bb-parent", "bb-x", types.DepBlocks)
	add("bb-child", "bb-parent", types.DepParentChild)
	add("bb-grandchild", "bb-child", types.DepParentChild)
	add("bb-spawn-child", "bb-spawner", types.DepParentChild)
	add("bb-wait", "bb-spawner", types.DepWaitsFor)
	add("bb-cond", "bb-x", types.DepConditionalBlocks)
	add("bb-wisp-blk", "bb-wx", types.DepBlocks)
	add("bb-ext", "external:remote:pending", types.DepBlocks)
	add("bb-mixed", "bb-x", types.DepBlocks)
	add("bb-mixed", "external:remote:pending", types.DepBlocks)
	add("bb-related", "external:remote:pending", types.DepRelated)
	add("bb-collide", "bb-x", types.DepBlocks)
	add("bb-on-dup", "bb-dup-blocker", types.DepBlocks)

	// The collision anomaly: bb-collide also has a wisps row (not blocked, with
	// its own blocking edge), and bb-dup-blocker has a closed wisps row. IsBlocked
	// takes the flag from the issues row, reads both edge tables in order, and
	// takes a doubly-stored blocker's status from its wisps row.
	addWispRow("bb-collide", types.StatusOpen)
	addWispRow("bb-dup-blocker", types.StatusClosed)
	if _, err := store.execContext(ctx, `
		INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_by)
		VALUES (?, ?, ?, ?, ?)
	`, depid.New("bb-collide", "bb-y"), "bb-collide", "bb-y", types.DepBlocks, "tester"); err != nil {
		t.Fatalf("insert colliding wisp edge: %v", err)
	}

	// The page: each row held by its own open blocker. Listed first, it fills
	// the first 200-id batch, so the shapes above land in the second.
	const page = 210 // > queryBatchSize (200) blocked ids and blocker targets
	bulk := make([]*types.Issue, 0, 2*page)
	ids := make([]string, 0, page+32)
	for i := range page {
		blocker := fmt.Sprintf("bb-page-blocker-%03d", i)
		id := fmt.Sprintf("bb-page-%03d", i)
		bulk = append(bulk,
			&types.Issue{ID: blocker, Title: blocker, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			&types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
				Dependencies: []*types.Dependency{{DependsOnID: blocker, Type: types.DepBlocks}}})
		ids = append(ids, id)
	}
	if err := store.CreateIssuesWithFullOptions(ctx, bulk, "tester", storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("CreateIssuesWithFullOptions(page): %v", err)
	}
	ids = append(ids, "bb-x", "bb-blk", "bb-two", "bb-blk-done", "bb-blk-pin", "bb-parent", "bb-child", "bb-grandchild",
		"bb-wait", "bb-cond", "bb-wisp-blk", "bb-ext", "bb-mixed", "bb-related", "bb-free", "bb-collide", "bb-on-dup",
		"bb-missing", "bb-blk", "bb-page-000")

	got, err := store.IsBlockedWithBlockersBatch(ctx, ids)
	if err != nil {
		t.Fatalf("IsBlockedWithBlockersBatch: %v", err)
	}
	want := make(map[string][]string)
	for _, id := range ids {
		blocked, blockers, err := store.IsBlocked(ctx, id)
		if err != nil {
			t.Fatalf("IsBlocked(%s): %v", id, err)
		}
		if blocked {
			want[id] = blockers
		}
		list, ok := got[id]
		if ok != blocked || !reflect.DeepEqual(list, blockers) {
			t.Errorf("IsBlockedWithBlockersBatch[%s] = (%v, %#v), want IsBlocked's (%v, %#v)", id, ok, list, blocked, blockers)
		}
	}
	if len(got) != len(want) {
		t.Errorf("IsBlockedWithBlockersBatch answered %d ids, want the %d IsBlocked reports blocked", len(got), len(want))
	}

	// Spot-check the decisive rows, so the equality above is not two equal
	// wrong answers.
	for id, wantList := range map[string]string{
		"bb-blk":      "[bb-x]",
		"bb-two":      "[bb-x bb-y]",
		"bb-wait":     "[bb-spawner (waits-for)]",
		"bb-cond":     "[bb-x (conditional-blocks)]",
		"bb-wisp-blk": "[bb-wx]",
		"bb-mixed":    "[bb-x]",
		"bb-collide":  "[bb-x bb-y]",
		"bb-page-000": "[bb-page-blocker-000]",
		"bb-page-209": "[bb-page-blocker-209]",
	} {
		if list, ok := got[id]; !ok || fmt.Sprint(list) != wantList {
			t.Errorf("IsBlockedWithBlockersBatch[%s] = (%v, %v), want (true, %s)", id, ok, list, wantList)
		}
	}
	for _, id := range []string{"bb-child", "bb-grandchild", "bb-on-dup"} {
		if list, ok := got[id]; !ok || list != nil {
			t.Errorf("IsBlockedWithBlockersBatch[%s] = (%v, %#v), want (true, nil): blocked with no live blocker", id, ok, list)
		}
	}
	for _, id := range []string{"bb-x", "bb-blk-done", "bb-related", "bb-free", "bb-missing"} {
		if list, ok := got[id]; ok {
			t.Errorf("IsBlockedWithBlockersBatch[%s] = %#v, want absent", id, list)
		}
	}
}
