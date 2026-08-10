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

// TestEventsJournalPageReportsTheHead is the guard for the read that
// GET /v0/beads/events answers from. The head is the whole reason that read
// exists beside ReadEventsJournal — it is how a polling consumer tells "this
// page is the end" from "there is more, keep asking" — so it is checked against
// the substrate rather than assumed.
//
// Three properties, and the last one is the one that would break silently:
// the head reflects every mutation ever journaled; it survives a prune, because
// prune deletes rows and never touches the counter; and a truncated read still
// fails through this path rather than answering with rows and a plausible head.
func TestEventsJournalPageReportsTheHead(t *testing.T) {
	env := newTestEnv(t, "pge")
	ctx := context.Background()
	store := env.store
	store.SetEventsJournalEnabled(true)

	must := func(err error, what string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	for _, id := range []string{"pge-1", "pge-2", "pge-3", "pge-4", "pge-5"} {
		must(store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
		}, "actor"), "create "+id)
	}

	// A BOUNDED page: the head must describe the journal, not the page. A head
	// derived from the last row returned would read as "caught up" here and
	// stall a consumer three records short.
	page, err := store.ReadEventsJournalPage(ctx, 0, 2)
	must(err, "read page")
	if len(page.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(page.Rows))
	}
	if page.Head != 5 {
		t.Fatalf("head = %d, want 5 — the head must be the journal's, not the page's", page.Head)
	}

	// Caught up: no rows, and the same head.
	caughtUp, err := store.ReadEventsJournalPage(ctx, 5, 0)
	must(err, "read caught up")
	if len(caughtUp.Rows) != 0 || caughtUp.Head != 5 {
		t.Fatalf("caught-up page = %+v, want no rows and head 5", caughtUp)
	}

	// Prune the whole journal. The counter is untouched, so the head stands
	// while the rows are gone — which is what lets a fully pruned journal say
	// "you are at the end of my history" instead of "I have nothing".
	if _, err := store.PruneEventsJournal(ctx, 6, 0, 0); err != nil {
		t.Fatalf("prune: %v", err)
	}
	afterPrune, err := store.ReadEventsJournalPage(ctx, 5, 0)
	must(err, "read after prune")
	if len(afterPrune.Rows) != 0 || afterPrune.Head != 5 {
		t.Fatalf("post-prune page = %+v, want no rows and head 5", afterPrune)
	}

	// And a checkpoint below the pruned window still FAILS on this path, with
	// the same window the CLI's read reports. A page read that answered an
	// empty success here would be the silent-loss case the truncation contract
	// exists to prevent, reintroduced by the second read plumbing.
	_, err = store.ReadEventsJournalPage(ctx, 2, 0)
	trunc := requireTruncated(t, err)
	if trunc.Since != 2 || trunc.Floor != 6 || trunc.Head != 5 {
		t.Fatalf("truncation = %+v, want since 2, floor 6, head 5", trunc)
	}
}

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
