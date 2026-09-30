package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCreateIssueInfersEphemeralFromWispType pins the mint-side half of the
// typed-wisp tier fix on this store's own routing: a wisp_type is a claim of
// ephemerality, so a create that carries one without the flag still lands in
// the wisps plane marked ephemeral, instead of minting the flag-less
// issues-plane shape the sweeper conformance case
// (RunSweeperTreatsALegacyTypedWispAsEphemeralTier) has to manufacture by
// hand. Verified red with the inference reverted.
func TestCreateIssueInfersEphemeralFromWispType(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "twp-minted-1",
		Title:     "typed wisp minted without the flag",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		WispType:  types.WispTypePatrol,
	}
	if err := store.CreateIssue(ctx, issue, "typed-wisp-seed"); err != nil {
		t.Fatalf("creating typed wisp: %v", err)
	}
	if !issue.Ephemeral {
		t.Errorf("issue.Ephemeral = false after create, want true — wisp_type implies the ephemeral tier")
	}

	for _, tc := range []struct {
		table string
		want  int
	}{{"wisps", 1}, {"issues", 0}} {
		var got int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+tc.table+" WHERE id = ?", issue.ID).Scan(&got); err != nil {
			t.Fatalf("counting %s rows: %v", tc.table, err)
		}
		if got != tc.want {
			t.Errorf("%s rows for %s = %d, want %d", tc.table, issue.ID, got, tc.want)
		}
	}

	var ephemeral int
	if err := store.db.QueryRowContext(ctx,
		"SELECT ephemeral FROM wisps WHERE id = ?", issue.ID).Scan(&ephemeral); err != nil {
		t.Fatalf("reading minted wisp row: %v", err)
	}
	if ephemeral != 1 {
		t.Errorf("wisps.ephemeral = %d, want 1", ephemeral)
	}
}
