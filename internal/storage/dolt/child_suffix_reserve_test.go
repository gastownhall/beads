package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// childSuffixEnv drives the GH#4750 child-suffix cases on one server-backed
// store: a parent whose counter is missing (the legacy shape: a database
// written before the singular create reconciled, or a child created through a
// door that did not advance it), and the operation under test, after which the
// next generated child must not re-mint a suffix the parent already used.
type childSuffixEnv struct {
	store *DoltStore
	ctx   context.Context
}

func newChildSuffixEnv(t *testing.T) *childSuffixEnv {
	t.Helper()
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)
	return &childSuffixEnv{store: store, ctx: ctx}
}

func (e *childSuffixEnv) create(t *testing.T, id string, wisp bool) {
	t.Helper()
	issue := &types.Issue{ID: id, Title: "child suffix " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: wisp}
	if err := e.store.CreateIssue(e.ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue(%s): %v", id, err)
	}
}

// dropCounter removes parent's counter row from both planes, leaving the
// legacy state the reservation must survive.
func (e *childSuffixEnv) dropCounter(t *testing.T, parent string) {
	t.Helper()
	for _, table := range []string{"child_counters", "wisp_child_counters"} {
		//nolint:gosec // G202: table is one of two constants.
		if _, err := e.store.db.ExecContext(e.ctx, "DELETE FROM "+table+" WHERE parent_id = ?", parent); err != nil {
			t.Fatalf("drop %s[%s]: %v", table, parent, err)
		}
	}
}

func (e *childSuffixEnv) counter(t *testing.T, table, parent string) int {
	t.Helper()
	var n int
	//nolint:gosec // G202: table is a test constant.
	if err := e.store.db.QueryRowContext(e.ctx, "SELECT COALESCE(MAX(last_child), 0) FROM "+table+" WHERE parent_id = ?", parent).Scan(&n); err != nil {
		t.Fatalf("read %s[%s]: %v", table, parent, err)
	}
	return n
}

// assertNext asserts the next generated children of parent, in order.
func (e *childSuffixEnv) assertNext(t *testing.T, parent string, want ...string) {
	t.Helper()
	for _, w := range want {
		got, err := e.store.GetNextChildID(e.ctx, parent)
		if err != nil {
			t.Fatalf("GetNextChildID(%s): %v", parent, err)
		}
		if got != w {
			t.Fatalf("GetNextChildID(%s) = %s, want %s: a used child suffix was re-minted", parent, got, w)
		}
	}
}

func (e *childSuffixEnv) rename(t *testing.T, oldID, newID string) {
	t.Helper()
	issue := &types.Issue{ID: newID, Title: "renamed " + oldID}
	if err := e.store.UpdateIssueID(e.ctx, oldID, newID, issue, "tester"); err != nil {
		t.Fatalf("UpdateIssueID(%s -> %s): %v", oldID, newID, err)
	}
}

// seedChildren creates parent and its children parent.1..parent.n, then drops
// parent's counter: the legacy shape, where the highest live child is the only
// record of the suffixes used.
func (e *childSuffixEnv) seedChildren(t *testing.T, parent string, parentWisp, childWisp bool, n int) {
	t.Helper()
	e.create(t, parent, parentWisp)
	for i := 1; i <= n; i++ {
		e.create(t, parent+"."+string(rune('0'+i)), childWisp)
	}
	e.dropCounter(t, parent)
}

// committedCounter reads parent's child_counters row as of HEAD: the
// reservation must ride the removal's own Dolt commit (for a wisp removal,
// the post-transaction commit of the durable rows it changed).
func (e *childSuffixEnv) committedCounter(t *testing.T, parent string) int {
	t.Helper()
	var n int
	if err := e.store.db.QueryRowContext(e.ctx, "SELECT COALESCE(MAX(last_child), 0) FROM child_counters AS OF 'HEAD' WHERE parent_id = ?", parent).Scan(&n); err != nil {
		t.Fatalf("read committed child_counters[%s]: %v", parent, err)
	}
	return n
}

