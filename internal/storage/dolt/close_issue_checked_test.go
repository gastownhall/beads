package dolt

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestCloseIssueChecked exercises the atomic, guarded close. The guard
// (is_blocked) and the close share ONE transaction, so a blocked issue is
// refused with storage.ErrCloseBlocked and — critically — the transaction rolls
// back leaving the issue open with no `closed` event recorded (the atomic-refuse
// property). Force bypasses the guard, and already-closed is an idempotent
// success (Unchanged=true).
func TestCloseIssueChecked(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// mkBlocked creates a blocker and a target with a `blocks` dependency so the
	// target's is_blocked is 1, mirroring the fixtures in is_blocked_test.go.
	mkBlocked := func(t *testing.T, prefix string) string {
		t.Helper()
		blocker, target := prefix+"-blocker", prefix+"-target"
		createPerm(t, ctx, store, blocker)
		createPerm(t, ctx, store, target)
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: target, DependsOnID: blocker, Type: types.DepBlocks,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		if !getIsBlocked(t, ctx, store, "issues", target) {
			t.Fatalf("%s should be is_blocked = 1 after adding blocks dep", target)
		}
		return target
	}

	// mkBlockedWisp is mkBlocked's ephemeral twin: the target is a wisp, so the
	// close routes through closeWispChecked (its own rollback wrapper, no
	// DOLT_COMMIT) rather than the permanent withRetryTx path.
	mkBlockedWisp := func(t *testing.T, prefix string) string {
		t.Helper()
		blocker, target := prefix+"-blocker", prefix+"-target"
		createPerm(t, ctx, store, blocker)
		createWisp(t, ctx, store, target)
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: target, DependsOnID: blocker, Type: types.DepBlocks,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		if !getIsBlocked(t, ctx, store, "wisps", target) {
			t.Fatalf("wisp %s should be is_blocked = 1 after adding blocks dep", target)
		}
		return target
	}

	getStatus := func(t *testing.T, id string) types.Status {
		t.Helper()
		iss, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		if iss == nil {
			t.Fatalf("GetIssue(%s) returned nil issue", id)
		}
		return iss.Status
	}

	countClosedEvents := func(t *testing.T, id string) int {
		t.Helper()
		events, err := store.GetEvents(ctx, id, 0)
		if err != nil {
			t.Fatalf("GetEvents(%s): %v", id, err)
		}
		n := 0
		for _, e := range events {
			if e.EventType == types.EventClosed {
				n++
			}
		}
		return n
	}

	// mkClosed creates a plain issue and closes it via the guarded path so the
	// already-closed case can re-close it.
	mkClosed := func(t *testing.T, id string) string {
		t.Helper()
		createPerm(t, ctx, store, id)
		if _, err := store.CloseIssueChecked(ctx, id, "tester",
			storage.CloseIssueOptions{Reason: "done"}); err != nil {
			t.Fatalf("initial CloseIssueChecked(%s): %v", id, err)
		}
		return id
	}

	// mkClosedBlockedStale creates a closed issue and then forces is_blocked=1
	// back onto the closed row via direct SQL + DOLT_COMMIT. This reproduces the
	// stale denormalization a cross-clone Dolt merge can leave: the schema
	// explicitly models closed+is_blocked=1 rows (GetStatistics filters
	// `is_blocked = 1 AND status <> 'closed'`), and a hand-resolved merge conflict
	// can leave the flag stale indefinitely. No single-clone store API path can
	// otherwise reach this state, because every in-process close recomputes and
	// clears is_blocked for the closed row — so it is seeded the same way
	// blocked_merge_test.go seeds merged state.
	mkClosedBlockedStale := func(t *testing.T, id string) string {
		t.Helper()
		createPerm(t, ctx, store, id)
		if _, err := store.CloseIssueChecked(ctx, id, "tester",
			storage.CloseIssueOptions{Reason: "done"}); err != nil {
			t.Fatalf("initial CloseIssueChecked(%s): %v", id, err)
		}
		if _, err := store.db.ExecContext(ctx,
			"UPDATE issues SET is_blocked = 1 WHERE id = ?", id); err != nil {
			t.Fatalf("force stale is_blocked=1 on %s: %v", id, err)
		}
		if _, err := store.db.ExecContext(ctx,
			"CALL DOLT_COMMIT('-Am', 'simulate merged stale is_blocked')"); err != nil && !isDoltNothingToCommit(err) {
			t.Fatalf("commit stale is_blocked for %s: %v", id, err)
		}
		// Guard the fixture itself: the row must be closed AND carry the stale
		// is_blocked=1 the regression depends on.
		if got := getStatus(t, id); got != types.StatusClosed {
			t.Fatalf("%s should be closed for the regression fixture, got %q", id, got)
		}
		if !getIsBlocked(t, ctx, store, "issues", id) {
			t.Fatalf("%s should carry stale is_blocked=1 for the regression fixture", id)
		}
		return id
	}

	tests := []struct {
		name  string
		setup func(t *testing.T) string
		opts  storage.CloseIssueOptions
		check func(t *testing.T, id string, res storage.CloseIssueResult, err error)
	}{
		{
			name:  "blocked without force refuses and rolls back",
			setup: func(t *testing.T) string { return mkBlocked(t, "cic-noforce") },
			opts:  storage.CloseIssueOptions{Reason: "done"},
			check: func(t *testing.T, id string, res storage.CloseIssueResult, err error) {
				if !errors.Is(err, storage.ErrCloseBlocked) {
					t.Fatalf("err = %v, want errors.Is(_, ErrCloseBlocked)", err)
				}
				// Atomic refuse: the issue must still be open (not closed) and
				// no closed event may have been written — the guard aborted the
				// tx before the close ran.
				if got := getStatus(t, id); got == types.StatusClosed {
					t.Fatalf("issue %s status = closed after refused close; guard did not abort", id)
				}
				if n := countClosedEvents(t, id); n != 0 {
					t.Fatalf("closed event count for %s = %d, want 0 (tx must have rolled back)", id, n)
				}
			},
		},
		{
			name:  "blocked with force closes",
			setup: func(t *testing.T) string { return mkBlocked(t, "cic-force") },
			opts:  storage.CloseIssueOptions{Reason: "done", Force: true},
			check: func(t *testing.T, id string, res storage.CloseIssueResult, err error) {
				if err != nil {
					t.Fatalf("Force close err = %v, want nil", err)
				}
				if res.Unchanged {
					t.Fatalf("res.Unchanged = true, want false (a real close)")
				}
				if got := getStatus(t, id); got != types.StatusClosed {
					t.Fatalf("issue %s status = %q, want closed", id, got)
				}
			},
		},
		{
			name: "not blocked closes",
			setup: func(t *testing.T) string {
				createPerm(t, ctx, store, "cic-plain")
				return "cic-plain"
			},
			opts: storage.CloseIssueOptions{Reason: "done"},
			check: func(t *testing.T, id string, res storage.CloseIssueResult, err error) {
				if err != nil {
					t.Fatalf("close err = %v, want nil", err)
				}
				if res.Unchanged {
					t.Fatalf("res.Unchanged = true, want false (a real close)")
				}
				if got := getStatus(t, id); got != types.StatusClosed {
					t.Fatalf("issue %s status = %q, want closed", id, got)
				}
			},
		},
		{
			// The guard refuses only on a LIVE direct blocker, matching the
			// historical `bd close` predicate (blocked && len(blockers) > 0). A
			// transitively-blocked child (parent-child of a blocked parent) has
			// is_blocked=1 but NO direct blocker of its own, so it must close
			// without Force.
			name: "transitively blocked child closes without force",
			setup: func(t *testing.T) string {
				blocker, parent, child := "cic-trans-blocker", "cic-trans-parent", "cic-trans-child"
				createPerm(t, ctx, store, blocker)
				createPerm(t, ctx, store, parent)
				createPerm(t, ctx, store, child)
				if err := store.AddDependency(ctx, &types.Dependency{
					IssueID: parent, DependsOnID: blocker, Type: types.DepBlocks,
				}, "tester"); err != nil {
					t.Fatalf("AddDependency(parent blocks blocker): %v", err)
				}
				if err := store.AddDependency(ctx, &types.Dependency{
					IssueID: child, DependsOnID: parent, Type: types.DepParentChild,
				}, "tester"); err != nil {
					t.Fatalf("AddDependency(parent-child): %v", err)
				}
				// Precondition: the child inherits is_blocked=1 from its blocked
				// parent, with no direct blocker edge of its own.
				if !getIsBlocked(t, ctx, store, "issues", child) {
					t.Fatalf("%s should inherit is_blocked=1 from blocked parent %s", child, parent)
				}
				return child
			},
			opts: storage.CloseIssueOptions{Reason: "done"},
			check: func(t *testing.T, id string, res storage.CloseIssueResult, err error) {
				if err != nil {
					t.Fatalf("transitive-blocked close err = %v, want nil (no live direct blocker)", err)
				}
				if res.Unchanged {
					t.Fatalf("res.Unchanged = true, want false (a real close)")
				}
				if got := getStatus(t, id); got != types.StatusClosed {
					t.Fatalf("issue %s status = %q, want closed", id, got)
				}
			},
		},
		{
			// A stale is_blocked=1 (its only direct blocker has since closed, but
			// the denormalized column was not recomputed) must NOT refuse: the
			// guard reads the LIVE blocker list — empty because the blocker is
			// closed — and self-heals by closing.
			name: "stale is_blocked with closed direct blocker closes",
			setup: func(t *testing.T) string {
				blocker, target := "cic-stale-blocker", "cic-stale-target"
				createPerm(t, ctx, store, blocker)
				createPerm(t, ctx, store, target)
				if err := store.AddDependency(ctx, &types.Dependency{
					IssueID: target, DependsOnID: blocker, Type: types.DepBlocks,
				}, "tester"); err != nil {
					t.Fatalf("AddDependency(blocks): %v", err)
				}
				if !getIsBlocked(t, ctx, store, "issues", target) {
					t.Fatalf("%s should be is_blocked=1 after adding blocks dep", target)
				}
				// Close the blocker via a raw UPDATE that skips the is_blocked
				// recompute, leaving target.is_blocked stale at 1 while its only
				// direct blocker is now closed.
				if _, err := store.db.ExecContext(ctx,
					"UPDATE issues SET status = 'closed' WHERE id = ?", blocker); err != nil {
					t.Fatalf("raw-close blocker: %v", err)
				}
				if !getIsBlocked(t, ctx, store, "issues", target) {
					t.Fatalf("%s is_blocked column should still read stale-1", target)
				}
				return target
			},
			opts: storage.CloseIssueOptions{Reason: "done"},
			check: func(t *testing.T, id string, res storage.CloseIssueResult, err error) {
				if err != nil {
					t.Fatalf("stale-blocked close err = %v, want nil (live blocker is closed; self-heals)", err)
				}
				if res.Unchanged {
					t.Fatalf("res.Unchanged = true, want false (a real close)")
				}
				if got := getStatus(t, id); got != types.StatusClosed {
					t.Fatalf("issue %s status = %q, want closed", id, got)
				}
			},
		},
		{
			name:  "already closed is idempotent",
			setup: func(t *testing.T) string { return mkClosed(t, "cic-idem") },
			opts:  storage.CloseIssueOptions{Reason: "again"},
			check: func(t *testing.T, id string, res storage.CloseIssueResult, err error) {
				if err != nil {
					t.Fatalf("re-close err = %v, want nil", err)
				}
				if !res.Unchanged {
					t.Fatalf("res.Unchanged = false, want true (already closed)")
				}
				if got := getStatus(t, id); got != types.StatusClosed {
					t.Fatalf("issue %s status = %q, want closed", id, got)
				}
			},
		},
		{
			// Regression: a non-force re-close of an ALREADY-CLOSED row that still
			// carries a stale is_blocked=1 must stay idempotent (Unchanged=true).
			// The guard only has meaning for an open→closed transition, so it must
			// not fire on a row that is already closed — otherwise the documented
			// idempotency contract breaks once bd close is wired to this primitive.
			name:  "already closed with stale is_blocked=1 is idempotent, not refused",
			setup: func(t *testing.T) string { return mkClosedBlockedStale(t, "cic-idem-stale") },
			opts:  storage.CloseIssueOptions{Reason: "again"},
			check: func(t *testing.T, id string, res storage.CloseIssueResult, err error) {
				if errors.Is(err, storage.ErrCloseBlocked) {
					t.Fatalf("re-close of already-closed %s refused with ErrCloseBlocked; "+
						"the guard must not fire on an already-closed row (stale is_blocked=1)", id)
				}
				if err != nil {
					t.Fatalf("re-close err = %v, want nil", err)
				}
				if !res.Unchanged {
					t.Fatalf("res.Unchanged = false, want true (already closed)")
				}
				if got := getStatus(t, id); got != types.StatusClosed {
					t.Fatalf("issue %s status = %q, want closed", id, got)
				}
			},
		},
		{
			name:  "force on already closed is unchanged",
			setup: func(t *testing.T) string { return mkClosed(t, "cic-force-idem") },
			opts:  storage.CloseIssueOptions{Reason: "again", Force: true},
			check: func(t *testing.T, id string, res storage.CloseIssueResult, err error) {
				if err != nil {
					t.Fatalf("force re-close err = %v, want nil", err)
				}
				if !res.Unchanged {
					t.Fatalf("res.Unchanged = false, want true (already closed, even with Force)")
				}
				if got := getStatus(t, id); got != types.StatusClosed {
					t.Fatalf("issue %s status = %q, want closed", id, got)
				}
			},
		},
		{
			name: "non-blocking dep does not trip guard",
			setup: func(t *testing.T) string {
				createPerm(t, ctx, store, "cic-rel-other")
				createPerm(t, ctx, store, "cic-rel-target")
				if err := store.AddDependency(ctx, &types.Dependency{
					IssueID: "cic-rel-target", DependsOnID: "cic-rel-other", Type: types.DepRelated,
				}, "tester"); err != nil {
					t.Fatalf("AddDependency(related): %v", err)
				}
				if getIsBlocked(t, ctx, store, "issues", "cic-rel-target") {
					t.Fatalf("cic-rel-target should NOT be is_blocked with only a related dep")
				}
				return "cic-rel-target"
			},
			opts: storage.CloseIssueOptions{Reason: "done"},
			check: func(t *testing.T, id string, res storage.CloseIssueResult, err error) {
				if err != nil {
					t.Fatalf("close with non-blocking dep err = %v, want nil", err)
				}
				if res.Unchanged {
					t.Fatalf("res.Unchanged = true, want false (a real close)")
				}
				if got := getStatus(t, id); got != types.StatusClosed {
					t.Fatalf("issue %s status = %q, want closed", id, got)
				}
			},
		},
		{
			name:  "wisp blocked without force refuses and rolls back",
			setup: func(t *testing.T) string { return mkBlockedWisp(t, "cic-wisp-noforce") },
			opts:  storage.CloseIssueOptions{Reason: "done"},
			check: func(t *testing.T, id string, res storage.CloseIssueResult, err error) {
				if !errors.Is(err, storage.ErrCloseBlocked) {
					t.Fatalf("err = %v, want errors.Is(_, ErrCloseBlocked)", err)
				}
				// closeWispChecked's deferred Rollback must discard the tx: the
				// wisp stays open with no closed event.
				if got := getStatus(t, id); got == types.StatusClosed {
					t.Fatalf("wisp %s status = closed after refused close; guard did not abort", id)
				}
				if n := countClosedEvents(t, id); n != 0 {
					t.Fatalf("closed event count for wisp %s = %d, want 0 (tx must have rolled back)", id, n)
				}
			},
		},
		{
			name:  "wisp blocked with force closes",
			setup: func(t *testing.T) string { return mkBlockedWisp(t, "cic-wisp-force") },
			opts:  storage.CloseIssueOptions{Reason: "done", Force: true},
			check: func(t *testing.T, id string, res storage.CloseIssueResult, err error) {
				if err != nil {
					t.Fatalf("Force close wisp err = %v, want nil", err)
				}
				if res.Unchanged {
					t.Fatalf("res.Unchanged = true, want false (a real close)")
				}
				if got := getStatus(t, id); got != types.StatusClosed {
					t.Fatalf("wisp %s status = %q, want closed", id, got)
				}
			},
		},
		{
			name:  "missing id returns ErrNotFound",
			setup: func(t *testing.T) string { return "cic-does-not-exist" },
			opts:  storage.CloseIssueOptions{Reason: "done"},
			check: func(t *testing.T, id string, res storage.CloseIssueResult, err error) {
				if !errors.Is(err, storage.ErrNotFound) {
					t.Fatalf("err = %v, want errors.Is(_, ErrNotFound)", err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.setup(t)
			res, err := store.CloseIssueChecked(ctx, id, "tester", tc.opts)
			tc.check(t, id, res, err)
		})
	}

	t.Run("open children refuse and force reports count", func(t *testing.T) {
		parent, permanentChild, wispChild := "cic-children-parent", "cic-children-permanent", "cic-children-wisp"
		blocker := "cic-children-blocker"
		createPerm(t, ctx, store, parent)
		createPerm(t, ctx, store, permanentChild)
		createWisp(t, ctx, store, wispChild)
		createPerm(t, ctx, store, blocker)
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: parent, DependsOnID: blocker, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("AddDependency(%s -> %s): %v", parent, blocker, err)
		}
		for _, child := range []string{permanentChild, wispChild} {
			if err := store.AddDependency(ctx, &types.Dependency{IssueID: child, DependsOnID: parent, Type: types.DepParentChild}, "tester"); err != nil {
				t.Fatalf("AddDependency(%s -> %s): %v", child, parent, err)
			}
		}
		res, err := store.CloseIssueChecked(ctx, parent, "tester", storage.CloseIssueOptions{Reason: "done"})
		var childErr *storage.CloseOpenChildrenError
		if !errors.Is(err, storage.ErrCloseOpenChildren) || !errors.As(err, &childErr) || childErr.IssueID != parent || childErr.OpenChildren != 2 {
			t.Fatalf("child error = %+v, err = %v; want typed refusal for %s with 2 children", childErr, err, parent)
		}
		if got, want := getStatus(t, parent), types.StatusOpen; got != want {
			t.Fatalf("parent status after refusal = %q, want %q", got, want)
		}
		res, err = store.CloseIssueChecked(ctx, parent, "tester", storage.CloseIssueOptions{Reason: "override", Force: true})
		if err != nil || res.Unchanged || res.OpenChildren != 2 {
			t.Fatalf("force result = %+v, err = %v; want changed close with 2 children", res, err)
		}
		res, err = store.CloseIssueChecked(ctx, parent, "tester", storage.CloseIssueOptions{Reason: "again"})
		if !errors.Is(err, storage.ErrCloseOpenChildren) {
			t.Fatalf("non-force re-close error = %v, want ErrCloseOpenChildren", err)
		}
		res, err = store.CloseIssueChecked(ctx, parent, "tester", storage.CloseIssueOptions{Reason: "again", Force: true})
		if err != nil || !res.Unchanged || res.OpenChildren != 2 {
			t.Fatalf("forced re-close result = %+v, err = %v; want unchanged with 2 children", res, err)
		}

		wispParent, childOfWispParent := "cic-children-wisp-parent", "cic-children-wisp-parent-child"
		createWisp(t, ctx, store, wispParent)
		createPerm(t, ctx, store, childOfWispParent)
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: childOfWispParent, DependsOnID: wispParent, Type: types.DepParentChild}, "tester"); err != nil {
			t.Fatalf("AddDependency(%s -> %s): %v", childOfWispParent, wispParent, err)
		}
		res, err = store.CloseIssueChecked(ctx, wispParent, "tester", storage.CloseIssueOptions{Reason: "done"})
		if !errors.Is(err, storage.ErrCloseOpenChildren) {
			t.Fatalf("wisp parent close error = %v, want ErrCloseOpenChildren", err)
		}
		res, err = store.CloseIssueChecked(ctx, wispParent, "tester", storage.CloseIssueOptions{Reason: "override", Force: true})
		if err != nil || res.Unchanged || res.OpenChildren != 1 {
			t.Fatalf("wisp parent force result = %+v, err = %v; want changed close with 1 child", res, err)
		}
	})

	t.Run("duplicate durable and wisp child edge counts once", func(t *testing.T) {
		parent, child := "cic-collision-parent", "cic-collision-child"
		createPerm(t, ctx, store, parent)
		createPerm(t, ctx, store, child)
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: child, DependsOnID: parent, Type: types.DepParentChild,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency(%s -> %s): %v", child, parent, err)
		}
		if _, err := store.execContext(ctx, `
			INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral, no_history)
			VALUES (?, ?, '', '', '', '', ?, ?, ?, ?, ?)
		`, child, "wisp duplicate "+child, types.StatusOpen, 2, types.TypeTask, false, true); err != nil {
			t.Fatalf("insert matching wisp child: %v", err)
		}

		collisionID := depid.New(child, parent)
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES (?, ?, ?, 'parent-child', 'tester')",
			collisionID, child, parent); err != nil {
			t.Fatalf("seed duplicate child edge: %v", err)
		}

		res, err := store.CloseIssueChecked(ctx, parent, "tester", storage.CloseIssueOptions{Reason: "done"})
		var childErr *storage.CloseOpenChildrenError
		if !errors.Is(err, storage.ErrCloseOpenChildren) || !errors.As(err, &childErr) || childErr.OpenChildren != 1 {
			t.Fatalf("child error = %+v, err = %v; want typed refusal with 1 child", childErr, err)
		}
		res, err = store.CloseIssueChecked(ctx, parent, "tester", storage.CloseIssueOptions{Reason: "override", Force: true})
		if err != nil || res.Unchanged || res.OpenChildren != 1 {
			t.Fatalf("force result = %+v, err = %v; want changed close with 1 child", res, err)
		}
	})

	t.Run("durable dependency wins a cross-table type conflict", func(t *testing.T) {
		parent, child := "cic-conflict-parent", "cic-conflict-child"
		createPerm(t, ctx, store, parent)
		createPerm(t, ctx, store, child)
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: child, DependsOnID: parent, Type: types.DepRelated,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency(%s -> %s): %v", child, parent, err)
		}
		if _, err := store.execContext(ctx, `
			INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral, no_history)
			VALUES (?, ?, '', '', '', '', ?, ?, ?, ?, ?)
		`, child, "wisp duplicate "+child, types.StatusOpen, 2, types.TypeTask, false, true); err != nil {
			t.Fatalf("insert matching wisp child: %v", err)
		}

		collisionID := depid.New(child, parent)
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES (?, ?, ?, 'parent-child', 'tester')",
			collisionID, child, parent); err != nil {
			t.Fatalf("seed conflicting child edge: %v", err)
		}

		res, err := store.CloseIssueChecked(ctx, parent, "tester", storage.CloseIssueOptions{Reason: "done"})
		if err != nil || res.Unchanged || res.OpenChildren != 0 {
			t.Fatalf("close result = %+v, err = %v; want changed close with no durable parent-child child", res, err)
		}
	})

	t.Run("missing target precedes child count", func(t *testing.T) {
		child, missing := "cic-dangling-child", "cic-dangling-parent"
		createPerm(t, ctx, store, child)
		if _, err := store.db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
			t.Fatalf("disable FK checks: %v", err)
		}
		if _, err := store.db.ExecContext(ctx, "INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, 'parent-child', NOW(), 'tester')", child, missing); err != nil {
			t.Fatalf("insert dangling parent-child edge: %v", err)
		}
		if _, err := store.db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
			t.Fatalf("re-enable FK checks: %v", err)
		}
		_, err := store.CloseIssueChecked(ctx, missing, "tester", storage.CloseIssueOptions{Reason: "done"})
		if !errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrCloseOpenChildren) {
			t.Fatalf("err = %v, want ErrNotFound but not ErrCloseOpenChildren", err)
		}
	})
}

