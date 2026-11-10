//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestRunInTransactionPostCommitStageFailureIsIndeterminate(t *testing.T) {
	te := newTestEnv(t, "eci")
	ctx := context.Background()
	calls := 0

	err := te.store.RunInTransaction(ctx, "test: indeterminate staged commit", func(tx storage.Transaction) error {
		calls++
		if err := tx.CreateIssue(ctx, &types.Issue{
			ID:        "eci-post-commit",
			Title:     "post-commit failure",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}, "tester"); err != nil {
			return err
		}
		// Closing the store leaves this transaction's pinned SQL connection
		// intact, but deterministically rejects the later staging connection.
		return te.store.Close()
	})
	if err == nil {
		t.Fatal("post-commit stage failure returned nil")
	}
	if !errors.Is(err, beads.ErrCommitIndeterminate) {
		t.Fatalf("errors.Is(err, beads.ErrCommitIndeterminate) = false; err = %v", err)
	}
	if calls != 1 {
		t.Fatalf("callback calls = %d, want 1", calls)
	}
	te.assertRowExists(t, ctx, "issues", "eci-post-commit")
}
