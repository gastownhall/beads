package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestCreateIssuesRetryReMintsAutoIDAfterConcurrentWinner forces the GH#7289
// interleaving deterministically: while the batch create's transaction is open
// (after it minted and wrote its auto id, inside the dependency pass's skip
// callback), a second connection commits an issue under that same id. The
// batch's commit then conflicts and withRetryTx replays the body. The replay
// must mint a fresh id; replaying the stale one as an explicit id upserts over
// the concurrent winner's committed row.
func TestCreateIssuesRetryReMintsAutoIDAfterConcurrentWinner(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		Title:     "batch loser",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "test-missing-target",
			Type:        types.DepBlocks,
		}},
	}

	var staleID string
	var winnerErr error
	opts := storage.BatchCreateOptions{
		OnSkippedDependency: func(issueID, _, _ string) {
			if staleID != "" {
				return
			}
			staleID = issueID
			winnerErr = store.CreateIssue(ctx, &types.Issue{
				ID:        issueID,
				Title:     "concurrent winner",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
			}, "winner")
		},
	}

	if err := store.CreateIssuesWithFullOptions(ctx, []*types.Issue{issue}, "loser", opts); err != nil {
		t.Fatalf("CreateIssuesWithFullOptions: %v", err)
	}
	if staleID == "" {
		t.Fatal("skip callback never ran: the interleaving was not forced")
	}
	if winnerErr != nil {
		t.Fatalf("concurrent winner create: %v", winnerErr)
	}

	winner, err := store.GetIssue(ctx, staleID)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", staleID, err)
	}
	if winner.Title != "concurrent winner" {
		t.Errorf("concurrent winner %s was overwritten by the retried batch: title = %q, want %q", staleID, winner.Title, "concurrent winner")
	}
	if issue.ID == staleID {
		t.Errorf("retried batch reused the stale auto-minted id %s instead of minting a fresh one", staleID)
	}
}
