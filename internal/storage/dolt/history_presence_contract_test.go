package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/storagecontract"
	"github.com/steveyegge/beads/internal/types"
)

// TestHistoryPresenceContract runs the HistoryPresence contract against the
// server-backed store. It shares a body with the embedded leg
// (issueops.HistoricalIssueIDsInTx) and differs only in how the read
// connection is opened, which is what this wiring checks.
func TestHistoryPresenceContract(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	fixture := storagecontract.HistoryPresenceFixture{
		Prefix:   "hp",
		Presence: store,
		CreateIssue: func(ctx context.Context, id, title string) error {
			return store.CreateIssue(ctx, &types.Issue{
				ID:        id,
				Title:     title,
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}, "tester")
		},
		DeleteIssue:   store.DeleteIssue,
		Commit:        store.Commit,
		CurrentCommit: store.GetCurrentCommit,
		ResetHard: func(ctx context.Context, commit string) error {
			_, err := store.db.ExecContext(ctx, "CALL DOLT_RESET('--hard', ?)", commit)
			return err
		},
	}

	storagecontract.RunHistoryPresenceContract(t, ctx, fixture)
}
