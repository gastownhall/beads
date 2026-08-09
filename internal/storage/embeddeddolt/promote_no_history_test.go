//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestPromoteClearsNoHistory is the promote half of bd-r9uce: a promoted wisp
// is fully durable, so PromoteFromEphemeralInTx must clear BOTH wisp-plane
// flags, not just Ephemeral. A no-history wisp (Ephemeral=false,
// NoHistory=true) promoted with NoHistory intact lands in the issues table
// still flag-marked as wisp-plane state, and flag-based plane inference —
// most damagingly import's table routing — silently re-planes it back into
// the wisps table, dropping its relations on the way.
func TestPromoteClearsNoHistory(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	ctx := t.Context()

	t.Run("no_history wisp", func(t *testing.T) {
		te := newTestEnv(t, "pnh")
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: "pnh-w", Title: "no-history wisp", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, NoHistory: true,
			Labels: []string{"keepme"},
		}, "tester"); err != nil {
			t.Fatalf("create no-history wisp: %v", err)
		}
		te.assertRowExists(t, ctx, "wisps", "pnh-w")

		if err := te.store.PromoteFromEphemeral(ctx, "pnh-w", "tester"); err != nil {
			t.Fatalf("promote no-history wisp: %v", err)
		}

		te.assertRowExists(t, ctx, "issues", "pnh-w")
		te.assertRowNotExists(t, ctx, "wisps", "pnh-w")
		var noHistory, ephemeral int
		te.queryScalar(t, ctx, "SELECT no_history, ephemeral FROM issues WHERE id = ?",
			[]any{"pnh-w"}, &noHistory, &ephemeral)
		if noHistory != 0 {
			t.Errorf("promoted row no_history = %d, want 0 (promote must clear NoHistory too)", noHistory)
		}
		if ephemeral != 0 {
			t.Errorf("promoted row ephemeral = %d, want 0", ephemeral)
		}
		got, err := te.store.GetIssue(ctx, "pnh-w")
		if err != nil {
			t.Fatalf("get promoted issue: %v", err)
		}
		if got.NoHistory || got.Ephemeral {
			t.Errorf("promoted issue flags = {Ephemeral:%v NoHistory:%v}, want both false",
				got.Ephemeral, got.NoHistory)
		}
	})

	// Non-regression: the ephemeral-wisp happy path keeps its shape.
	t.Run("ephemeral wisp", func(t *testing.T) {
		te := newTestEnv(t, "pnh")
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: "pnh-e", Title: "ephemeral wisp", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
		}, "tester"); err != nil {
			t.Fatalf("create ephemeral wisp: %v", err)
		}
		if err := te.store.PromoteFromEphemeral(ctx, "pnh-e", "tester"); err != nil {
			t.Fatalf("promote ephemeral wisp: %v", err)
		}
		got, err := te.store.GetIssue(ctx, "pnh-e")
		if err != nil {
			t.Fatalf("get promoted issue: %v", err)
		}
		if got.NoHistory || got.Ephemeral {
			t.Errorf("promoted issue flags = {Ephemeral:%v NoHistory:%v}, want both false",
				got.Ephemeral, got.NoHistory)
		}
	})
}

// TestPartitionWispIDs pins the store-level membership partition export's
// plane-marker stamping relies on (bd-r9uce): classification must follow
// TABLE membership, never row flags — after promotion the row is durable
// whatever flags it carries.
func TestPartitionWispIDs(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	ctx := t.Context()
	te := newTestEnv(t, "pw")
	seed := []*types.Issue{
		{ID: "pw-dur", Title: "durable", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "pw-eph", Title: "ephemeral wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		{ID: "pw-noh", Title: "no-history wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, NoHistory: true},
	}
	for _, issue := range seed {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	// The promoted no-history shape: a durable row that (as wild data) still
	// carries no_history=1. Flags say wisp; the table says durable.
	te.exec(t, ctx, "UPDATE issues SET no_history = 1 WHERE id = ?", "pw-dur")

	wispIDs, permIDs, err := te.store.PartitionWispIDs(ctx,
		[]string{"pw-dur", "pw-eph", "pw-noh", "pw-ghost"})
	if err != nil {
		t.Fatalf("PartitionWispIDs: %v", err)
	}
	wantWisp := []string{"pw-eph", "pw-noh"}
	wantPerm := []string{"pw-dur", "pw-ghost"}
	if len(wispIDs) != len(wantWisp) || wispIDs[0] != wantWisp[0] || wispIDs[1] != wantWisp[1] {
		t.Errorf("wispIDs = %v, want %v", wispIDs, wantWisp)
	}
	if len(permIDs) != len(wantPerm) || permIDs[0] != wantPerm[0] || permIDs[1] != wantPerm[1] {
		t.Errorf("permIDs = %v, want %v", permIDs, wantPerm)
	}
}
