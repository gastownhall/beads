//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// childSuffixEnv drives the GH#4750 child-suffix cases on one embedded store:
// a parent whose counter is missing (the legacy shape: a database written
// before the singular create reconciled, or a child created through a door
// that did not advance it), and the operation under test, after which the
// next generated child must not re-mint a suffix the parent already used.
type childSuffixEnv struct {
	te  *testEnv
	ctx context.Context
}

func newChildSuffixEnv(t *testing.T, prefix string) *childSuffixEnv {
	t.Helper()
	return &childSuffixEnv{te: newTestEnv(t, prefix), ctx: t.Context()}
}

func (e *childSuffixEnv) create(t *testing.T, id string, wisp bool) {
	t.Helper()
	issue := &types.Issue{ID: id, Title: "child suffix " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: wisp}
	if err := e.te.store.CreateIssue(e.ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue(%s): %v", id, err)
	}
}

// dropCounter removes parent's counter row from both planes, leaving the
// legacy state the reservation must survive.
func (e *childSuffixEnv) dropCounter(t *testing.T, parent string) {
	t.Helper()
	e.te.exec(t, e.ctx, "DELETE FROM child_counters WHERE parent_id = ?", parent)
	e.te.exec(t, e.ctx, "DELETE FROM wisp_child_counters WHERE parent_id = ?", parent)
}

func (e *childSuffixEnv) counter(t *testing.T, table, parent string) int {
	t.Helper()
	var n int
	e.te.queryScalar(t, e.ctx, "SELECT COALESCE(MAX(last_child), 0) FROM "+table+" WHERE parent_id = ?", []any{parent}, &n)
	return n
}