// TestCloseIssueCheckedParentChildInsertCannotPhantomMerge is a controlled
// two-transaction Dolt regression: a close which observes no children and a
// concurrently-created open parent-child edge must not both commit. If they do,
// the resulting durable state can preserve an overlapping stale-snapshot
// decision instead of forcing one writer to retry against the other.
func TestCloseIssueCheckedParentChildInsertCannotPhantomMerge(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const (
		parent = "close-child-phantom-parent"
		child  = "close-child-phantom-child"
	)
	createPerm(t, ctx, store, parent)
	createPerm(t, ctx, store, child)

	// Deliberately start both transactions before either writes. The close reads
	// zero children before txAdd inserts its edge; committing close first makes
	// this interleaving deterministic rather than timing-dependent.
	txClose, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin close transaction: %v", err)
	}
	defer txClose.Rollback() // no-op after Commit; preserves cleanup on early Fatal.
	txAdd, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin add-child transaction: %v", err)
	}
	defer txAdd.Rollback()

	closeResult, err := issueops.CloseIssueCheckedInTx(ctx, txClose, parent, "done", "tester", "", false, nil)
	if err != nil {
		t.Fatalf("checked close before concurrent edge: %v", err)
	}
	if closeResult.OpenChildren != 0 || closeResult.AlreadyClosed {
		t.Fatalf("close precondition result = %+v, want a real close after observing zero children", closeResult)
	}

	if _, err := issueops.AddDependencyInTx(ctx, txAdd, &types.Dependency{
		IssueID: child, DependsOnID: parent, Type: types.DepParentChild,
	}, "tester", issueops.AddDependencyOpts{}); err != nil {
		t.Fatalf("insert concurrent parent-child edge: %v", err)
	}

	closeCommitErr := txClose.Commit()
	addCommitErr := txAdd.Commit()
	t.Logf("controlled close/add commits: close=%v add=%v", closeCommitErr, addCommitErr)
	if closeCommitErr == nil && addCommitErr == nil {
		var parentStatus, childStatus string
		if err := store.db.QueryRowContext(ctx, "SELECT status FROM issues WHERE id = ?", parent).Scan(&parentStatus); err != nil {
			t.Fatalf("read parent after both commits: %v", err)
		}
		if err := store.db.QueryRowContext(ctx, "SELECT status FROM issues WHERE id = ?", child).Scan(&childStatus); err != nil {
			t.Fatalf("read child after both commits: %v", err)
		}
		t.Fatalf("phantom parent-child merge: checked close and edge insert both committed (parent=%s child=%s); one transaction must conflict or be refused", parentStatus, childStatus)
	}

	// A conflict on either commit is the required serialization outcome. Keep
	// both errors in the failure below should a future Dolt version return an
	// unexpected non-serialization result from a changed transaction contract.
	if closeCommitErr != nil && !isSerializationError(closeCommitErr) {
		t.Fatalf("close commit error = %v, want a serialization conflict", closeCommitErr)
	}
	if addCommitErr != nil && !isSerializationError(addCommitErr) {
		t.Fatalf("add-child commit error = %v, want a serialization conflict", addCommitErr)
	}
}

