package dolt

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// depEventsOfType returns the events on issue id whose type matches et, as read
// back through the store (working-set view, the same view bd and library
// callers see).
func depEventsOfType(t *testing.T, ctx context.Context, store *DoltStore, id string, et types.EventType) []*types.Event {
	t.Helper()
	events, err := store.GetEvents(ctx, id, 0)
	if err != nil {
		t.Fatalf("GetEvents(%s): %v", id, err)
	}
	var out []*types.Event
	for _, e := range events {
		if e.EventType == et {
			out = append(out, e)
		}
	}
	return out
}

func newValueOf(t *testing.T, e *types.Event) string {
	t.Helper()
	if e.NewValue == nil {
		t.Fatalf("event %s has nil new_value", e.EventType)
	}
	return *e.NewValue
}

// TestDependencyEventEmission ports the SQLite-era dependency_added /
// dependency_removed event emission onto the Dolt engine: a real add or remove
// records a descriptive event on the source's event table, an idempotent re-add
// does not double-emit, and the added event is DOLT_COMMITted (staged), not left
// dangling in the working set.
func TestDependencyEventEmission(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	t.Run("AddEmitsDependencyAdded", func(t *testing.T) {
		src, tgt := "de-add-a", "de-add-b"
		createPerm(t, ctx, store, src)
		createPerm(t, ctx, store, tgt)
		if err := store.AddDependencyWithOptions(ctx, &types.Dependency{
			IssueID: src, DependsOnID: tgt, Type: types.DepBlocks,
		}, "tester", storage.DependencyAddOptions{EmitEvent: true}); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}

		added := depEventsOfType(t, ctx, store, src, types.EventDependencyAdded)
		if len(added) != 1 {
			t.Fatalf("dependency_added event count on %s = %d, want 1", src, len(added))
		}
		if added[0].Actor != "tester" {
			t.Fatalf("dependency_added actor = %q, want %q", added[0].Actor, "tester")
		}
		if got, want := newValueOf(t, added[0]), "Added dependency: de-add-a blocks de-add-b"; got != want {
			t.Fatalf("dependency_added new_value = %q, want %q", got, want)
		}
		// No event should land on the target (the source owns the edge history).
		if n := len(depEventsOfType(t, ctx, store, tgt, types.EventDependencyAdded)); n != 0 {
			t.Fatalf("target %s must carry no dependency_added event, got %d", tgt, n)
		}
	})

	t.Run("AddCommitsEventRow", func(t *testing.T) {
		src, tgt := "de-commit-a", "de-commit-b"
		createPerm(t, ctx, store, src)
		createPerm(t, ctx, store, tgt)
		if err := store.AddDependencyWithOptions(ctx, &types.Dependency{
			IssueID: src, DependsOnID: tgt, Type: types.DepBlocks,
		}, "tester", storage.DependencyAddOptions{EmitEvent: true}); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}

		// AS OF 'HEAD' reads the committed tree only: a non-zero count proves the
		// event row was staged and committed, not merely written to the working set.
		var committed int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM events AS OF 'HEAD' WHERE issue_id = ? AND event_type = ?",
			src, string(types.EventDependencyAdded),
		).Scan(&committed); err != nil {
			t.Fatalf("count committed dependency_added events: %v", err)
		}
		if committed != 1 {
			t.Fatalf("committed dependency_added count = %d, want 1 (events must be staged before DOLT_COMMIT)", committed)
		}
		// The events table must be clean afterward — the row is in the commit, not
		// left dirty in the working set where a later DOLT_RESET could drop it.
		var dirtyEvents int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'events'",
		).Scan(&dirtyEvents); err != nil {
			t.Fatalf("query dolt_status for events: %v", err)
		}
		if dirtyEvents != 0 {
			t.Fatalf("events table dirty after AddDependency (count=%d); staging is not committing the event", dirtyEvents)
		}
	})

	t.Run("ExplicitRemoveEmitsDependencyRemoved", func(t *testing.T) {
		src, tgt := "de-rm-a", "de-rm-b"
		createPerm(t, ctx, store, src)
		createPerm(t, ctx, store, tgt)
		if err := store.AddDependencyWithOptions(ctx, &types.Dependency{
			IssueID: src, DependsOnID: tgt, Type: types.DepBlocks,
		}, "tester", storage.DependencyAddOptions{EmitEvent: true}); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		// Only the explicit bd dep remove verb (EmitEvent) records history.
		if err := store.RemoveDependencyWithOptions(ctx, src, tgt, "remover", storage.DependencyRemoveOptions{EmitEvent: true}); err != nil {
			t.Fatalf("RemoveDependency: %v", err)
		}

		removed := depEventsOfType(t, ctx, store, src, types.EventDependencyRemoved)
		if len(removed) != 1 {
			t.Fatalf("dependency_removed event count on %s = %d, want 1", src, len(removed))
		}
		if removed[0].Actor != "remover" {
			t.Fatalf("dependency_removed actor = %q, want %q", removed[0].Actor, "remover")
		}
		if got, want := newValueOf(t, removed[0]), "Removed dependency on de-rm-b"; got != want {
			t.Fatalf("dependency_removed new_value = %q, want %q", got, want)
		}
		// The removed event is committed too.
		var committed int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM events AS OF 'HEAD' WHERE issue_id = ? AND event_type = ?",
			src, string(types.EventDependencyRemoved),
		).Scan(&committed); err != nil {
			t.Fatalf("count committed dependency_removed events: %v", err)
		}
		if committed != 1 {
			t.Fatalf("committed dependency_removed count = %d, want 1", committed)
		}
	})

	t.Run("RemoveMissingEdgeEmitsNothing", func(t *testing.T) {
		src, tgt := "de-noop-a", "de-noop-b"
		createPerm(t, ctx, store, src)
		createPerm(t, ctx, store, tgt)
		// No edge was ever added; a no-op remove must not record an event even
		// when the explicit verb asks to emit — there is no edge to report.
		if err := store.RemoveDependencyWithOptions(ctx, src, tgt, "remover", storage.DependencyRemoveOptions{EmitEvent: true}); err != nil {
			t.Fatalf("RemoveDependency (no-op): %v", err)
		}
		if n := len(depEventsOfType(t, ctx, store, src, types.EventDependencyRemoved)); n != 0 {
			t.Fatalf("no-op remove must emit no dependency_removed event, got %d", n)
		}
	})

	t.Run("StructuralRemoveIsSilent", func(t *testing.T) {
		src, tgt := "de-struct-a", "de-struct-b"
		createPerm(t, ctx, store, src)
		createPerm(t, ctx, store, tgt)
		if err := store.AddDependencyWithOptions(ctx, &types.Dependency{
			IssueID: src, DependsOnID: tgt, Type: types.DepBlocks,
		}, "tester", storage.DependencyAddOptions{EmitEvent: true}); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		// The plain RemoveDependency is the silent structural default — the same
		// call issue delete / bd update reparent / batch / duplicate cleanup make.
		// It removes the edge but records NO dependency_removed event, matching
		// the proxied DeleteAllForIDs(DepInsertOpts{}) path.
		if err := store.RemoveDependency(ctx, src, tgt, "structural"); err != nil {
			t.Fatalf("RemoveDependency (structural): %v", err)
		}
		recs, err := store.GetDependencyRecords(ctx, src)
		if err != nil {
			t.Fatalf("GetDependencyRecords: %v", err)
		}
		if len(recs) != 0 {
			t.Fatalf("structural remove left the edge in place: %+v", recs)
		}
		if n := len(depEventsOfType(t, ctx, store, src, types.EventDependencyRemoved)); n != 0 {
			t.Fatalf("structural remove must not emit dependency_removed, got %d", n)
		}
		assertCommittedEventCount(ctx, t, store.db, src, types.EventDependencyRemoved, 0)
	})

	t.Run("IdempotentReAddDoesNotDoubleEmit", func(t *testing.T) {
		src, tgt := "de-idem-a", "de-idem-b"
		createPerm(t, ctx, store, src)
		createPerm(t, ctx, store, tgt)
		dep := &types.Dependency{IssueID: src, DependsOnID: tgt, Type: types.DepBlocks}
		if err := store.AddDependencyWithOptions(ctx, dep, "tester", storage.DependencyAddOptions{EmitEvent: true}); err != nil {
			t.Fatalf("AddDependency (first): %v", err)
		}
		// Second add of the same edge/type is an idempotent metadata-only update
		// that returns before the INSERT — it must not record a second event.
		if err := store.AddDependencyWithOptions(ctx, dep, "tester", storage.DependencyAddOptions{EmitEvent: true}); err != nil {
			t.Fatalf("AddDependency (idempotent re-add): %v", err)
		}
		if n := len(depEventsOfType(t, ctx, store, src, types.EventDependencyAdded)); n != 1 {
			t.Fatalf("idempotent re-add must not double-emit: dependency_added count = %d, want 1", n)
		}
	})

	t.Run("WispSourceEmitsIntoWispEventTable", func(t *testing.T) {
		src, tgt := "de-wisp-a", "de-wisp-b"
		createWisp(t, ctx, store, src)
		createPerm(t, ctx, store, tgt)
		if err := store.AddDependencyWithOptions(ctx, &types.Dependency{
			IssueID: src, DependsOnID: tgt, Type: types.DepBlocks,
		}, "tester", storage.DependencyAddOptions{EmitEvent: true}); err != nil {
			t.Fatalf("AddDependency (wisp source): %v", err)
		}

		// GetEvents routes a wisp source to wisp_events, so the event is visible
		// there and nowhere in the permanent events table.
		added := depEventsOfType(t, ctx, store, src, types.EventDependencyAdded)
		if len(added) != 1 {
			t.Fatalf("wisp dependency_added event count on %s = %d, want 1", src, len(added))
		}
		if got, want := newValueOf(t, added[0]), "Added dependency: de-wisp-a blocks de-wisp-b"; got != want {
			t.Fatalf("wisp dependency_added new_value = %q, want %q", got, want)
		}
		var permCount int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM events WHERE issue_id = ?", src,
		).Scan(&permCount); err != nil {
			t.Fatalf("count permanent events for wisp source: %v", err)
		}
		if permCount != 0 {
			t.Fatalf("wisp-source event leaked into permanent events table (count=%d)", permCount)
		}
	})
}

