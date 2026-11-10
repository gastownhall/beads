//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Auto-prune against the real engine. The decision tables elsewhere cover the
// policy and the batch loop against fakes; these cover the two things a fake
// cannot be wrong about — whether Dolt accepts the batched prefix delete, and
// whether a mutation actually leaves the journal bounded.

func createIssues(t *testing.T, ctx context.Context, env *testEnv, prefix string, n int) {
	t.Helper()
	for i := range n {
		id := prefix + "-" + string(rune('a'+i))
		if err := env.store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
		}, "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
}

// createJournaledIssues creates n issues and returns the seqs they journaled.
// Only valid on a journal that has not been pruned yet: it reads from 0.
func createJournaledIssues(t *testing.T, ctx context.Context, env *testEnv, prefix string, n int) []int64 {
	t.Helper()
	createIssues(t, ctx, env, prefix, n)
	rows, err := env.store.ReadEventsJournal(ctx, 0, 0)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(rows) != n {
		t.Fatalf("read %d rows, want %d", len(rows), n)
	}
	seqs := make([]int64, 0, len(rows))
	for _, r := range rows {
		seqs = append(seqs, r.Seq)
	}
	return seqs
}

// TestEventsAutoPruneBoundsTheJournalToTheFloors is the whole feature in one
// test: a workspace with the journal on and a rows floor of three ends up
// holding three records, without anyone running `bd events prune`.
//
// It also pins that the survivors are the NEWEST three and that they are
// contiguous — the automatic path resolves to the same single prefix bound the
// manual one does, because it is the same resolver.
func TestEventsAutoPruneBoundsTheJournalToTheFloors(t *testing.T) {
	env := newTestEnv(t, "apf")
	ctx := context.Background()
	env.store.SetEventsJournalEnabled(true)
	seqs := createJournaledIssues(t, ctx, env, "apf", 8)

	deleted, err := eventsjournal.AutoPrune(ctx, env.store, eventsjournal.AutoPruneOptions{RetainRows: 3})
	if err != nil {
		t.Fatalf("auto-prune: %v", err)
	}
	if deleted != 5 {
		t.Fatalf("auto-prune deleted %d rows, want 5 (everything below the newest three)", deleted)
	}

	after, err := env.store.ReadEventsJournal(ctx, seqs[4], 0)
	if err != nil {
		t.Fatalf("read after auto-prune: %v", err)
	}
	if len(after) != 3 || after[0].Seq != seqs[5] {
		t.Fatalf("survivors = %+v, want the three rows from seq %d up", after, seqs[5])
	}
	for i := 1; i < len(after); i++ {
		if after[i].Seq != after[i-1].Seq+1 {
			t.Fatalf("auto-prune left a hole between seq %d and %d", after[i-1].Seq, after[i].Seq)
		}
	}

	// A consumer still inside the floor is unaffected: its checkpoint resumes
	// cleanly rather than raising the truncation error.
	resumed, err := env.store.ReadEventsJournal(ctx, seqs[5], 0)
	if err != nil {
		t.Fatalf("a consumer inside the retained window was truncated: %v", err)
	}
	if len(resumed) != 2 {
		t.Fatalf("resume from seq %d returned %d rows, want 2", seqs[5], len(resumed))
	}
}

// TestEventsAutoPruneKeepsAnUnboundedJournalUnbounded. Both floors at 0 is the
// documented way to say "keep every record forever". Maintenance that deleted
// anything here would be deleting exactly what the operator asked to keep.
func TestEventsAutoPruneKeepsAnUnboundedJournalUnbounded(t *testing.T) {
	env := newTestEnv(t, "apu")
	ctx := context.Background()
	env.store.SetEventsJournalEnabled(true)
	createJournaledIssues(t, ctx, env, "apu", 4)

	deleted, err := eventsjournal.AutoPrune(ctx, env.store, eventsjournal.AutoPruneOptions{})
	if err != nil {
		t.Fatalf("auto-prune with both floors disabled: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("auto-prune deleted %d rows with both floors disabled, want 0", deleted)
	}
	rows, err := env.store.ReadEventsJournal(ctx, 0, 0)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("journal holds %d rows, want all 4", len(rows))
	}
}

