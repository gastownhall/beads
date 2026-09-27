//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestEventsJournalRestartAndRetentionBoundary is the AC03 end-to-end guard: an
// exporter that restarts must resume the acknowledged contiguous prefix, and one
// whose checkpoint was pruned past must be told so rather than silently skipping
// to the current floor or receiving an empty success.
//
// It runs against the real embedded store — the same read path `bd events
// tail --since` takes — so the counter self-heal, the prune floors, and the
// truncation check are all exercised together rather than mocked.
//
// IT SURVIVED THE JOURNAL CONTRACT, and which half survived is the point.
// backend/conformance/journal_contract.go now pins the same restart, retention
// and truncation promises on all three legs — but it pins them on
// journalops.Journal, whose body is issueops.ReadEventsPageInTx. Every read
// below is ReadEventsJournal, which is storage.EventsJournalAccessor's
// list-only read and a SECOND composition of the same parts: it pays for the
// head only where the verdict is ambiguous, because `bd events tail --follow`
// runs it every second. A mutation of one is invisible to the other, so the
// role contract cannot stand in for this file. Its page-path twin,
// TestEventsJournalPageReportsTheHead, could and was deleted with the contract
// that replaced it.
func TestEventsJournalRestartAndRetentionBoundary(t *testing.T) {
	env := newTestEnv(t, "trn")
	ctx := context.Background()
	store := env.store
	store.SetEventsJournalEnabled(true)

	must := func(err error, what string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	create := func(id string) {
		t.Helper()
		must(store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
		}, "actor"), "create "+id)
	}

	for _, id := range []string{"trn-1", "trn-2", "trn-3", "trn-4", "trn-5", "trn-6"} {
		create(id)
	}

	all, err := store.ReadEventsJournal(ctx, 0, 0)
	must(err, "read all")
	if len(all) != 6 {
		t.Fatalf("read %d rows, want 6", len(all))
	}
	head := all[len(all)-1].Seq

	// Restart: a consumer that acknowledged through row 3 resumes at 4 with no
	// loss and no error.
	ack := all[2].Seq
	resumed, err := store.ReadEventsJournal(ctx, ack, 0)
	must(err, "resume after restart")
	if len(resumed) != 3 || resumed[0].Seq != ack+1 {
		t.Fatalf("restart resume = %d rows starting at %d, want 3 starting at %d", len(resumed), resumed[0].Seq, ack+1)
	}

	// Caught up: reading from the head is an empty success, never a truncation.
	tip, err := store.ReadEventsJournal(ctx, head, 0)
	must(err, "read at head")
	if len(tip) != 0 {
		t.Fatalf("read at head returned %d rows, want 0", len(tip))
	}

	// Prune the first three rows. A consumer still at the acknowledged
	// watermark is exactly at the new floor-1 and must keep working.
	n, err := store.PruneEventsJournal(ctx, all[3].Seq, 0, 0)
	must(err, "prune")
	if n != 3 {
		t.Fatalf("pruned %d rows, want 3", n)
	}
	stillFine, err := store.ReadEventsJournal(ctx, ack, 0)
	must(err, "read at the retained floor")
	if len(stillFine) != 3 || stillFine[0].Seq != ack+1 {
		t.Fatalf("read at floor-1 = %d rows starting at %d, want 3 starting at %d", len(stillFine), stillFine[0].Seq, ack+1)
	}

	// Pruned past: a checkpoint one below the floor-1 boundary must fail typed,
	// carrying the window the engine can still serve.
	_, err = store.ReadEventsJournal(ctx, ack-1, 0)
	trunc := requireTruncated(t, err)
	if trunc.Since != ack-1 {
		t.Errorf("Since = %d, want %d", trunc.Since, ack-1)
	}
	if trunc.Floor != all[3].Seq {
		t.Errorf("Floor = %d, want %d", trunc.Floor, all[3].Seq)
	}
	if trunc.Head != head {
		t.Errorf("Head = %d, want %d", trunc.Head, head)
	}

	// `bd events export` (--since 0) over a pruned journal must not silently
	// present the surviving suffix as a complete history.
	_, err = store.ReadEventsJournal(ctx, 0, 0)
	if exported := requireTruncated(t, err); exported.Floor != all[3].Seq {
		t.Errorf("export Floor = %d, want %d", exported.Floor, all[3].Seq)
	}

	// The limit path takes the same decision: a capped read from a pruned
	// checkpoint must not return a truncated window as success.
	_, err = store.ReadEventsJournal(ctx, 0, 2)
	requireTruncated(t, err)

	// Prune everything. An empty journal is indistinguishable from "nothing
	// new" at the SQL level, so this is the case that would otherwise strand a
	// consumer forever on a poll loop.
	if _, err := store.PruneEventsJournal(ctx, head+1, 0, 0); err != nil {
		t.Fatalf("prune all: %v", err)
	}
	empty := requireTruncated(t, secondErr(store.ReadEventsJournal(ctx, ack, 0)))
	if empty.Floor != head+1 || empty.Head != head {
		t.Errorf("fully-pruned window = [%d..%d], want [%d..%d]", empty.Floor, empty.Head, head+1, head)
	}
	// A consumer that had already reached the head is caught up, not truncated.
	if _, err := store.ReadEventsJournal(ctx, head, 0); err != nil {
		t.Errorf("read at head of a fully pruned journal must succeed empty, got %v", err)
	}

	// Seq never resets across a prune, so the next mutation continues the
	// history rather than colliding with a consumer's dedupe window.
	create("trn-7")
	next, err := store.ReadEventsJournal(ctx, head, 0)
	must(err, "read after post-prune create")
	if len(next) != 1 || next[0].Seq != head+1 {
		t.Fatalf("post-prune create = %+v, want a single row at seq %d", next, head+1)
	}
}

// TestEventsJournalPageReportsTheHead was here, and the Journal contract
// replaced it: RunJournalLimitCapsRowsNotHead pins that a bounded page reports
// the JOURNAL's head, RunJournalHeadArrivesWithItsRowsAndDetectsCaughtUp pins
// the caught-up answer, RunJournalHeadSurvivesAFullPrune pins the head standing
// over an emptied table, and RunJournalTruncationIsTypedAndNamesTheWindow pins
// the typed failure on the same read. Every one of them now runs on three legs
// where this ran on one, against the same body
// (issueops.ReadEventsPageInTx). See journal_contract_test.go.

func requireTruncated(t *testing.T, err error) *storage.EventsJournalTruncatedError {
	t.Helper()
	var trunc *storage.EventsJournalTruncatedError
	if err == nil {
		t.Fatal("expected a truncation error, got nil (silent skip or empty success)")
	}
	if !errors.As(err, &trunc) {
		t.Fatalf("error is not *EventsJournalTruncatedError: %T %v", err, err)
	}
	return trunc
}

func secondErr(_ []storage.EventsJournalRow, err error) error { return err }