// TestRunInTransactionAddRemoveDependencyCommitsEvents is the FIX-1 regression
// guard for the transaction-path plumbing: the explicit-verb tx path
// (AddDependencyWithOptions with EmitEvent) / RemoveDependency emit dep events
// through issueops, and StageAndCommit commits only the dirty set — so the
// source's event table MUST be staged or the event is a torn write (written to
// the working set but never committed, dropped on reset/reconcile). A permanent
// source commits the event; AS OF 'HEAD' reads the committed tree only, so a
// count of 1 proves the event was staged and committed.
func TestRunInTransactionAddRemoveDependencyCommitsEvents(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	src, tgt := "tx-dep-a", "tx-dep-b"
	createPerm(t, ctx, store, src)
	createPerm(t, ctx, store, tgt)

	if err := store.RunInTransaction(ctx, "test: tx add dependency emits event", func(tx storage.Transaction) error {
		return tx.AddDependencyWithOptions(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: types.DepBlocks}, "tester", storage.DependencyAddOptions{EmitEvent: true})
	}); err != nil {
		t.Fatalf("RunInTransaction AddDependency: %v", err)
	}
	assertCommittedEventCount(ctx, t, store.db, src, types.EventDependencyAdded, 1)

	if err := store.RunInTransaction(ctx, "test: tx remove dependency emits event", func(tx storage.Transaction) error {
		return tx.RemoveDependencyWithOptions(ctx, src, tgt, "remover", storage.DependencyRemoveOptions{EmitEvent: true})
	}); err != nil {
		t.Fatalf("RunInTransaction RemoveDependency: %v", err)
	}
	assertCommittedEventCount(ctx, t, store.db, src, types.EventDependencyRemoved, 1)
}