// TestEventsAutoPruneThrottleSkipsTheSecondPass covers the persisted watermark
// end to end: the first pass stamps it, and a second pass inside the interval
// finds nothing due and deletes nothing — even though the floors say there is
// work to do.
func TestEventsAutoPruneThrottleSkipsTheSecondPass(t *testing.T) {
	env := newTestEnv(t, "apt")
	ctx := context.Background()
	env.store.SetEventsJournalEnabled(true)
	createJournaledIssues(t, ctx, env, "apt", 6)

	opts := eventsjournal.AutoPruneOptions{RetainRows: 2, Interval: time.Hour, VolumeRows: 1_000_000}
	if _, err := eventsjournal.AutoPrune(ctx, env.store, opts); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	createIssues(t, ctx, env, "aptb", 3)
	deleted, err := eventsjournal.AutoPrune(ctx, env.store, opts)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("second pass inside the interval deleted %d rows, want 0", deleted)
	}

	// Past the interval it is due again and applies the floor to the new head.
	late := opts
	late.Now = time.Now().UTC().Add(2 * time.Hour)
	deleted, err = eventsjournal.AutoPrune(ctx, env.store, late)
	if err != nil {
		t.Fatalf("third pass: %v", err)
	}
	if deleted == 0 {
		t.Fatal("a pass past the throttle interval deleted nothing")
	}
	var remaining int
	env.queryScalar(t, ctx, "SELECT COUNT(*) FROM bd_events_journal", nil, &remaining)
	if remaining != 2 {
		t.Fatalf("journal holds %d rows after the late pass, want the 2 the floor retains", remaining)
	}
}

// TestPruneEventsBatchDeletesTheLowestSeqsOnly is the engine-level proof for the
// one statement auto-prune executes. Dolt has to accept `DELETE ... ORDER BY
// ... LIMIT` (MySQL's single-table form), and it has to honor the ordering: a
// limited delete that picked an arbitrary subset would punch a hole into the
// middle of the retained window, which is the failure the whole feature is
// built to prevent.
func TestPruneEventsBatchDeletesTheLowestSeqsOnly(t *testing.T) {
	env := newTestEnv(t, "apb")
	ctx := context.Background()
	env.store.SetEventsJournalEnabled(true)
	seqs := createJournaledIssues(t, ctx, env, "apb", 6)

	var deleted int64
	if err := env.store.RunEventsMaintenanceTx(ctx, func(ctx context.Context, tx issueops.DBTX) error {
		var err error
		deleted, err = issueops.PruneEventsBatchInTx(ctx, tx, seqs[5], 2)
		return err
	}); err != nil {
		t.Fatalf("batch delete: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("batch deleted %d rows, want exactly the 2 it was limited to", deleted)
	}

	rows, err := env.store.ReadEventsJournal(ctx, seqs[1], 0)
	if err != nil {
		t.Fatalf("read after batch: %v", err)
	}
	if len(rows) != 4 || rows[0].Seq != seqs[2] {
		t.Fatalf("survivors = %+v, want the four rows from seq %d up — the batch must delete the LOWEST seqs", rows, seqs[2])
	}
}

// TestReadEventsAutoPruneStateResolvesBothHalvesInOneQuery pins the throttle
// read against the real engine: one statement, two scalar subqueries, and a
// missing slot row that reads as "never pruned here" rather than an error. It
// is the query every journaled mutation pays for, so its shape is worth a test
// of its own.
func TestReadEventsAutoPruneStateResolvesBothHalvesInOneQuery(t *testing.T) {
	env := newTestEnv(t, "aps")
	ctx := context.Background()
	env.store.SetEventsJournalEnabled(true)
	seqs := createJournaledIssues(t, ctx, env, "aps", 3)

	var (
		watermark string
		head      int64
	)
	read := func() {
		t.Helper()
		if err := env.store.RunEventsMaintenanceTx(ctx, func(ctx context.Context, tx issueops.DBTX) error {
			var err error
			watermark, head, err = issueops.ReadEventsAutoPruneStateInTx(ctx, tx, eventsjournal.AutoPruneSlotKey)
			return err
		}); err != nil {
			t.Fatalf("read auto-prune state: %v", err)
		}
	}

	read()
	if watermark != "" {
		t.Errorf("watermark = %q on a workspace that never pruned, want empty", watermark)
	}
	if head != seqs[len(seqs)-1] {
		t.Errorf("head = %d, want %d", head, seqs[len(seqs)-1])
	}

	if err := env.store.RunEventsMaintenanceTx(ctx, func(ctx context.Context, tx issueops.DBTX) error {
		return issueops.SetEventsAutoPruneStateInTx(ctx, tx, eventsjournal.AutoPruneSlotKey, `{"ts":"2026-01-01T00:00:00Z","head":2}`)
	}); err != nil {
		t.Fatalf("write watermark: %v", err)
	}
	read()
	if watermark == "" {
		t.Error("watermark did not survive a round trip through local_metadata")
	}
}
