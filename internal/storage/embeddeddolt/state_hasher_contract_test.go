//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/storagecontract"
	"github.com/steveyegge/beads/internal/types"
)

// TestStateHasherContract runs the StateHasher contract against the embedded
// store. Embedded mode is the default open path, and under batch or off
// auto-commit its writes stay in the working set exactly as server-mode
// writes do, so HEAD-only change detection skipped them (GH#6955).
func TestStateHasherContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "sh")
	ctx := t.Context()

	storagecontract.RunStateHasherContract(t, ctx, storagecontract.StateHasherFixture{
		Prefix: "sh",
		Store:  te.store,
		CreateIssue: func(ctx context.Context, id, title string) error {
			return te.store.CreateIssue(ctx, &types.Issue{
				ID:        id,
				Title:     title,
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}, "tester")
		},
		ReadIssue: func(ctx context.Context, id string) error {
			_, err := te.store.GetIssue(ctx, id)
			return err
		},
		Commit:        te.store.Commit,
		CurrentCommit: te.store.GetCurrentCommit,
	})
}
