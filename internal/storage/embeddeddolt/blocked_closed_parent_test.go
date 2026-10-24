//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// blockedGraph builds rows and edges through the store's own write paths, and
// reads (or, for the stale-data cases, plants) the derived column over a raw
// connection: a stale is_blocked is a value no verb produces.
type blockedGraph struct {
	t  *testing.T
	te *testEnv
}

func (g blockedGraph) create(ctx context.Context, id string, wisp bool) {
	g.t.Helper()
	iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: wisp}
	if err := g.te.store.CreateIssue(ctx, iss, "tester"); err != nil {
		g.t.Fatalf("create %s: %v", id, err)
	}
}

func (g blockedGraph) dep(ctx context.Context, src, tgt string, typ types.DependencyType) {
	g.t.Helper()
	if err := g.te.store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ}, "tester"); err != nil {
		g.t.Fatalf("add %s dep %s->%s: %v", typ, src, tgt, err)
	}
}

func (g blockedGraph) close(ctx context.Context, id string) {
	g.t.Helper()
	if err := g.te.store.CloseIssue(ctx, id, "done", "tester", ""); err != nil {
		g.t.Fatalf("close %s: %v", id, err)
	}
}

var planeTable = map[bool]string{false: "issues", true: "wisps"}

func (g blockedGraph) isBlocked(ctx context.Context, id string, wisp bool) bool {
	g.t.Helper()
	var v int
	g.te.queryScalar(g.t, ctx, "SELECT is_blocked FROM "+planeTable[wisp]+" WHERE id = ?", []any{id}, &v)
	return v == 1
}

// inTx runs fn in one committed transaction on a raw connection, so a case can
// drive the issueops core directly against the real engine.
func (g blockedGraph) inTx(ctx context.Context, fn func(tx *sql.Tx)) {
	g.t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, g.te.dataDir, g.te.database, "main")
	if err != nil {
		g.t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		g.t.Fatalf("begin: %v", err)
	}
	fn(tx)
	if err := tx.Commit(); err != nil {
		g.t.Fatalf("commit: %v", err)
	}
}

func (g blockedGraph) staleCount(ctx context.Context) int64 {
	g.t.Helper()
	var n int64
	g.inTx(ctx, func(tx *sql.Tx) {
		var err error
		if n, err = issueops.CountIsBlockedInconsistenciesInTx(ctx, tx); err != nil {
			g.t.Fatalf("count: %v", err)
		}
	})
	return n
}

// TestClosedParentWithStaleFlagDoesNotPropagate pins the closed/pinned guard on
// the parent-child legs of the should-be-blocked union, in every combination of
// parent and child plane. A closed or pinned row is never blocked
// (issueops.BlockedStateInvariant), but the legs read the parent's STORED flag,
// which the merge clause admits may be stale. The cases plant that stale flag
// and drive the union's three consumers: the scoped recompute, the doctor
// count and the full repair.
func TestClosedParentWithStaleFlagDoesNotPropagate(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	for _, tc := range []struct {
		name                  string
		status                types.Status
		parentWisp, childWisp bool
	}{
		{"closed issue parent, issue child", types.StatusClosed, false, false},
		{"closed issue parent, wisp child", types.StatusClosed, false, true},
		{"closed wisp parent, issue child", types.StatusClosed, true, false},
		{"closed wisp parent, wisp child", types.StatusClosed, true, true},
		{"pinned issue parent, issue child", types.StatusPinned, false, false},
		{"pinned wisp parent, wisp child", types.StatusPinned, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te := newTestEnv(t, "cp")
			ctx := t.Context()
			g := blockedGraph{t, te}

			g.create(ctx, "cp-parent", tc.parentWisp)
			g.create(ctx, "cp-child", tc.childWisp)
			g.dep(ctx, "cp-child", "cp-parent", types.DepParentChild)
			if tc.status == types.StatusClosed {
				g.close(ctx, "cp-parent")
			} else if err := te.store.UpdateIssue(ctx, "cp-parent", map[string]interface{}{"status": string(types.StatusPinned)}, "tester"); err != nil {
				t.Fatalf("pin parent: %v", err)
			}
			te.exec(t, ctx, "UPDATE "+planeTable[tc.parentWisp]+" SET is_blocked = 1 WHERE id = ?", "cp-parent")

			var childIssues, childWisps []string
			if tc.childWisp {
				childWisps = []string{"cp-child"}
			} else {
				childIssues = []string{"cp-child"}
			}
			// The stale parent is outside the batch, so only the union's own
			// guard can keep the child clear.
			g.inTx(ctx, func(tx *sql.Tx) {
				if err := issueops.RecomputeIsBlockedInTx(ctx, tx, childIssues, childWisps); err != nil {
					t.Fatalf("scoped recompute: %v", err)
				}
			})
			if g.isBlocked(ctx, "cp-child", tc.childWisp) {
				t.Fatalf("open child was marked blocked by a %s parent's stale is_blocked = 1", tc.status)
			}
			// The parent's own flag is the one stale row; the settled child is
			// not mark-eligible.
			if n := g.staleCount(ctx); n != 1 {
				t.Fatalf("stale count = %d, want 1 (the parent's own flag only)", n)
			}

			// A child already carrying the inherited flag is unmark-eligible in
			// the same pass as its parent, not one pass later.
			te.exec(t, ctx, "UPDATE "+planeTable[tc.childWisp]+" SET is_blocked = 1 WHERE id = ?", "cp-child")
			if n := g.staleCount(ctx); n != 2 {
				t.Fatalf("stale count = %d, want 2 (parent and child)", n)
			}
			g.inTx(ctx, func(tx *sql.Tx) {
				fixed, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx)
				if err != nil {
					t.Fatalf("full repair: %v", err)
				}
				if fixed != 2 {
					t.Fatalf("full repair corrected %d rows, want 2", fixed)
				}
			})
			if g.isBlocked(ctx, "cp-parent", tc.parentWisp) || g.isBlocked(ctx, "cp-child", tc.childWisp) {
				t.Fatal("full repair left a stale flag behind")
			}
		})
	}
}

