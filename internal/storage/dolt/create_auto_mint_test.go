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