// TestCreateTimeTxAddDependencyDoesNotEmit proves the create-with-deps path —
// tx.AddDependency without EmitEvent, exactly what createIssueWithDeps runs for
// bd create --parent/--deps/--waits-for — wires the edge but records NO
// dependency_added event. This keeps implicit create-time edges quiet and in
// parity with the proxied create-with-deps contract (depRepo.Insert without
// EmitEvent), which the domain consistency test asserts on the proxied side.
func TestCreateTimeTxAddDependencyDoesNotEmit(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	parent, child := "ct-parent", "ct-child"
	createPerm(t, ctx, store, parent)
	createPerm(t, ctx, store, child)

	// Mirror createIssueWithDeps: add the implicit parent-child edge through the
	// plain transaction AddDependency (no EmitEvent), the exact call bd create
	// --parent makes.
	if err := store.RunInTransaction(ctx, "test: create-time parent edge", func(tx storage.Transaction) error {
		return tx.AddDependency(ctx, &types.Dependency{IssueID: child, DependsOnID: parent, Type: types.DepParentChild}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction create-time AddDependency: %v", err)
	}

	// The edge must exist ...
	recs, err := store.GetDependencyRecords(ctx, child)
	if err != nil {
		t.Fatalf("GetDependencyRecords: %v", err)
	}
	if len(recs) != 1 || recs[0].DependsOnID != parent {
		t.Fatalf("create-time parent-child edge missing or wrong: %+v", recs)
	}
	// ... but records NO dependency_added event, in the working set or the commit.
	if n := len(depEventsOfType(t, ctx, store, child, types.EventDependencyAdded)); n != 0 {
		t.Fatalf("create-time parent-child edge must not emit dependency_added, got %d", n)
	}
	assertCommittedEventCount(ctx, t, store.db, child, types.EventDependencyAdded, 0)
}

// TestRunInTransactionWispSourceDependencyRoutesToWispEvents proves the tx-path
// stages the SOURCE's event table wisp-aware: a wisp-source dep records its event
// in wisp_events (the wisp's branch-local home), never in the permanent events
// table. Wisp tables are dolt-ignored, so "persisted" here means readable in the
// working set, which is where all wisp data lives.
func TestRunInTransactionWispSourceDependencyRoutesToWispEvents(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	src, tgt := "tx-wdep-a", "tx-wdep-b"
	createWisp(t, ctx, store, src)
	createWisp(t, ctx, store, tgt)

	if err := store.RunInTransaction(ctx, "test: tx wisp add dependency", func(tx storage.Transaction) error {
		return tx.AddDependencyWithOptions(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: types.DepBlocks}, "tester", storage.DependencyAddOptions{EmitEvent: true})
	}); err != nil {
		t.Fatalf("RunInTransaction wisp AddDependency: %v", err)
	}
	assertEventCountInTable(ctx, t, store.db, "wisp_events", src, types.EventDependencyAdded, 1)
	assertEventCountInTable(ctx, t, store.db, "events", src, types.EventDependencyAdded, 0)

	if err := store.RunInTransaction(ctx, "test: tx wisp remove dependency", func(tx storage.Transaction) error {
		return tx.RemoveDependencyWithOptions(ctx, src, tgt, "remover", storage.DependencyRemoveOptions{EmitEvent: true})
	}); err != nil {
		t.Fatalf("RunInTransaction wisp RemoveDependency: %v", err)
	}
	assertEventCountInTable(ctx, t, store.db, "wisp_events", src, types.EventDependencyRemoved, 1)
	assertEventCountInTable(ctx, t, store.db, "events", src, types.EventDependencyRemoved, 0)
}

// assertEventCountInTable counts working-set rows for issueID/eventType in the
// named event table (events or wisp_events). Used for wisp assertions, where the
// dolt-ignored wisp_events table is never committed to HEAD.
//
//nolint:gosec // G201: table is a test-supplied constant ("events" or "wisp_events").
func assertEventCountInTable(ctx context.Context, t *testing.T, db *sql.DB, table, issueID string, eventType types.EventType, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+table+" WHERE issue_id = ? AND event_type = ?",
		issueID, eventType,
	).Scan(&got); err != nil {
		t.Fatalf("count %s.%s for %s: %v", table, eventType, issueID, err)
	}
	if got != want {
		t.Fatalf("%s.%s count for %s = %d, want %d", table, eventType, issueID, got, want)
	}
}