// assertNext asserts the next generated children of parent, in order.
func (e *childSuffixEnv) assertNext(t *testing.T, parent string, want ...string) {
	t.Helper()
	for _, w := range want {
		got, err := e.te.store.GetNextChildID(e.ctx, parent)
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
	if err := e.te.store.UpdateIssueID(e.ctx, oldID, newID, issue, "tester"); err != nil {
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

// committedCounter reads parent's child_counters row as of HEAD: a removal
// through RunInTransaction must carry the reservation in its own Dolt commit.
// (The store's direct writes commit SQL only; they reach HEAD with the next
// Dolt commit, so the working set is their contract.)
func (e *childSuffixEnv) committedCounter(t *testing.T, parent string) int {
	t.Helper()
	var n int
	e.te.queryScalar(t, e.ctx, "SELECT COALESCE(MAX(last_child), 0) FROM child_counters AS OF 'HEAD' WHERE parent_id = ?", []any{parent}, &n)
	return n
}

// counterRows counts parent's counter rows across both planes.
func (e *childSuffixEnv) counterRows(t *testing.T, parent string) int {
	t.Helper()
	var n, w int
	e.te.queryScalar(t, e.ctx, "SELECT COUNT(*) FROM child_counters WHERE parent_id = ?", []any{parent}, &n)
	e.te.queryScalar(t, e.ctx, "SELECT COUNT(*) FROM wisp_child_counters WHERE parent_id = ?", []any{parent}, &w)
	return n + w
}

// TestChildSuffixReservation pins GH#4750's live-database leg on the embedded
// store: the create, rename and removal paths preserve a parent's child
// suffixes in its counter, so a counter that was missing or lagging cannot let
// the next generated child re-mint a suffix the parent already used.
func TestChildSuffixReservation(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	// AC-2: renaming a top-level issue into a dotted child id raises the
	// parent's counter to the new suffix.
	t.Run("RenameIntoAChildIDRaisesTheParentCounter", func(t *testing.T) {
		e := newChildSuffixEnv(t, "rn")
		e.create(t, "rn-p", false)
		e.create(t, "rn-x", false)
		e.dropCounter(t, "rn-p")
		e.rename(t, "rn-x", "rn-p.9")
		if got := e.counter(t, "child_counters", "rn-p"); got < 9 {
			t.Fatalf("child_counters[rn-p] = %d after renaming rn-x to rn-p.9, want >= 9", got)
		}
	})

	// AC-3: renaming the only child away leaves its suffix reserved.
	t.Run("RenameAwayKeepsTheVacatedSuffixReserved", func(t *testing.T) {
		e := newChildSuffixEnv(t, "ra")
		e.create(t, "ra-p", false)
		e.create(t, "ra-p.5", false)
		e.dropCounter(t, "ra-p")
		e.rename(t, "ra-p.5", "ra-y")
		e.assertNext(t, "ra-p", "ra-p.6")
	})

	// AC-3 on the wisps plane: the vacated suffix is reserved in the wisp
	// parent's own counter table.
	t.Run("RenameAwayFromAWispParentKeepsTheSuffixReserved", func(t *testing.T) {
		e := newChildSuffixEnv(t, "rw")
		e.create(t, "rw-p", true)
		e.create(t, "rw-p.5", true)
		e.dropCounter(t, "rw-p")
		e.rename(t, "rw-p.5", "rw-y")
		if got := e.counter(t, "wisp_child_counters", "rw-p"); got < 5 {
			t.Fatalf("wisp_child_counters[rw-p] = %d after renaming rw-p.5 away, want >= 5", got)
		}
		e.assertNext(t, "rw-p", "rw-p.6")
	})

	// AC-4: removing P.3, the highest of P.1-P.3, through each removal entry
	// keeps suffix 3 used, so the next children are P.4 and P.5.
	removals := []struct {
		name                  string
		parentWisp, childWisp bool
		remove                func(t *testing.T, e *childSuffixEnv, id string)
		doltCommits           bool
	}{
		{"DeleteIssue", false, false, removeByDeleteIssue, false},
		{"DeleteIssueOfAWispChildOfADurableParent", false, true, removeByDeleteIssue, false},
		{"DeleteIssueUnderAWispParent", true, true, removeByDeleteIssue, false},
		{"DeleteIssues", false, false, removeByDeleteIssues, false},
		{"DeleteIssuesOfAWispChildOfADurableParent", false, true, removeByDeleteIssues, false},
		{"TxDeleteIssue", false, false, removeByTxDeleteIssue, true},
		{"TxDeleteIssueOfAWispChildOfADurableParent", false, true, removeByTxDeleteIssue, true},
	}
	for i, tc := range removals {
		t.Run("RemoveThrough"+tc.name+"KeepsTheSuffixReserved", func(t *testing.T) {
			prefix := "rm" + string(rune('a'+i))
			e := newChildSuffixEnv(t, prefix)
			parent := prefix + "-p"
			e.seedChildren(t, parent, tc.parentWisp, tc.childWisp, 3)
			tc.remove(t, e, parent+".3")
			if tc.doltCommits && !tc.parentWisp {
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
		e := newChildSuffixEnv(t, "sr")
		e.create(t, "sr-p", false)
		e.create(t, "sr-p.1", false)
		e.create(t, "sr-p.2", false)
		other := &types.Issue{ID: "sr-p.3", Title: "child suffix sr-p.3", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, SourceRepo: "other-repo"}
		if err := e.te.store.CreateIssue(e.ctx, other, "tester"); err != nil {
			t.Fatalf("CreateIssue(sr-p.3): %v", err)
		}
		e.dropCounter(t, "sr-p")
		n, err := e.te.store.DeleteIssuesBySourceRepo(e.ctx, "other-repo")
		if err != nil || n != 1 {
			t.Fatalf("DeleteIssuesBySourceRepo(other-repo) = %d, %v; want 1, nil", n, err)
		}
		if got := e.counter(t, "child_counters", "sr-p"); got < 3 {
			t.Fatalf("child_counters[sr-p] = %d after removing sr-p.3, want >= 3", got)
		}
		e.assertNext(t, "sr-p", "sr-p.4", "sr-p.5")
	})

	// AC-5: the reservation only raises a counter. With the parent's counter
	// at 10, creating P.8, renaming into P.9 and removing P.3 all leave it at
	// 10, and the next child is P.11.
	t.Run("ReservationNeverLowersTheCounter", func(t *testing.T) {
		e := newChildSuffixEnv(t, "nd")
		e.seedChildren(t, "nd-p", false, false, 3)
		e.create(t, "nd-x", false)
		e.te.exec(t, e.ctx, "REPLACE INTO child_counters (parent_id, last_child) VALUES (?, 10)", "nd-p")
		e.create(t, "nd-p.8", false)
		e.rename(t, "nd-x", "nd-p.9")
		removeByDeleteIssue(t, e, "nd-p.3")
		if got := e.counter(t, "child_counters", "nd-p"); got != 10 {
			t.Fatalf("child_counters[nd-p] = %d after create, rename and removal below 10, want 10", got)
		}
		e.assertNext(t, "nd-p", "nd-p.11")
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
			prefix := "cd" + string(rune('a'+i))
			e := newChildSuffixEnv(t, prefix)
			parent := prefix + "-p"
			e.create(t, parent, tc.parentWisp)
			e.create(t, parent+".1", tc.childWisp)
			e.create(t, parent+".2", tc.childWisp)
			if _, err := e.te.store.DeleteIssues(e.ctx, []string{parent}, true, true, false); err != nil {
				t.Fatalf("DeleteIssues(%s, cascade): %v", parent, err)
			}
			if got := e.counterRows(t, parent); got != 0 {
				t.Fatalf("%d counter rows for %s after a cascade delete of it and its children, want 0", got, parent)
			}
		})
	}
}

func removeByDeleteIssue(t *testing.T, e *childSuffixEnv, id string) {
	t.Helper()
	if err := e.te.store.DeleteIssue(e.ctx, id); err != nil {
		t.Fatalf("DeleteIssue(%s): %v", id, err)
	}
}

func removeByDeleteIssues(t *testing.T, e *childSuffixEnv, id string) {
	t.Helper()
	if _, err := e.te.store.DeleteIssues(e.ctx, []string{id}, false, true, false); err != nil {
		t.Fatalf("DeleteIssues(%s): %v", id, err)
	}
}

func removeByTxDeleteIssue(t *testing.T, e *childSuffixEnv, id string) {
	t.Helper()
	if err := e.te.store.RunInTransaction(e.ctx, "child suffix: delete "+id, func(tx storage.Transaction) error {
		return tx.DeleteIssue(e.ctx, id)
	}); err != nil {
		t.Fatalf("Tx.DeleteIssue(%s): %v", id, err)
	}
}
