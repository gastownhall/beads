package uow

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestCreateExplicitChildIDAdvancesTheParentCounter pins the unit-of-work half
// of GH#4750: creating a child with an explicit hierarchical ID through the
// domain insert door must advance the parent's child counter, in the parent's
// storage plane, whichever insert branch serves the create (strict CreateOnly
// or upsert) and whichever plane the child row itself lands in. On the base
// the insert writes no counter row, so last_child stays behind the live
// suffix high-water mark and the next --parent create recycles lower
// suffixes (GH#4750).
func TestCreateExplicitChildIDAdvancesTheParentCounter(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "ccr")
	kit := newUOWRoleFixtureKit(provider, "ccr")

	// childCounter reads last_child through the aggregate form, so an absent
	// row — the base's state for every case below — reads as 0 instead of
	// failing the query.
	childCounter := func(table, parent string) int {
		var rows, last int
		err := kit.QueryScalar(ctx, fmt.Sprintf(
			"SELECT COUNT(*), COALESCE(MAX(last_child), 0) FROM %s WHERE parent_id = ?", table),
			[]any{parent}, &rows, &last)
		if err != nil {
			t.Fatalf("reading %s for %s: %v", table, parent, err)
		}
		if rows > 1 {
			t.Fatalf("%s holds %d rows for %s, want at most 1", table, rows, parent)
		}
		return last
	}
	seed := func(id string, wisp bool) *types.Issue {
		return &types.Issue{ID: id, Title: "t", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: wisp}
	}

	t.Run("StrictCreateOnlyChild", func(t *testing.T) {
		if err := kit.CreateIssue(ctx, seed("ccr-d", false), "actor"); err != nil {
			t.Fatalf("seed parent ccr-d: %v", err)
		}
		if err := kit.CreateIssue(ctx, seed("ccr-d.8", false), "actor"); err != nil {
			t.Fatalf("create explicit child ccr-d.8: %v", err)
		}
		if got := childCounter("child_counters", "ccr-d"); got < 8 {
			t.Fatalf("child_counters.last_child for ccr-d = %d, want >= 8 after creating ccr-d.8", got)
		}
	})

	t.Run("UpsertChild", func(t *testing.T) {
		if err := kit.CreateIssue(ctx, seed("ccr-u", false), "actor"); err != nil {
			t.Fatalf("seed parent ccr-u: %v", err)
		}
		// The upsert door: the domain create WITHOUT CreateOnly, the insert
		// domain/db's Insert serves with ON DUPLICATE KEY UPDATE — the shape
		// the proxied quick/todo/gate servers and batch creates use.
		err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			params := domain.CreateIssueParams{Issue: seed("ccr-u.9", false), ExplicitID: "ccr-u.9"}
			_, err := uw.IssueUseCase().CreateIssue(ctx, params, "actor")
			return "create explicit child ccr-u.9", err
		})
		if err != nil {
			t.Fatalf("upsert explicit child ccr-u.9: %v", err)
		}
		if got := childCounter("child_counters", "ccr-u"); got < 9 {
			t.Fatalf("child_counters.last_child for ccr-u = %d, want >= 9 after creating ccr-u.9", got)
		}
	})

	t.Run("WispParentWispChild", func(t *testing.T) {
		if err := kit.CreateWisp(ctx, seed("ccr-w", true), "actor"); err != nil {
			t.Fatalf("seed wisp parent ccr-w: %v", err)
		}
		if err := kit.CreateWisp(ctx, seed("ccr-w.8", true), "actor"); err != nil {
			t.Fatalf("create explicit wisp child ccr-w.8: %v", err)
		}
		if got := childCounter("wisp_child_counters", "ccr-w"); got < 8 {
			t.Fatalf("wisp_child_counters.last_child for ccr-w = %d, want >= 8 after creating ccr-w.8", got)
		}
	})

	// The counter is routed by the PARENT's plane, not the child's: a wisp
	// child of a durable parent advances the durable counter, and leaves the
	// wisp counter untouched.
	t.Run("ChildPlaneDiffersFromParent", func(t *testing.T) {
		if err := kit.CreateIssue(ctx, seed("ccr-m", false), "actor"); err != nil {
			t.Fatalf("seed durable parent ccr-m: %v", err)
		}
		if err := kit.CreateWisp(ctx, seed("ccr-m.5", true), "actor"); err != nil {
			t.Fatalf("create explicit wisp child ccr-m.5: %v", err)
		}
		if got := childCounter("child_counters", "ccr-m"); got < 5 {
			t.Fatalf("child_counters.last_child for ccr-m = %d, want >= 5 after creating ccr-m.5", got)
		}
		if got := childCounter("wisp_child_counters", "ccr-m"); got != 0 {
			t.Fatalf("wisp_child_counters.last_child for ccr-m = %d, want 0: the counter follows the parent's plane", got)
		}
	})
}
