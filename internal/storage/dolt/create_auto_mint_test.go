package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
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

// requireOnlyComment fails t unless id has exactly one comment, with text, and
// returns its id.
func requireOnlyComment(t *testing.T, ctx context.Context, store *DoltStore, id, text string) string {
	t.Helper()
	comments, err := store.GetIssueComments(ctx, id)
	if err != nil {
		t.Fatalf("GetIssueComments(%s): %v", id, err)
	}
	if len(comments) != 1 || comments[0].Text != text {
		got := make([]string, len(comments))
		for i, c := range comments {
			got[i] = c.Text
		}
		t.Fatalf("%s comments = %q, want only %q", id, got, text)
	}
	return comments[0].ID
}

// requireOnlyEdgeTo fails t unless id's only dependency targets dependsOn.
func requireOnlyEdgeTo(t *testing.T, ctx context.Context, store *DoltStore, id, dependsOn string) {
	t.Helper()
	deps, err := store.GetDependencyRecords(ctx, id)
	if err != nil {
		t.Fatalf("GetDependencyRecords(%s): %v", id, err)
	}
	if len(deps) != 1 || deps[0].IssueID != id || deps[0].DependsOnID != dependsOn {
		got := make([]string, len(deps))
		for i, d := range deps {
			got[i] = d.IssueID + "->" + d.DependsOnID
		}
		t.Fatalf("%s edges = %q, want only %s->%s", id, got, id, dependsOn)
	}
}

// TestCreateIssuesReplayKeepsEdgesOnRemintedID pins the batch replay leg of
// bd-t4h0n7 (PR #6754) on both server batch arms: a concurrent writer commits
// an explicit issue (with its own comment and edge) under the id an open
// batch minted for its auto-minted item. On the replay the item must take a
// fresh id, and the comment id derived from the lost id and the edge owner
// defaulted to it must follow the new id rather than land on the winner,
// whose comment and edge stay exactly as it wrote them.
func TestCreateIssuesReplayKeepsEdgesOnRemintedID(t *testing.T) {
	for _, arm := range []struct {
		name      string
		ephemeral bool
	}{{"durable", false}, {"all wisps", true}} {
		t.Run(arm.name, func(t *testing.T) {
			store, cleanup := setupConcurrentTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()
			for _, id := range []string{"test-winnertarget", "test-losertarget"} {
				target := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
				if err := store.CreateIssue(ctx, target, "tester"); err != nil {
					t.Fatalf("CreateIssue(%s): %v", id, err)
				}
			}

			loser := autoMintIssue("loser")
			loser.Ephemeral = arm.ephemeral
			loser.Comments = []*types.Comment{{Author: "tester", Text: "loser note"}}
			loser.Dependencies = []*types.Dependency{{DependsOnID: "test-losertarget", Type: types.DepBlocks}}
			explicit := &types.Issue{ID: "test-explicit", Title: "explicit", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: arm.ephemeral}
			var firstCommentID string
			firstID, attempts := commitWinnerOnFirstAttempt(t, store, loser, func(_ context.Context, mintedID string) error {
				firstCommentID = loser.Comments[0].ID
				winner := &types.Issue{
					ID: mintedID, Title: "winner", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug, Ephemeral: arm.ephemeral,
					Comments:     []*types.Comment{{Author: "rival", Text: "winner note"}},
					Dependencies: []*types.Dependency{{DependsOnID: "test-winnertarget", Type: types.DepBlocks}},
				}
				// The batch path, since it is the one that persists Dependencies.
				return store.CreateIssuesWithFullOptions(ctx, []*types.Issue{winner}, "rival", storage.BatchCreateOptions{SkipPrefixValidation: true})
			})
			if err := store.CreateIssues(ctx, []*types.Issue{loser, explicit}, "tester"); err != nil {
				t.Fatalf("CreateIssues(loser, explicit): %v", err)
			}
			if *attempts < 2 {
				t.Fatalf("loser made %d attempt(s); the winner's commit must force a replay", *attempts)
			}
			if loser.ID == "" || loser.ID == *firstID {
				t.Fatalf("loser id = %q after replay, want a fresh id distinct from the lost %q", loser.ID, *firstID)
			}
			if explicit.ID != "test-explicit" {
				t.Errorf("explicit id = %q, want the supplied test-explicit", explicit.ID)
			}

			requireIssueTitle(t, ctx, store, *firstID, "winner")
			requireOnlyComment(t, ctx, store, *firstID, "winner note")
			requireOnlyEdgeTo(t, ctx, store, *firstID, "test-winnertarget")
			requireOneCreatedEvent(t, ctx, store, *firstID)

			requireIssueTitle(t, ctx, store, loser.ID, "loser")
			commentID := requireOnlyComment(t, ctx, store, loser.ID, "loser note")
			if commentID == firstCommentID || loser.Comments[0].ID != commentID {
				t.Errorf("loser comment id = %q (stored %q), want one re-derived for %s, not the lost attempt's %q",
					loser.Comments[0].ID, commentID, loser.ID, firstCommentID)
			}
			requireOnlyEdgeTo(t, ctx, store, loser.ID, "test-losertarget")
			if loser.Dependencies[0].IssueID != loser.ID {
				t.Errorf("loser edge owner = %q, want %s", loser.Dependencies[0].IssueID, loser.ID)
			}
			requireOneCreatedEvent(t, ctx, store, loser.ID)
			requireIssueTitle(t, ctx, store, "test-explicit", "explicit")
		})
	}
}