// TestCloseIssueCheckedPinnedConnRefusalRestoresCoordinationSavepoint proves
// that the production pinned-connection UOW can catch a checked-close refusal
// and commit a sibling operation without persisting the refused close's
// coordination writes.
func TestCloseIssueCheckedPinnedConnRefusalRestoresCoordinationSavepoint(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const (
		parent  = "close-savepoint-parent"
		child   = "close-savepoint-child"
		sibling = "close-savepoint-sibling"
	)
	for _, id := range []string{parent, child, sibling} {
		createPerm(t, ctx, store, id)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: child, DependsOnID: parent, Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency(%s -> %s): %v", child, parent, err)
	}

	coordinationValue := func(t *testing.T, tier string) *string {
		t.Helper()
		var value string
		err := store.db.QueryRowContext(ctx,
			"SELECT value FROM local_metadata WHERE `key` LIKE ?",
			"dependency-coordination/v1/"+tier+"/%",
		).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			t.Fatalf("read %s coordination cell: %v", tier, err)
		}
		return &value
	}
	beforeDurable := coordinationValue(t, "dependencies")
	beforeWisp := coordinationValue(t, "wisp_dependencies")
	if beforeDurable == nil || beforeWisp != nil {
		t.Fatalf("pre-call coordination = durable:%v wisp:%v, want durable token and absent wisp token", beforeDurable, beforeWisp)
	}

	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin transaction connection: %v", err)
	}
	transactionOpen := true
	defer func() {
		if transactionOpen {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
		_ = conn.Close()
	}()
	if _, err := conn.ExecContext(ctx, "START TRANSACTION"); err != nil {
		t.Fatalf("start pinned-connection transaction: %v", err)
	}

	_, err = issueops.CloseIssueCheckedInTx(ctx, conn, parent, "done", "tester", "", false, nil)
	if !errors.Is(err, storage.ErrCloseOpenChildren) {
		t.Fatalf("checked close error = %v, want ErrCloseOpenChildren", err)
	}
	if _, err := issueops.CloseIssueInTx(ctx, conn, sibling, "done", "tester", ""); err != nil {
		t.Fatalf("close sibling after refused parent close: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("commit sibling close after refusal: %v", err)
	}
	transactionOpen = false
	if err := conn.Close(); err != nil {
		t.Fatalf("release committed transaction connection: %v", err)
	}

	for _, check := range []struct {
		id     string
		status types.Status
	}{{parent, types.StatusOpen}, {sibling, types.StatusClosed}} {
		issue, err := store.GetIssue(ctx, check.id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", check.id, err)
		}
		if issue.Status != check.status {
			t.Fatalf("%s status = %q, want %q", check.id, issue.Status, check.status)
		}
	}

	for _, check := range []struct {
		tier string
		want *string
	}{
		{tier: "dependencies", want: beforeDurable},
		{tier: "wisp_dependencies", want: beforeWisp},
	} {
		got := coordinationValue(t, check.tier)
		if (got == nil) != (check.want == nil) {
			t.Fatalf("%s coordination presence = %v, want %v", check.tier, got != nil, check.want != nil)
		}
		if got != nil && *got != *check.want {
			t.Fatalf("%s coordination value = %q, want pre-call %q", check.tier, *got, *check.want)
		}
	}
}

// TestCloseIssueCheckedVersionCAS exercises the optional ExpectedVersion
// optimistic-concurrency check layered onto the guarded close. The version read
// and the close share ONE transaction, so a stale version is refused with
// storage.ErrVersionMismatch and — critically — the transaction rolls back
// leaving the issue open with no `closed` event (the atomic-refuse property,
// mirroring the is_blocked guard). The CAS is orthogonal to Force: a stale
// version is refused even with Force set. A nil ExpectedVersion disables the
// check, so behavior is unchanged from S2.
func TestCloseIssueCheckedVersionCAS(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	rowVersion := func(t *testing.T, id string) int64 {
		t.Helper()
		iss, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		if iss == nil {
			t.Fatalf("GetIssue(%s) returned nil issue", id)
		}
		return iss.RowVersion
	}
	getStatus := func(t *testing.T, id string) types.Status {
		t.Helper()
		iss, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		return iss.Status
	}
	countClosedEvents := func(t *testing.T, id string) int {
		t.Helper()
		events, err := store.GetEvents(ctx, id, 0)
		if err != nil {
			t.Fatalf("GetEvents(%s): %v", id, err)
		}
		n := 0
		for _, e := range events {
			if e.EventType == types.EventClosed {
				n++
			}
		}
		return n
	}
	ptr := func(v int64) *int64 { return &v }

	t.Run("matching version closes", func(t *testing.T) {
		createPerm(t, ctx, store, "cas-match")
		v := rowVersion(t, "cas-match")
		res, err := store.CloseIssueChecked(ctx, "cas-match", "tester",
			storage.CloseIssueOptions{Reason: "done", ExpectedVersion: ptr(v)})
		if err != nil {
			t.Fatalf("close with matching version err = %v, want nil", err)
		}
		if res.Unchanged {
			t.Fatalf("res.Unchanged = true, want false (a real close)")
		}
		if got := getStatus(t, "cas-match"); got != types.StatusClosed {
			t.Fatalf("cas-match status = %q, want closed", got)
		}
	})

	t.Run("stale version refuses atomically", func(t *testing.T) {
		createPerm(t, ctx, store, "cas-stale")
		v := rowVersion(t, "cas-stale")
		// v+1 is a version the row provably does not hold.
		res, err := store.CloseIssueChecked(ctx, "cas-stale", "tester",
			storage.CloseIssueOptions{Reason: "done", ExpectedVersion: ptr(v + 1)})
		if !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrVersionMismatch)", err)
		}
		if res.Unchanged {
			t.Fatalf("res.Unchanged = true on mismatch, want false")
		}
		// Atomic refuse: still open, no closed event — the CAS aborted the tx
		// before the close ran.
		if got := getStatus(t, "cas-stale"); got == types.StatusClosed {
			t.Fatalf("cas-stale status = closed after refused close; CAS did not abort")
		}
		if n := countClosedEvents(t, "cas-stale"); n != 0 {
			t.Fatalf("closed event count for cas-stale = %d, want 0 (tx must have rolled back)", n)
		}
	})

	t.Run("concurrent write invalidates captured version", func(t *testing.T) {
		createPerm(t, ctx, store, "cas-concurrent")
		v1 := rowVersion(t, "cas-concurrent")
		// A mutating write rewrites row_lock, so the captured v1 is now stale.
		if err := store.UpdateIssue(ctx, "cas-concurrent",
			map[string]interface{}{"priority": 1}, "tester"); err != nil {
			t.Fatalf("UpdateIssue: %v", err)
		}
		if v2 := rowVersion(t, "cas-concurrent"); v2 == v1 {
			t.Fatalf("RowVersion unchanged after UpdateIssue (%d); CAS could not detect the write", v2)
		}
		res, err := store.CloseIssueChecked(ctx, "cas-concurrent", "tester",
			storage.CloseIssueOptions{Reason: "done", ExpectedVersion: ptr(v1)})
		if !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrVersionMismatch)", err)
		}
		if res.Unchanged {
			t.Fatalf("res.Unchanged = true on stale close, want false")
		}
		if got := getStatus(t, "cas-concurrent"); got == types.StatusClosed {
			t.Fatalf("cas-concurrent closed despite stale version")
		}
	})

	t.Run("nil ExpectedVersion skips the check", func(t *testing.T) {
		createPerm(t, ctx, store, "cas-nil")
		res, err := store.CloseIssueChecked(ctx, "cas-nil", "tester",
			storage.CloseIssueOptions{Reason: "done", ExpectedVersion: nil})
		if err != nil {
			t.Fatalf("nil ExpectedVersion close err = %v, want nil (back-compat)", err)
		}
		if res.Unchanged {
			t.Fatalf("res.Unchanged = true, want false (a real close)")
		}
		if got := getStatus(t, "cas-nil"); got != types.StatusClosed {
			t.Fatalf("cas-nil status = %q, want closed", got)
		}
	})

	t.Run("Force does not bypass the version check", func(t *testing.T) {
		createPerm(t, ctx, store, "cas-force")
		v := rowVersion(t, "cas-force")
		res, err := store.CloseIssueChecked(ctx, "cas-force", "tester",
			storage.CloseIssueOptions{Reason: "done", Force: true, ExpectedVersion: ptr(v + 1)})
		if !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("err = %v, want errors.Is(_, ErrVersionMismatch) even with Force", err)
		}
		if res.Unchanged {
			t.Fatalf("res.Unchanged = true on mismatch+Force, want false")
		}
		if got := getStatus(t, "cas-force"); got == types.StatusClosed {
			t.Fatalf("cas-force closed under Force despite stale version; CAS is not orthogonal to Force")
		}
	})

	t.Run("missing id returns ErrNotFound", func(t *testing.T) {
		res, err := store.CloseIssueChecked(ctx, "cas-missing", "tester",
			storage.CloseIssueOptions{Reason: "done", ExpectedVersion: ptr(1)})
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("err = %v, want errors.Is(_, ErrNotFound) for absent row", err)
		}
		if res.Unchanged {
			t.Fatalf("res.Unchanged = true for missing id, want false")
		}
	})

	// The wisp subtests exercise CheckVersionInTx's wisp routing (SELECT row_lock
	// FROM wisps) and the wisp atomic-refuse seam: closeWispChecked uses a bare
	// BeginTx with a deferred Rollback (no withRetryTx, matching the package-wide
	// wisp write pattern), so a stale version must leave the wisp untouched.
	t.Run("wisp matching version closes", func(t *testing.T) {
		createWisp(t, ctx, store, "cas-wisp-match")
		// Reading the version at all requires the wisp route: a read against the
		// issues table for this id would miss, so a successful close here proves
		// CheckVersionInTx routed to the wisps table.
		v := rowVersion(t, "cas-wisp-match")
		res, err := store.CloseIssueChecked(ctx, "cas-wisp-match", "tester",
			storage.CloseIssueOptions{Reason: "done", ExpectedVersion: ptr(v)})
		if err != nil {
			t.Fatalf("wisp close with matching version err = %v, want nil", err)
		}
		if res.Unchanged {
			t.Fatalf("res.Unchanged = true, want false (a real wisp close)")
		}
		if got := getStatus(t, "cas-wisp-match"); got != types.StatusClosed {
			t.Fatalf("cas-wisp-match status = %q, want closed", got)
		}
	})

	t.Run("wisp stale version refuses atomically", func(t *testing.T) {
		createWisp(t, ctx, store, "cas-wisp-stale")
		v := rowVersion(t, "cas-wisp-stale")
		res, err := store.CloseIssueChecked(ctx, "cas-wisp-stale", "tester",
			storage.CloseIssueOptions{Reason: "done", ExpectedVersion: ptr(v + 1)})
		if !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("wisp err = %v, want errors.Is(_, ErrVersionMismatch)", err)
		}
		if res.Unchanged {
			t.Fatalf("res.Unchanged = true on wisp mismatch, want false")
		}
		// closeWispChecked's deferred Rollback must discard the tx: the wisp stays
		// open with no closed event (atomic-refuse on the wisp path).
		if got := getStatus(t, "cas-wisp-stale"); got == types.StatusClosed {
			t.Fatalf("cas-wisp-stale status = closed after refused close; wisp CAS did not abort")
		}
		if n := countClosedEvents(t, "cas-wisp-stale"); n != 0 {
			t.Fatalf("closed event count for cas-wisp-stale = %d, want 0 (wisp tx must have rolled back)", n)
		}
	})

	// Idempotency composes with the CAS: re-closing an already-closed issue with
	// its CURRENT (post-close) version passes the version check and short-circuits
	// to the idempotent Unchanged result — not a spurious mismatch and not a
	// second close event.
	t.Run("already closed re-close with post-close version is idempotent", func(t *testing.T) {
		createPerm(t, ctx, store, "cas-idem")
		if _, err := store.CloseIssueChecked(ctx, "cas-idem", "tester",
			storage.CloseIssueOptions{Reason: "done"}); err != nil {
			t.Fatalf("initial close err = %v, want nil", err)
		}
		postClose := rowVersion(t, "cas-idem")
		if n := countClosedEvents(t, "cas-idem"); n != 1 {
			t.Fatalf("closed event count after first close = %d, want 1", n)
		}
		res, err := store.CloseIssueChecked(ctx, "cas-idem", "tester",
			storage.CloseIssueOptions{Reason: "again", ExpectedVersion: ptr(postClose)})
		if err != nil {
			t.Fatalf("re-close with post-close version err = %v, want nil (CAS passes, idempotent)", err)
		}
		if !res.Unchanged {
			t.Fatalf("res.Unchanged = false, want true (already closed)")
		}
		if n := countClosedEvents(t, "cas-idem"); n != 1 {
			t.Fatalf("closed event count after idempotent re-close = %d, want 1 (no second close)", n)
		}
	})
}