// TestNoEventDependencyOpsDoNotSweepDirtyEvents is the staging regression for the
// review finding: a no-event dependency op on the permanent Dolt store (structural
// remove, silent structural add, idempotent re-add, or missing-edge remove) must
// stage ONLY the dependencies table for its Dolt commit, never events. Staging
// events would sweep an unrelated pending event row — written by a prior or
// concurrent op that has not committed yet — into the dependency commit under the
// wrong message (GH#2455 stale-working-set capture).
func TestNoEventDependencyOpsDoNotSweepDirtyEvents(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// stageStrayEvent writes an event row directly into the working set without a
	// Dolt commit, simulating a prior op whose own commit is still pending. It
	// returns the row id so the caller can prove the row stays uncommitted.
	stageStrayEvent := func(t *testing.T, issueID, tag string) string {
		t.Helper()
		id := "stray-evt-" + tag
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO events (id, issue_id, event_type, actor, old_value, new_value) VALUES (?, ?, ?, ?, ?, ?)",
			id, issueID, string(types.EventUpdated), "other", "", "uncommitted stray event",
		); err != nil {
			t.Fatalf("stage stray uncommitted event: %v", err)
		}
		return id
	}
	assertUncommitted := func(t *testing.T, strayID string) {
		t.Helper()
		var committed int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM events AS OF 'HEAD' WHERE id = ?", strayID,
		).Scan(&committed); err != nil {
			t.Fatalf("count committed stray event: %v", err)
		}
		if committed != 0 {
			t.Fatalf("a no-event dependency op swept an unrelated pending event into its commit (committed=%d, want 0)", committed)
		}
	}

	t.Run("StructuralRemove", func(t *testing.T) {
		src, tgt := "sweep-rm-a", "sweep-rm-b"
		createPerm(t, ctx, store, src)
		createPerm(t, ctx, store, tgt)
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("AddDependency (silent): %v", err)
		}
		stray := stageStrayEvent(t, src, "rm")
		// A structural (no-event) remove commits the edge deletion; it must stage
		// only dependencies, leaving the stray event uncommitted.
		if err := store.RemoveDependency(ctx, src, tgt, "structural"); err != nil {
			t.Fatalf("RemoveDependency (structural): %v", err)
		}
		assertUncommitted(t, stray)
	})

	t.Run("SilentStructuralAdd", func(t *testing.T) {
		src, tgt := "sweep-add-a", "sweep-add-b"
		createPerm(t, ctx, store, src)
		createPerm(t, ctx, store, tgt)
		stray := stageStrayEvent(t, src, "add")
		// A silent structural add commits the new edge; it must stage only
		// dependencies, leaving the stray event uncommitted.
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("AddDependency (silent): %v", err)
		}
		assertUncommitted(t, stray)
	})

	t.Run("IdempotentReAdd", func(t *testing.T) {
		src, tgt := "sweep-idem-a", "sweep-idem-b"
		createPerm(t, ctx, store, src)
		createPerm(t, ctx, store, tgt)
		dep := &types.Dependency{IssueID: src, DependsOnID: tgt, Type: types.DepBlocks}
		if err := store.AddDependencyWithOptions(ctx, dep, "tester", storage.DependencyAddOptions{EmitEvent: true}); err != nil {
			t.Fatalf("AddDependency (first): %v", err)
		}
		stray := stageStrayEvent(t, src, "idem")
		// An idempotent re-add writes no event even with EmitEvent set, so it must
		// not stage events and must not sweep the stray row.
		if err := store.AddDependencyWithOptions(ctx, dep, "tester", storage.DependencyAddOptions{EmitEvent: true}); err != nil {
			t.Fatalf("AddDependency (idempotent re-add): %v", err)
		}
		assertUncommitted(t, stray)
	})
}
