package dolt

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestJournaledWritersOverlapDoNotAbort reproduces gastownhall/beads#6117 /
// PR #7645: in server mode with the events journal on, two overlapping
// journaled write transactions both bump the single bd_events_seq row, so the
// one that commits second is aborted with 1213. The expected behaviour is that
// overlapping journaled writers queue rather than abort each other: both land.
func TestJournaledWritersOverlapDoNotAbort(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	store.SetEventsJournalEnabled(true)
	defer store.SetEventsJournalEnabled(false)

	newIssue := func(title string) *types.Issue {
		return &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}

	shortDone := make(chan error, 1)
	longErr := store.RunInTransaction(ctx, "long writer", func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, newIssue("long writer"), "actor"); err != nil {
			return err
		}
		// A short journaled writer runs while this one is still open. If
		// writers queue, it waits for this commit; give it a bounded window.
		go func() { shortDone <- store.CreateIssue(ctx, newIssue("short writer"), "actor") }()
		select {
		case err := <-shortDone:
			t.Logf("short writer finished inside the long writer's window: %v", err)
			shortDone <- err
		case <-time.After(3 * time.Second):
			t.Logf("short writer still waiting after 3s")
		}
		return nil
	})
	if longErr != nil {
		t.Fatalf("long journaled writer was aborted by an overlapping journaled writer: %v", longErr)
	}
	if err := <-shortDone; err != nil {
		t.Fatalf("short journaled writer failed: %v", err)
	}
	rows, err := store.ReadEventsJournal(ctx, 0, 100)
	t.Logf("journal rows=%d err=%v", len(rows), err)
}