// TestClosedParentWithStaleFlagDoesNotUnexplainAGate pins the same guard on the
// other place a parent's flag crosses a parent-child edge: the walk that
// decides whether a close gate is blocked FROM ABOVE (gastownhall/beads#6506).
//
//	ug-x  <--blocks--  ug-c ── ug-e ── ug-k        ug-e blocks on ug-k
//
// ug-e is a close gate over its own child, so it darkens ug-k only while
// something above it is exogenously blocked, which ug-c is until it closes or
// is pinned. Move ug-c's status the way a merge does (the flag stays 1) and
// recompute ug-k: a closed or pinned ug-c is not blocked, so ug-k must come
// clear. ug-c keeps a blocks edge onto the open ug-x, so it carries a reason
// row of its own and is not a transparent no-reason parent either.
func TestClosedParentWithStaleFlagDoesNotUnexplainAGate(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	for _, status := range []types.Status{types.StatusClosed, types.StatusPinned} {
		t.Run(string(status), func(t *testing.T) {
			te := newTestEnv(t, "ug")
			ctx := t.Context()
			g := blockedGraph{t, te}

			for _, id := range []string{"ug-x", "ug-c", "ug-e", "ug-k"} {
				g.create(ctx, id, false)
			}
			g.dep(ctx, "ug-c", "ug-x", types.DepBlocks)
			g.dep(ctx, "ug-e", "ug-c", types.DepParentChild)
			g.dep(ctx, "ug-e", "ug-k", types.DepBlocks)
			// The hierarchy edge under the gate closes a loop, which the per-edge
			// cycle check refuses; that is how the idiom arrives through import or merge.
			if err := te.store.RunInTransaction(ctx, "gate", func(tx storage.Transaction) error {
				return tx.AddDependencyWithOptions(ctx, &types.Dependency{IssueID: "ug-k", DependsOnID: "ug-e", Type: types.DepParentChild},
					"tester", storage.DependencyAddOptions{SkipCycleCheck: true})
			}); err != nil {
				t.Fatalf("add gate edge: %v", err)
			}
			if !g.isBlocked(ctx, "ug-k", false) {
				t.Fatal("precondition: ug-k should be dark while ug-c is exogenously blocked")
			}

			if status == types.StatusClosed {
				te.exec(t, ctx, "UPDATE issues SET status = 'closed', closed_at = NOW() WHERE id = ?", "ug-c")
			} else {
				te.exec(t, ctx, "UPDATE issues SET status = 'pinned' WHERE id = ?", "ug-c")
			}
			g.inTx(ctx, func(tx *sql.Tx) {
				if err := issueops.RecomputeIsBlockedInTx(ctx, tx, []string{"ug-k"}, nil); err != nil {
					t.Fatalf("scoped recompute: %v", err)
				}
			})
			if g.isBlocked(ctx, "ug-k", false) {
				t.Fatalf("ug-k stayed dark: the %s ug-c's stale flag still read as a blocked parent above the gate", status)
			}
			if !g.isBlocked(ctx, "ug-e", false) {
				t.Fatal("ug-e must stay blocked on its own open child")
			}
			if n := g.staleCount(ctx); n != 1 {
				t.Fatalf("stale count = %d, want 1 (the %s ug-c's own flag only)", n, status)
			}
		})
	}
}
