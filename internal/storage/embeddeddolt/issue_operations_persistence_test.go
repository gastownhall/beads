//go:build cgo

package embeddeddolt

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func TestEmbeddedMoveIssuePersistenceEphemeralNoHistorySmoke(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), ".beads"), "persist", "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "persist"); err != nil {
		t.Fatal(err)
	}
	issue := &types.Issue{ID: "persist-embedded", Title: "embedded", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatal(err)
	}
	if err := store.runTransaction(ctx, "persistence", func(tx *embeddedTransaction) error {
		current, err := issueops.GetIssueInTx(ctx, tx.tx, issue.ID)
		if err != nil {
			return err
		}
		_, err = issueops.MoveIssuePersistenceInTx(ctx, tx.tx, current, types.PersistenceModeNoHistory)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ephemeral || !got.NoHistory {
		t.Fatalf("persistence = (%t,%t), want (false,true)", got.Ephemeral, got.NoHistory)
	}
}
