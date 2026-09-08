//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestEventsJournalPruneNeverSplitsASkewedPair is the regression guard for the
// clock-skew prune hole, run against the real engine and a real skewed table.
//
// The journal's ts is CLIENT-stamped at insert time, so it is not monotone in
// seq: two writers against one SQL server, or one writer whose clock is stepped
// back by NTP between commits, produce a journal where an OLDER timestamp sits
// at a HIGHER seq. A retain-days floor expressed as a per-row `ts < cutoff`
// then deletes that higher seq while keeping its lower-seq neighbour, leaving a
// hole in the MIDDLE of the retained window — which the truncation check, which
// can only see a missing prefix from the caller's checkpoint, would never
// report. Silent record loss, in the one feature whose entire promise is that a
// cursor can be trusted.
//
// The fixture below is exactly that shape: seq 3 is young, seq 4 is old.
// A per-row age predicate deletes 1, 2 and 4 and keeps 3, 5, 6. Resolving the
// age floor to a seq instead keeps everything from the oldest still-young seq
// upward, so 4 survives with 3 and the survivors stay contiguous.
func TestEventsJournalPruneNeverSplitsASkewedPair(t *testing.T) {
	env := newTestEnv(t, "skw")
	ctx := context.Background()
	store := env.store
	store.SetEventsJournalEnabled(true)

	for _, id := range []string{"skw-1", "skw-2", "skw-3", "skw-4", "skw-5", "skw-6"} {
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
		}, "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	rows, err := store.ReadEventsJournal(ctx, 0, 0)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(rows) != 6 {
		t.Fatalf("read %d rows, want 6", len(rows))
	}

	// Stamp the skew. Everything is outside a 7-day window except seq 3, 5 and
	// 6; seq 4 is the row whose clock stepped back.
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -30)
	young := now.Add(-time.Hour)
	for _, s := range []struct {
		seq int64
		ts  time.Time
	}{
		{rows[0].Seq, old}, {rows[1].Seq, old}, {rows[2].Seq, young},
		{rows[3].Seq, old}, {rows[4].Seq, young}, {rows[5].Seq, young},
	} {
		env.exec(t, ctx, "UPDATE bd_events_journal SET ts = ? WHERE seq = ?", s.ts, s.seq)
	}

	n, err := store.PruneEventsJournal(ctx, 1_000_000, 7, 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 2 {
		t.Fatalf("prune deleted %d rows, want 2 (only the prefix below the oldest still-young seq)", n)
	}

	after, err := store.ReadEventsJournal(ctx, rows[1].Seq, 0)
	if err != nil {
		t.Fatalf("read after prune: %v", err)
	}
	if len(after) != 4 {
		t.Fatalf("after prune %d rows, want 4: %+v", len(after), after)
	}
	for i := 1; i < len(after); i++ {
		if after[i].Seq != after[i-1].Seq+1 {
			t.Fatalf("prune left a hole between seq %d and %d: an old row was deleted out from under a younger one",
				after[i-1].Seq, after[i].Seq)
		}
	}
	// The specific row a per-row age predicate would have taken.
	if after[1].Seq != rows[3].Seq {
		t.Errorf("seq %d (old, but above the age floor) did not survive: %+v", rows[3].Seq, after)
	}
}

// TestEventsJournalPruneWhenEveryRowIsOlderThanTheCutoff is the steady state of
// any workspace that has been idle longer than its retain-days window, and it
// is the shape the age floor's SQL answers oddly: MIN(seq) over no matching
// rows returns ONE row containing NULL, not sql.ErrNoRows. Scanning that into a
// plain int64 fails with a conversion error and takes the user's prune down
// with it; scanning into sql.NullInt64 and reading Valid is what makes "nothing
// is young enough" mean "the age floor constrains nothing".
//
// The decision table covers this against an injected fake. This covers it
// against the real engine, because the fake cannot be wrong about the driver.
func TestEventsJournalPruneWhenEveryRowIsOlderThanTheCutoff(t *testing.T) {
	env := newTestEnv(t, "old")
	ctx := context.Background()
	store := env.store
	store.SetEventsJournalEnabled(true)

	for _, id := range []string{"old-1", "old-2", "old-3", "old-4"} {
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
		}, "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	rows, err := store.ReadEventsJournal(ctx, 0, 0)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("read %d rows, want 4", len(rows))
	}

	// Age every row well past the window: the journal of a workspace nobody has
	// touched in a month.
	stale := time.Now().UTC().AddDate(0, 0, -30)
	for _, r := range rows {
		env.exec(t, ctx, "UPDATE bd_events_journal SET ts = ? WHERE seq = ?", stale, r.Seq)
	}

	// The age floor protects nothing, so --before alone governs: rows below the
	// third seq go, the rest stay.
	n, err := store.PruneEventsJournal(ctx, rows[2].Seq, 7, 0)
	if err != nil {
		t.Fatalf("prune with every row older than the cutoff: %v", err)
	}
	if n != 2 {
		t.Fatalf("prune deleted %d rows, want 2", n)
	}
	after, err := store.ReadEventsJournal(ctx, rows[1].Seq, 0)
	if err != nil {
		t.Fatalf("read after prune: %v", err)
	}
	if len(after) != 2 || after[0].Seq != rows[2].Seq {
		t.Fatalf("survivors = %+v, want the two rows from seq %d up", after, rows[2].Seq)
	}
}

// TestEventsJournalReadRefusesAnInteriorHole is the defense-in-depth half. bd's
// own prune cannot produce a hole above the floor (it resolves to one `seq <
// bound` prefix delete), but a restored, hand-edited or partially-copied
// journal table can — and that hole is invisible to a left-edge-only check.
// The read sweeps the batch it already has in memory and refuses rather than
// hand a consumer a window with a record silently missing from the middle.
func TestEventsJournalReadRefusesAnInteriorHole(t *testing.T) {
	env := newTestEnv(t, "hol")
	ctx := context.Background()
	store := env.store
	store.SetEventsJournalEnabled(true)

	for _, id := range []string{"hol-1", "hol-2", "hol-3", "hol-4", "hol-5"} {
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
		}, "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	rows, err := store.ReadEventsJournal(ctx, 0, 0)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("read %d rows, want 5", len(rows))
	}
	head := rows[len(rows)-1].Seq

	// Punch out a middle row, the way a hand-repaired table arrives.
	env.exec(t, ctx, "DELETE FROM bd_events_journal WHERE seq = ?", rows[2].Seq)

	_, err = store.ReadEventsJournal(ctx, 0, 0)
	trunc := requireTruncated(t, err)
	if trunc.Since != rows[1].Seq {
		t.Errorf("Since = %d, want %d (the last seq servable contiguously from the checkpoint)", trunc.Since, rows[1].Seq)
	}
	if trunc.Floor != rows[3].Seq {
		t.Errorf("Floor = %d, want %d (the start of the next intact island)", trunc.Floor, rows[3].Seq)
	}
	if trunc.Head != head {
		t.Errorf("Head = %d, want %d", trunc.Head, head)
	}

	// Resuming past the hole works: the surviving island is contiguous, so a
	// consumer that accepts the gap is not stuck.
	resumed, err := store.ReadEventsJournal(ctx, trunc.Floor-1, 0)
	if err != nil {
		t.Fatalf("resume from floor-1: %v", err)
	}
	if len(resumed) != 2 || resumed[0].Seq != rows[3].Seq {
		t.Fatalf("resume from floor-1 = %+v, want the two rows above the hole", resumed)
	}
}
