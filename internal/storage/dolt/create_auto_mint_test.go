package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// autoMintIssue is a create that names no id.
func autoMintIssue(title string) *types.Issue {
	return &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
}

// requireIssueTitle fails t unless id reads back with title.
func requireIssueTitle(t *testing.T, ctx context.Context, store *DoltStore, id, title string) {
	t.Helper()
	got, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", id, err)
	}
	if got.Title != title {
		t.Fatalf("%s title = %q, want %q", id, got.Title, title)
	}
}

// TestCreateIssue_CounterModeJumpsPastExplicitIDs pins the counter-lag leg of
// bd-t4h0n7 (PR #6754) on the server store: explicit test-2 (issues) and
// test-100 (wisps) created after the counter's last bump hold values the
// counter would hand out next. The next auto-minted create must be test-101,
// leave both explicit rows untouched, and the one after it test-102. Before the
// fix the mint took test-2 and upserted over the explicit row.
func TestCreateIssue_CounterModeJumpsPastExplicitIDs(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("SetConfig(issue_id_mode): %v", err)
	}
	first := autoMintIssue("auto one")
	if err := store.CreateIssue(ctx, first, "tester"); err != nil {
		t.Fatalf("CreateIssue(auto one): %v", err)
	}
	if first.ID != "test-1" {
		t.Fatalf("first auto id = %q, want test-1", first.ID)
	}
	explicitTwo := &types.Issue{ID: "test-2", Title: "explicit two", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, explicitTwo, "tester"); err != nil {
		t.Fatalf("CreateIssue(explicit test-2): %v", err)
	}
	explicitHundred := &types.Issue{ID: "test-100", Title: "explicit hundred", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, explicitHundred, "tester"); err != nil {
		t.Fatalf("CreateIssue(explicit wisp test-100): %v", err)
	}

	minted := autoMintIssue("auto two")
	if err := store.CreateIssue(ctx, minted, "tester"); err != nil {
		t.Fatalf("CreateIssue(auto two) = %v, want test-101", err)
	}
	if minted.ID != "test-101" {
		t.Errorf("auto-minted id = %q, want test-101 (one past the held test-100)", minted.ID)
	}
	requireIssueTitle(t, ctx, store, "test-2", "explicit two")
	requireIssueTitle(t, ctx, store, "test-100", "explicit hundred")

	batch := []*types.Issue{autoMintIssue("auto three")}
	if err := store.CreateIssues(ctx, batch, "tester"); err != nil {
		t.Fatalf("CreateIssues(auto three) = %v, want test-102", err)
	}
	if batch[0].ID != "test-102" {
		t.Errorf("batch auto-minted id = %q, want test-102", batch[0].ID)
	}
}

// requireOneCreatedEvent fails t unless id has exactly one created event.
func requireOneCreatedEvent(t *testing.T, ctx context.Context, store *DoltStore, id string) {
	t.Helper()
	events, err := store.GetEvents(ctx, id, 0)
	if err != nil {
		t.Fatalf("GetEvents(%s): %v", id, err)
	}
	created := 0
	for _, event := range events {
		if event.EventType == types.EventCreated {
			created++
		}
	}
	if created != 1 {
		t.Errorf("%s has %d created events, want 1", id, created)
	}
}

// commitWinnerOnFirstAttempt sets store's create hook so the FIRST attempt of
// the create that carries loser commits winner(mintedID) through the store's
// other connection before that attempt's own SQL commit: a concurrent writer
// takes the id the attempt minted while the attempt is still open. It returns
// the id the first attempt minted and the number of attempts the loser made.
func commitWinnerOnFirstAttempt(t *testing.T, store *DoltStore, loser *types.Issue, winner func(ctx context.Context, mintedID string) error) (firstID *string, attempts *int) {
	t.Helper()
	firstID, attempts = new(string), new(int)
	store.createAttemptHook = func(ctx context.Context, issues []*types.Issue) error {
		carriesLoser := false
		for _, issue := range issues {
			carriesLoser = carriesLoser || issue == loser
		}
		if !carriesLoser {
			return nil
		}
		*attempts++
		if *attempts > 1 {
			return nil
		}
		*firstID = loser.ID
		if err := winner(ctx, loser.ID); err != nil {
			t.Errorf("winner create under %s: %v", loser.ID, err)
		}
		return nil
	}
	t.Cleanup(func() { store.createAttemptHook = nil })
	return firstID, attempts
}

// TestCreateIssueReplayRemintsAfterSameIDCommit pins the replay leg of
// bd-t4h0n7 (PR #6754) on the server single create: a concurrent writer
// commits an explicit issue under the id an open auto-minted create just
// inserted, so the create loses at commit and withRetryTx replays it. The
// replay must mint a fresh id instead of reusing the lost one as an explicit
// id (which upserted over the winner), and each row keeps its own content and
// one created event.
func TestCreateIssueReplayRemintsAfterSameIDCommit(t *testing.T) {
	for _, mode := range []string{"hash", "counter"} {
		t.Run(mode, func(t *testing.T) {
			store, cleanup := setupConcurrentTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()
			if err := store.SetConfig(ctx, "issue_id_mode", mode); err != nil {
				t.Fatalf("SetConfig(issue_id_mode): %v", err)
			}

			loser := autoMintIssue("loser")
			firstID, attempts := commitWinnerOnFirstAttempt(t, store, loser, func(_ context.Context, mintedID string) error {
				winner := &types.Issue{ID: mintedID, Title: "winner", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug}
				return store.CreateIssue(ctx, winner, "rival")
			})
			if err := store.CreateIssue(ctx, loser, "tester"); err != nil {
				t.Fatalf("CreateIssue(loser): %v", err)
			}
			if *attempts < 2 {
				t.Fatalf("loser made %d attempt(s); the winner's commit must force a replay", *attempts)
			}
			if loser.ID == "" || loser.ID == *firstID {
				t.Fatalf("loser id = %q after replay, want a fresh id distinct from the lost %q", loser.ID, *firstID)
			}
			requireIssueTitle(t, ctx, store, *firstID, "winner")
			requireIssueTitle(t, ctx, store, loser.ID, "loser")
			requireOneCreatedEvent(t, ctx, store, *firstID)
			requireOneCreatedEvent(t, ctx, store, loser.ID)
		})
	}
}
