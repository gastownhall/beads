package dolt

import (
	"context"
	"testing"

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
}
