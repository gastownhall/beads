//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/storagecontract"
	"github.com/steveyegge/beads/internal/types"
)

// TestHistoryPresenceContract runs the HistoryPresence contract against the
// embedded store — the DEFAULT open path, and the one with no DiffStore, so
// this capability is auto-export's only way to prove a deletion there
// (GH#5896).
func TestHistoryPresenceContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "hp")
	ctx := t.Context()

	fixture := storagecontract.HistoryPresenceFixture{
		Prefix:   "hp",
		Presence: te.store,
		CreateIssue: func(ctx context.Context, id, title string) error {
			return te.store.CreateIssue(ctx, &types.Issue{
				ID:        id,
				Title:     title,
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}, "tester")
		},
		DeleteIssue:   te.store.DeleteIssue,
		Commit:        te.store.Commit,
		CurrentCommit: te.store.GetCurrentCommit,
		ResetHard: func(ctx context.Context, commit string) error {
			te.exec(t, ctx, "CALL DOLT_RESET('--hard', ?)", commit)
			return nil
		},
	}

	storagecontract.RunHistoryPresenceContract(t, ctx, fixture)
}
