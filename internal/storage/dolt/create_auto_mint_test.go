package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestCreateIssue_CounterModeJumpsPastExplicitIDs is the server-mode twin of
// embeddeddolt's TestAutoMintedCounterIDJumpsPastExplicitIDs: explicit IDs
// created ahead of the counter must neither be overwritten nor wedge it.
func TestCreateIssue_CounterModeJumpsPastExplicitIDs(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("failed to set issue_id_mode: %v", err)
	}

	first := &types.Issue{Title: "auto first", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, first, "tester"); err != nil {
		t.Fatalf("CreateIssue(first): %v", err)
	}
	if first.ID != "test-1" {
		t.Fatalf("first counter ID = %q, want test-1", first.ID)
	}

	explicitIDs := []string{"test-2", "test-3", "test-4"}
	for _, id := range explicitIDs {
		explicit := &types.Issue{ID: id, Title: "explicit " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, explicit, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}

	for _, want := range []string{"test-5", "test-6"} {
		auto := &types.Issue{Title: "auto after lag", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, auto, "tester"); err != nil {
			t.Fatalf("auto CreateIssue (want %s): %v", want, err)
		}
		if auto.ID != want {
			t.Fatalf("auto counter ID = %q, want %s", auto.ID, want)
		}
	}

	for _, id := range explicitIDs {
		got, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		if got.Title != "explicit "+id {
			t.Fatalf("explicit %s title = %q, want %q", id, got.Title, "explicit "+id)
		}
	}
}

