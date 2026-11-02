package dolt

import (
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestJournalWriterLock_ConcurrentWritersSerialize pins AC-1 of
// gastownhall/beads#6117: with the events journal on in server mode, a second
// journaled writer's transaction may not BEGIN while a first writer is paused
// inside its own — the waiter queues on the journal-writer admission lock —
// and once the holder commits, both writes land with contiguous journal
// sequences in SQL commit order and the waiter's body ran exactly once (no
// counter-conflict replay). On the unfixed store the waiter's body starts
// inside the holder's snapshot, so the holder's commit aborts on the single
// bd_events_seq row and the write path replays it.
func TestJournalWriterLock_ConcurrentWritersSerialize(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	store.SetEventsJournalEnabled(true)
	defer store.SetEventsJournalEnabled(false)

	newIssue := func(title string) *types.Issue {
		return &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}

	// The waiter's target row, created before the experiment so the waiter's
	// body is one journaled update.
	target := newIssue("waiter target")
	if err := store.CreateIssue(ctx, target, "actor"); err != nil {
		t.Fatalf("create target issue: %v", err)
	}

	// Writer A: a journaled writer that pauses INSIDE its transaction after
	// emitting, holding the journal-writer admission lock while paused.
	writerA := newIssue("writer A issue")
	aInside := make(chan struct{}, 1)
	releaseA := make(chan struct{})
	longDone := make(chan error, 1)
	go func() {
		longDone <- store.RunInTransaction(ctx, "writer A", func(tx storage.Transaction) error {
			if err := tx.CreateIssue(ctx, writerA, "actor"); err != nil {
				return err
			}
			aInside <- struct{}{}
			select {
			case <-releaseA:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()

	// Watchdog: A must reach its pause; the test never waits unbounded.
	select {
	case <-aInside:
	case <-time.After(30 * time.Second):
		close(releaseA)
		t.Fatalf("writer A never entered its transaction")
	}

	// Writer B: a journaled writer through the store funnel. Its body may run
	// at most once and signals when it starts.
	var bRuns int
	bStarted := make(chan struct{}, 1)
	shortDone := make(chan error, 1)
	go func() {
		shortDone <- store.withRetryTx(ctx, func(tx *sql.Tx) error {
			bRuns++
			select {
			case bStarted <- struct{}{}:
			default:
			}
			if _, err := issueops.UpdateIssueInTx(ctx, tx, target.ID, map[string]interface{}{"status": types.StatusInProgress}, "actor-b"); err != nil {
				return err
			}
			return nil
		})
	}()

	// The AC-1 assertion: while A is paused inside its transaction, B does
	// not BEGIN. On the unfixed store B's body starts during A's snapshot.
	select {
	case <-bStarted:
		close(releaseA)
		<-longDone
		<-shortDone
		t.Fatalf("waiter began its transaction while the holder was inside its own")
	case <-time.After(1500 * time.Millisecond):
	}

	// Release A; both writers must complete.
	close(releaseA)
	select {
	case err := <-longDone:
		if err != nil {
			t.Fatalf("holder writer failed: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("holder writer never completed")
	}
	select {
	case err := <-shortDone:
		if err != nil {
			t.Fatalf("waiter writer failed: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("waiter writer never completed")
	}
	if bRuns != 1 {
		t.Fatalf("waiter body ran %d times, want exactly 1 (no counter-conflict replay)", bRuns)
	}

	// Both writes are durable with contiguous journal sequences in SQL commit
	// order: the sequence is gapless, A's row precedes B's, and B's update is
	// the last row — it committed after the holder released the lock.
	rows, err := store.ReadEventsJournal(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("read events journal: %v", err)
	}
	if len(rows) < 3 {
		t.Fatalf("journal rows = %d, want >= 3", len(rows))
	}
	slices.SortFunc(rows, func(a, b storage.EventsJournalRow) int {
		return int(a.Seq - b.Seq)
	})
	for i, r := range rows {
		if r.Seq != rows[0].Seq+int64(i) {
			t.Fatalf("journal seq %d at index %d breaks the contiguous sequence (first seq %d)", r.Seq, i, rows[0].Seq)
		}
	}
	last := rows[len(rows)-1]
	if last.IssueID != target.ID {
		t.Fatalf("last journal row is %s, want the waiter's update of %s (SQL commit order)", last.IssueID, target.ID)
	}
	holderSeq := int64(-1)
	for _, r := range rows {
		if r.IssueID == writerA.ID {
			holderSeq = r.Seq
		}
	}
	if holderSeq < 0 {
		t.Fatalf("no journal row for the holder's issue %s", writerA.ID)
	}
	if holderSeq >= last.Seq {
		t.Fatalf("holder journal seq %d is not before the waiter's %d (SQL commit order)", holderSeq, last.Seq)
	}
}