// counterRows counts parent's counter rows across both planes.
func (e *childSuffixEnv) counterRows(t *testing.T, parent string) int {
	t.Helper()
	var n int
	if err := e.store.db.QueryRowContext(e.ctx, "SELECT (SELECT COUNT(*) FROM child_counters WHERE parent_id = ?) + (SELECT COUNT(*) FROM wisp_child_counters WHERE parent_id = ?)", parent, parent).Scan(&n); err != nil {
		t.Fatalf("count counter rows for %s: %v", parent, err)
	}
	return n
}

// TestChildSuffixReservation pins GH#4750's live-database leg on the
// server-backed store: the create, rename and removal paths preserve a
// parent's child suffixes in its counter, so a counter that was missing or
// lagging cannot let the next generated child re-mint a suffix the parent
// already used.
func TestChildSuffixReservation(t *testing.T) {
	// AC-2: renaming a top-level issue into a dotted child id raises the
	// parent's counter to the new suffix.
	t.Run("RenameIntoAChildIDRaisesTheParentCounter", func(t *testing.T) {
		e := newChildSuffixEnv(t)
		e.create(t, "csr-rn-p", false)
		e.create(t, "csr-rn-x", false)
		e.dropCounter(t, "csr-rn-p")
		e.rename(t, "csr-rn-x", "csr-rn-p.9")
		if got := e.counter(t, "child_counters", "csr-rn-p"); got < 9 {
			t.Fatalf("child_counters[csr-rn-p] = %d after renaming csr-rn-x to csr-rn-p.9, want >= 9", got)
		}
	})

	// AC-3: renaming the only child away leaves its suffix reserved.
	t.Run("RenameAwayKeepsTheVacatedSuffixReserved", func(t *testing.T) {
		e := newChildSuffixEnv(t)
		e.create(t, "csr-ra-p", false)
		e.create(t, "csr-ra-p.5", false)
		e.dropCounter(t, "csr-ra-p")
		e.rename(t, "csr-ra-p.5", "csr-ra-y")
		e.assertNext(t, "csr-ra-p", "csr-ra-p.6")
	})

	// AC-3 on the wisps plane: the vacated suffix is reserved in the wisp
	// parent's own counter table.
	t.Run("RenameAwayFromAWispParentKeepsTheSuffixReserved", func(t *testing.T) {
		e := newChildSuffixEnv(t)
		e.create(t, "csr-rw-p", true)
		e.create(t, "csr-rw-p.5", true)
		e.dropCounter(t, "csr-rw-p")
		e.rename(t, "csr-rw-p.5", "csr-rw-y")
		if got := e.counter(t, "wisp_child_counters", "csr-rw-p"); got < 5 {
			t.Fatalf("wisp_child_counters[csr-rw-p] = %d after renaming csr-rw-p.5 away, want >= 5", got)
		}
		e.assertNext(t, "csr-rw-p", "csr-rw-p.6")
	})

	// AC-4: removing P.3, the highest of P.1-P.3, through each removal entry
	// keeps suffix 3 used, so the next children are P.4 and P.5. A wisp row
	// routes DeleteIssue to deleteWisp and DeleteIssues to deleteWispBatchTx.
	removals := []struct {
		name                  string
		parentWisp, childWisp bool
		remove                func(t *testing.T, e *childSuffixEnv, id string)
	}{
		{"DeleteIssue", false, false, csrDeleteIssue},
		{"DeleteWispOfADurableParent", false, true, csrDeleteIssue},
		{"DeleteWispUnderAWispParent", true, true, csrDeleteIssue},
		{"DeleteIssues", false, false, csrDeleteIssues},
		{"DeleteWispBatchOfADurableParent", false, true, csrDeleteIssues},
		{"DeleteWispBatchUnderAWispParent", true, true, csrDeleteIssues},
		{"TxDeleteIssue", false, false, csrTxDeleteIssue},
		{"TxDeleteWispOfADurableParent", false, true, csrTxDeleteIssue},
	}
	for i, tc := range removals {
		t.Run("RemoveThrough"+tc.name+"KeepsTheSuffixReserved", func(t *testing.T) {
			e := newChildSuffixEnv(t)
			parent := "csr-rm" + string(rune('a'+i)) + "-p"
			e.seedChildren(t, parent, tc.parentWisp, tc.childWisp, 3)
			tc.remove(t, e, parent+".3")
			if !tc.parentWisp {
				if got := e.committedCounter(t, parent); got < 3 {
					t.Fatalf("child_counters[%s] AS OF HEAD = %d after removing %s.3, want >= 3", parent, got, parent)
				}
			}
			e.assertNext(t, parent, parent+".4", parent+".5")
		})
	}

	// AC-4 through DeleteIssuesBySourceRepo: the child leaves with its source
	// repo while the parent, owned by another repo, survives.
	t.Run("RemoveThroughDeleteIssuesBySourceRepoKeepsTheSuffixReserved", func(t *testing.T) {
		e := newChildSuffixEnv(t)
		e.create(t, "csr-sr-p", false)
		e.create(t, "csr-sr-p.1", false)
		e.create(t, "csr-sr-p.2", false)
		other := &types.Issue{ID: "csr-sr-p.3", Title: "child suffix csr-sr-p.3", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, SourceRepo: "csr-other-repo"}
		if err := e.store.CreateIssue(e.ctx, other, "tester"); err != nil {
			t.Fatalf("CreateIssue(csr-sr-p.3): %v", err)
		}
		e.dropCounter(t, "csr-sr-p")
		n, err := e.store.DeleteIssuesBySourceRepo(e.ctx, "csr-other-repo")
		if err != nil || n != 1 {
			t.Fatalf("DeleteIssuesBySourceRepo(csr-other-repo) = %d, %v; want 1, nil", n, err)
		}
		if got := e.committedCounter(t, "csr-sr-p"); got < 3 {
			t.Fatalf("child_counters[csr-sr-p] AS OF HEAD = %d after removing csr-sr-p.3, want >= 3", got)
		}
		e.assertNext(t, "csr-sr-p", "csr-sr-p.4", "csr-sr-p.5")
	})

	// AC-5: the reservation only raises a counter. With the parent's counter
	// at 10, creating P.8, renaming into P.9 and removing P.3 all leave it at
	// 10, and the next child is P.11.
	t.Run("ReservationNeverLowersTheCounter", func(t *testing.T) {
		e := newChildSuffixEnv(t)
		e.seedChildren(t, "csr-nd-p", false, false, 3)
		e.create(t, "csr-nd-x", false)
		if _, err := e.store.db.ExecContext(e.ctx, "REPLACE INTO child_counters (parent_id, last_child) VALUES (?, 10)", "csr-nd-p"); err != nil {
			t.Fatalf("set child_counters[csr-nd-p] = 10: %v", err)
		}
		e.create(t, "csr-nd-p.8", false)
		e.rename(t, "csr-nd-x", "csr-nd-p.9")
		csrDeleteIssue(t, e, "csr-nd-p.3")
		if got := e.counter(t, "child_counters", "csr-nd-p"); got != 10 {
			t.Fatalf("child_counters[csr-nd-p] = %d after create, rename and removal below 10, want 10", got)
		}
		e.assertNext(t, "csr-nd-p", "csr-nd-p.11")
	})

	// A legacy suffix above math.MaxInt32 does not fit the INT counter column
	// (an upsert of it is out of range), and the allocator can never mint it.
	// Renaming into such an id and removing it in a batch with an in-range
	// sibling both succeed, and the sibling's suffix is still reserved.
	t.Run("SuffixBeyondTheCounterColumnIsSkipped", func(t *testing.T) {
		e := newChildSuffixEnv(t)
		e.seedChildren(t, "csr-big-p", false, false, 2)
		e.create(t, "csr-big-x", false)
		e.rename(t, "csr-big-x", "csr-big-p.3000000000")
		if got := e.counter(t, "child_counters", "csr-big-p"); got != 0 {
			t.Fatalf("child_counters[csr-big-p] = %d after renaming into csr-big-p.3000000000, want 0: the suffix does not fit the counter", got)
		}
		if _, err := e.store.DeleteIssues(e.ctx, []string{"csr-big-p.2", "csr-big-p.3000000000"}, false, true, false); err != nil {
			t.Fatalf("DeleteIssues(csr-big-p.2, csr-big-p.3000000000): %v", err)
		}
		if got := e.counter(t, "child_counters", "csr-big-p"); got != 2 {
			t.Fatalf("child_counters[csr-big-p] = %d after removing csr-big-p.2 and csr-big-p.3000000000, want 2", got)
		}
		e.assertNext(t, "csr-big-p", "csr-big-p.3")
	})

	// AC-7: a parent removed together with its children is departing, so the
	// children's suffixes are not reserved under it and no counter row
	// outlives it in either plane.
	cascades := []struct {
		name                  string
		parentWisp, childWisp bool
	}{
		{"DurableParentAndChildren", false, false},
		{"DurableParentAndWispChildren", false, true},
		{"WispParentAndChildren", true, true},
	}
	for i, tc := range cascades {
		t.Run("CascadeDeleteOf"+tc.name+"LeavesNoParentCounter", func(t *testing.T) {
			e := newChildSuffixEnv(t)
			parent := "csr-cd" + string(rune('a'+i)) + "-p"
			e.create(t, parent, tc.parentWisp)
			e.create(t, parent+".1", tc.childWisp)
			e.create(t, parent+".2", tc.childWisp)
			// Link each child to the parent and select all three for the
			// cascade, so the parent and its children depart together.
			// (Selecting the parent alone would not do on every plane: the
			// server routes a wisp parent to deleteWispBatch, which does not
			// expand a cascade.)
			children := []string{parent + ".1", parent + ".2"}
			for _, child := range children {
				if err := e.store.AddDependency(e.ctx, &types.Dependency{IssueID: child, DependsOnID: parent, Type: types.DepParentChild}, "tester"); err != nil {
					t.Fatalf("AddDependency(%s -> %s): %v", child, parent, err)
				}
			}
			res, err := e.store.DeleteIssues(e.ctx, append([]string{parent}, children...), true, true, false)
			if err != nil {
				t.Fatalf("DeleteIssues(%s, cascade): %v", parent, err)
			}
			if res.DeletedCount != 3 {
				t.Fatalf("DeleteIssues(%s, cascade) deleted %d issues, want 3 (the parent and its 2 children)", parent, res.DeletedCount)
			}
			if got := e.counterRows(t, parent); got != 0 {
				t.Fatalf("%d counter rows for %s after a cascade delete of it and its children, want 0", got, parent)
			}
		})
	}
}

func csrDeleteIssue(t *testing.T, e *childSuffixEnv, id string) {
	t.Helper()
	if err := e.store.DeleteIssue(e.ctx, id); err != nil {
		t.Fatalf("DeleteIssue(%s): %v", id, err)
	}
}

func csrDeleteIssues(t *testing.T, e *childSuffixEnv, id string) {
	t.Helper()
	if _, err := e.store.DeleteIssues(e.ctx, []string{id}, false, true, false); err != nil {
		t.Fatalf("DeleteIssues(%s): %v", id, err)
	}
}

func csrTxDeleteIssue(t *testing.T, e *childSuffixEnv, id string) {
	t.Helper()
	if err := e.store.RunInTransaction(e.ctx, "child suffix: delete "+id, func(tx storage.Transaction) error {
		return tx.DeleteIssue(e.ctx, id)
	}); err != nil {
		t.Fatalf("Tx.DeleteIssue(%s): %v", id, err)
	}
}