// TestCreateIssue_ReplayRemintsAutoMintedID covers withRetryTx replaying an
// auto-minted create. The first attempt mints an ID, and before it commits a
// second writer commits an explicit issue under that same ID. Dolt settles the
// collision at commit: the attempt conflicts, and withRetryTx replays the
// create on a fresh snapshot. The replay must mint a new ID. While the first
// attempt's ID stayed on the struct, the replay took the explicit-ID upsert
// path instead and overwrote the winner's row without an error.
func TestCreateIssue_ReplayRemintsAutoMintedID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// The attempt's transaction holds the store's only connection, so the
	// winner writes through a connection of its own on this test's branch.
	var branch string
	if err := store.db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&branch); err != nil {
		t.Fatalf("active_branch: %v", err)
	}
	winnerDB, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: testSharedDB}.String())
	if err != nil {
		t.Fatalf("open winner connection: %v", err)
	}
	defer winnerDB.Close()
	winnerConn, err := winnerDB.Conn(ctx)
	if err != nil {
		t.Fatalf("winner connection: %v", err)
	}
	defer winnerConn.Close()
	if _, err := winnerConn.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", branch); err != nil {
		t.Fatalf("winner DOLT_CHECKOUT(%s): %v", branch, err)
	}

	attempts := 0
	var firstID string
	store.createIssueAttemptHook = func(ctx context.Context, issue *types.Issue) error {
		attempts++
		if attempts > 1 {
			return nil
		}
		firstID = issue.ID
		tx, err := winnerConn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("winner begin: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		bc, err := issueops.NewBatchContext(ctx, tx, storage.BatchCreateOptions{SkipPrefixValidation: true})
		if err != nil {
			return fmt.Errorf("winner batch context: %w", err)
		}
		winner := &types.Issue{ID: issue.ID, Title: "winner", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := issueops.CreateIssueInTx(ctx, tx, bc, winner, "winner"); err != nil {
			return fmt.Errorf("winner create: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("winner commit: %w", err)
		}
		return nil
	}

	loser := &types.Issue{Title: "loser", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, loser, "loser"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if attempts < 2 {
		t.Fatalf("create ran %d attempt(s); the winner's commit should have forced a replay", attempts)
	}
	winner, err := store.GetIssue(ctx, firstID)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", firstID, err)
	}
	if winner.Title != "winner" {
		t.Errorf("the replay overwrote the winner: %s title = %q, want %q", firstID, winner.Title, "winner")
	}
	if loser.ID == firstID {
		t.Fatalf("the replay kept the first attempt's minted ID %q", firstID)
	}
	got, err := store.GetIssue(ctx, loser.ID)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", loser.ID, err)
	}
	if got.Title != "loser" {
		t.Errorf("%s title = %q, want %q", loser.ID, got.Title, "loser")
	}
}

// TestCreateIssues_ReplayRemintsAutoMintedIDs is the batch-path twin of
// TestCreateIssue_ReplayRemintsAutoMintedID, covering createIssuesWithFullOptions
// instead of createIssue. Same collision-then-replay shape (a second writer
// commits "winner" rows under the first attempt's minted IDs before it
// commits, forcing withRetryTx to replay), but with two issues in the batch,
// both colliding independently: this proves the fix resets every auto-minted
// ID in the batch on replay, not just the first one checked.
//
// Both losers must collide. The default (non-counter) ID mint,
// idgen.GenerateHashID(prefix, title, description, creator, createdAt,
// length, nonce), is a pure function of its inputs, not fresh entropy, and
// createdAt freezes after the first PrepareIssueForInsert call. A replay
// whose issue.ID is reset to "" but whose candidate is never actually
// contested deterministically re-derives the SAME id it minted on attempt 1
// — only a real collision forces the nonce (and so the candidate) to
// advance. A batch fix that resets every issue's ID but is only exercised
// against one real collision would look identical, on the uncontested
// issue, to a fix that resets nothing at all: both produce the same final
// ID. So both loser A and loser B need their first-attempt IDs claimed out
// from under them here, or the second one's assertions can't actually tell
// a reset apart from a no-op.
//
// Loser B also carries a dependency with an unset IssueID, covering fix spec
// item 4: PersistDependenciesWithOptionsResult defaults an empty
// Dependency.IssueID to the owning issue's ID, but only when it is still
// empty. Since Issue.Dependencies is a []*Dependency, that default survives a
// replay the same way a minted issue ID does. A fix that resets issue.ID on
// every attempt but not Dependencies[*].IssueID would leave the dependency
// row pointing at loser B's abandoned first-attempt ID once that ID is
// forced to change — an ID that is never actually inserted anywhere, since
// the fixed code re-mints a fresh one.
func TestCreateIssues_ReplayRemintsAutoMintedIDs(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	target := &types.Issue{Title: "dep target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, target, "tester"); err != nil {
		t.Fatalf("CreateIssue(target): %v", err)
	}

	// The attempt's transaction holds the store's only connection, so the
	// winner writes through a connection of its own on this test's branch.
	var branch string
	if err := store.db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&branch); err != nil {
		t.Fatalf("active_branch: %v", err)
	}
	winnerDB, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: testSharedDB}.String())
	if err != nil {
		t.Fatalf("open winner connection: %v", err)
	}
	defer winnerDB.Close()
	winnerConn, err := winnerDB.Conn(ctx)
	if err != nil {
		t.Fatalf("winner connection: %v", err)
	}
	defer winnerConn.Close()
	if _, err := winnerConn.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", branch); err != nil {
		t.Fatalf("winner DOLT_CHECKOUT(%s): %v", branch, err)
	}

	loserA := &types.Issue{Title: "loser A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	loserB := &types.Issue{
		Title: "loser B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{DependsOnID: target.ID, Type: types.DepRelated}},
	}

	attempts := 0
	var firstAID, firstBID string
	store.createIssueAttemptHook = func(ctx context.Context, issue *types.Issue) error {
		attempts++
		if attempts > 1 {
			return nil
		}
		firstAID = issue.ID
		// loserB is the same *types.Issue the batch call is mutating (issues
		// are passed as []*types.Issue, so this closure sees CreateIssuesInTxWithResult's
		// minted ID directly on loserB, no need to thread it through the hook).
		firstBID = loserB.ID
		tx, err := winnerConn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("winner begin: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		bc, err := issueops.NewBatchContext(ctx, tx, storage.BatchCreateOptions{SkipPrefixValidation: true})
		if err != nil {
			return fmt.Errorf("winner batch context: %w", err)
		}
		// Claim both first-attempt IDs, not just loser A's. See the doc
		// comment above: without a real collision on loser B's slot too, its
		// deterministic hash-based candidate is legitimately unchanged by a
		// correct reset, so this test needs both to actually distinguish a
		// fix that resets the whole batch from one that resets nothing.
		winnerA := &types.Issue{ID: firstAID, Title: "winner A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := issueops.CreateIssueInTx(ctx, tx, bc, winnerA, "winner"); err != nil {
			return fmt.Errorf("winner A create: %w", err)
		}
		winnerB := &types.Issue{ID: firstBID, Title: "winner B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := issueops.CreateIssueInTx(ctx, tx, bc, winnerB, "winner"); err != nil {
			return fmt.Errorf("winner B create: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("winner commit: %w", err)
		}
		return nil
	}

	if err := store.CreateIssues(ctx, []*types.Issue{loserA, loserB}, "loser"); err != nil {
		t.Fatalf("CreateIssues: %v", err)
	}
	if attempts < 2 {
		t.Fatalf("create ran %d attempt(s); the winner's commit should have forced a replay", attempts)
	}

	winnerA, err := store.GetIssue(ctx, firstAID)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", firstAID, err)
	}
	if winnerA.Title != "winner A" {
		t.Errorf("the replay overwrote winner A: %s title = %q, want %q", firstAID, winnerA.Title, "winner A")
	}
	winnerB, err := store.GetIssue(ctx, firstBID)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", firstBID, err)
	}
	if winnerB.Title != "winner B" {
		t.Errorf("the replay overwrote winner B: %s title = %q, want %q", firstBID, winnerB.Title, "winner B")
	}
	if loserA.ID == firstAID {
		t.Fatalf("loser A kept the first attempt's minted ID %q", firstAID)
	}
	if loserB.ID == firstBID {
		t.Fatalf("loser B kept the first attempt's minted ID %q; the fix must reset every auto-minted issue in the batch, not just the one that collided", firstBID)
	}

	gotA, err := store.GetIssue(ctx, loserA.ID)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", loserA.ID, err)
	}
	if gotA.Title != "loser A" {
		t.Errorf("%s title = %q, want %q", loserA.ID, gotA.Title, "loser A")
	}
	gotB, err := store.GetIssue(ctx, loserB.ID)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", loserB.ID, err)
	}
	if gotB.Title != "loser B" {
		t.Errorf("%s title = %q, want %q", loserB.ID, gotB.Title, "loser B")
	}

	var depIssueID string
	if err := store.db.QueryRowContext(ctx,
		"SELECT issue_id FROM dependencies WHERE depends_on_issue_id = ? AND type = ?",
		target.ID, string(types.DepRelated),
	).Scan(&depIssueID); err != nil {
		t.Fatalf("query loser B's dependency: %v", err)
	}
	if depIssueID != loserB.ID {
		t.Errorf("dependencies.issue_id = %q, want loser B's final ID %q; a replay must reset Dependencies[*].IssueID for auto-minted issues too, or it keeps pointing at the abandoned first-attempt ID", depIssueID, loserB.ID)
	}
}
