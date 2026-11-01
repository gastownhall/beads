//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// The arrival_seq tests on the embedded engine: the stamp the journal's create
// seam writes on a wisp row is the seq of its create record, so it inherits the
// counter's guarantees here exactly as on the SQL server
// (internal/storage/domain/db/journal_arrival_seq_test.go). This engine also
// reports a UNIQUE-index violation with its own error kind rather than a MySQL
// code, which is what the heal cases below drive: a stamp that collides with a
// surviving one must be recognized as a seq collision and healed.

func arrivalWisp(id string) *types.Issue {
	return &types.Issue{ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen, Ephemeral: true}
}

// arrivalStamps reads every wisp's stamp and every create record's seq over a
// short-lived raw connection.
func arrivalStamps(t *testing.T, env *testEnv) (stamps map[string]sql.NullInt64, records map[string]int64) {
	t.Helper()
	ctx := context.Background()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, env.dataDir, env.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	stamps = map[string]sql.NullInt64{}
	rows, err := db.QueryContext(ctx, "SELECT id, arrival_seq FROM wisps")
	if err != nil {
		t.Fatalf("query wisps: %v", err)
	}
	for rows.Next() {
		var id string
		var seq sql.NullInt64
		if err := rows.Scan(&id, &seq); err != nil {
			t.Fatalf("scan wisp: %v", err)
		}
		stamps[id] = seq
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("wisps rows: %v", err)
	}
	records = map[string]int64{}
	rows, err = db.QueryContext(ctx, "SELECT issue_id, seq FROM bd_events_journal WHERE op = 'create'")
	if err != nil {
		t.Fatalf("query journal: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var seq int64
		if err := rows.Scan(&id, &seq); err != nil {
			t.Fatalf("scan record: %v", err)
		}
		records[id] = seq
	}
	return stamps, records
}

// TestArrivalSeq_EmbeddedConcurrentWritersAreUnique races wisp creates through
// the real store path; the engine serializes them on the counter row, and every
// stamp must be distinct and equal to its own create record's seq.
func TestArrivalSeq_EmbeddedConcurrentWritersAreUnique(t *testing.T) {
	env := newTestEnv(t, "eas")
	env.store.SetEventsJournalEnabled(true)

	// The engine serializes these creates, so they share one budget: the test
	// binary's own deadline less a grace period to report a failure in, not a
	// fixed per-create timeout a slow race build can approach (12 creates ran
	// in about 30s under the farm's -race build).
	ctx := t.Context()
	if deadline, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-30*time.Second))
		defer cancel()
	}

	const writers = 12
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			errs[k] = env.store.CreateIssue(ctx, arrivalWisp(fmt.Sprintf("eas-race-%d", k)), "actor")
		}(i)
	}
	wg.Wait()
	for k, err := range errs {
		if err != nil {
			t.Fatalf("create %d: %v", k, err)
		}
	}
	stamps, records := arrivalStamps(t, env)
	seen := map[int64]string{}
	for i := range writers {
		id := fmt.Sprintf("eas-race-%d", i)
		stamp := stamps[id]
		if !stamp.Valid {
			t.Fatalf("%s is unstamped", id)
		}
		if prev, dup := seen[stamp.Int64]; dup {
			t.Fatalf("%s and %s share arrival_seq %d", prev, id, stamp.Int64)
		}
		seen[stamp.Int64] = id
		if stamp.Int64 != records[id] {
			t.Errorf("%s arrival_seq = %d, want its create record's seq %d", id, stamp.Int64, records[id])
		}
	}
}

// TestArrivalSeq_EmbeddedSequentialCreatesStampInCommitOrder is the
// two-writer half on this engine: a create that commits after another carries
// the higher stamp, each equal to its record.
func TestArrivalSeq_EmbeddedSequentialCreatesStampInCommitOrder(t *testing.T) {
	env := newTestEnv(t, "eas")
	env.store.SetEventsJournalEnabled(true)
	ctx := context.Background()
	for _, id := range []string{"eas-seq-b", "eas-seq-a"} {
		if err := env.store.CreateIssue(ctx, arrivalWisp(id), "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	stamps, records := arrivalStamps(t, env)
	first, second := stamps["eas-seq-b"], stamps["eas-seq-a"]
	if !first.Valid || !second.Valid || first.Int64 >= second.Int64 {
		t.Fatalf("stamps = %v then %v, want the first commit's lower", first, second)
	}
	if first.Int64 != records["eas-seq-b"] || second.Int64 != records["eas-seq-a"] {
		t.Errorf("stamps %d/%d differ from records %d/%d", first.Int64, second.Int64, records["eas-seq-b"], records["eas-seq-a"])
	}
}

// TestUniqueIndexAllowsManyUnstampedWisps pins that the unique index over
// arrival_seq admits any number of NULLs: every wisp created while the journal
// is off is unstamped, and they must all coexist.
func TestUniqueIndexAllowsManyUnstampedWisps(t *testing.T) {
	env := newTestEnv(t, "eas")
	env.store.SetEventsJournalEnabled(false)
	ctx := context.Background()
	for i := range 3 {
		if err := env.store.CreateIssue(ctx, arrivalWisp(fmt.Sprintf("eas-null-%d", i)), "actor"); err != nil {
			t.Fatalf("create unstamped wisp %d: %v", i, err)
		}
	}
	stamps, _ := arrivalStamps(t, env)
	for i := range 3 {
		if stamp := stamps[fmt.Sprintf("eas-null-%d", i)]; stamp.Valid {
			t.Errorf("wisp %d created with the journal off carries arrival_seq %d", i, stamp.Int64)
		}
	}
}

// TestArrivalSeq_EmbeddedHealRaisesTheCounterPastTheStamps winds the counter
// back below the surviving stamps with the journal empty — the state a prune
// plus a restored counter leaves. The stamp UPDATE collides on the unique
// index, which this engine reports with its own error kind; the create must
// heal past MAX(arrival_seq) and land just above it.
//
// The wind-back is deliberately deeper than one: wound back to high-1, the
// first draw is high, the counter's own value already sits there, and a heal
// that consulted only the (empty) journal would land on high+1 as well. Wound
// back three, a journal-only heal leaves the counter where it is and the retry
// collides with the next surviving stamp, so only a heal that reads
// MAX(arrival_seq) passes.
func TestArrivalSeq_EmbeddedHealRaisesTheCounterPastTheStamps(t *testing.T) {
	env := newTestEnv(t, "eas")
	env.store.SetEventsJournalEnabled(true)
	ctx := context.Background()
	for _, id := range []string{"eas-heal-a", "eas-heal-b", "eas-heal-c"} {
		if err := env.store.CreateIssue(ctx, arrivalWisp(id), "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	var high int64
	env.queryScalar(t, ctx, "SELECT MAX(arrival_seq) FROM wisps", nil, &high)
	env.exec(t, ctx, "DELETE FROM bd_events_journal")
	env.exec(t, ctx, "UPDATE bd_events_seq SET next_seq = ? WHERE id = 0", high-3)

	if err := env.store.CreateIssue(ctx, arrivalWisp("eas-heal-d"), "actor"); err != nil {
		t.Fatalf("a create whose first seq collides with a surviving stamp must heal, not wedge: %v", err)
	}
	stamps, records := arrivalStamps(t, env)
	if got := stamps["eas-heal-d"]; got.Int64 != high+1 || records["eas-heal-d"] != high+1 {
		t.Fatalf("healed create stamped %v with record %d, want both %d", got, records["eas-heal-d"], high+1)
	}
}

// TestArrivalSeq_EmbeddedJournalCollisionMovesTheStamp drives the other order:
// the stamp lands, then the journal INSERT collides with a surviving record.
// The retry must move the stamp with the record.
func TestArrivalSeq_EmbeddedJournalCollisionMovesTheStamp(t *testing.T) {
	env := newTestEnv(t, "eas")
	env.store.SetEventsJournalEnabled(true)
	ctx := context.Background()
	for _, id := range []string{"eas-move-a", "eas-move-b", "eas-move-c"} {
		durable := &types.Issue{ID: id, Title: "t", IssueType: types.TypeTask, Status: types.StatusOpen}
		if err := env.store.CreateIssue(ctx, durable, "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	var journalHigh int64
	env.queryScalar(t, ctx, "SELECT MAX(seq) FROM bd_events_journal", nil, &journalHigh)
	env.exec(t, ctx, "UPDATE bd_events_seq SET next_seq = ? WHERE id = 0", journalHigh-1)

	if err := env.store.CreateIssue(ctx, arrivalWisp("eas-move-wisp"), "actor"); err != nil {
		t.Fatalf("create after the counter was wound back: %v", err)
	}
	stamps, records := arrivalStamps(t, env)
	if got := stamps["eas-move-wisp"]; got.Int64 != journalHigh+1 || records["eas-move-wisp"] != journalHigh+1 {
		t.Fatalf("stamp %v / record %d, want both %d: the stamp must move with the retried record",
			got, records["eas-move-wisp"], journalHigh+1)
	}
}

// TestArrivalSeq_EmbeddedCreateLeavesTheStampOnTheCallersIssue pins the storage
// create APIs that write the stored row back onto the caller's struct
// (CreateIssue, CreateIssues and a transaction's CreateIssue, the calls the
// public beads.Storage exposes): once the create returns, the struct carries
// the stamp its row was given. PrepareIssueForInsert zeroes the field on the
// way in, so without the hand-back a caller that keeps the struct — the way a
// library consumer maps its create result — reads every stamped wisp as
// unstamped until it reads the row again. A durable create, and a wisp created
// with the journal off, stay at zero, matching their rows.
func TestArrivalSeq_EmbeddedCreateLeavesTheStampOnTheCallersIssue(t *testing.T) {
	env := newTestEnv(t, "eas")
	env.store.SetEventsJournalEnabled(true)
	ctx := context.Background()

	single := arrivalWisp("eas-hand-single")
	if err := env.store.CreateIssue(ctx, single, "actor"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	batch := []*types.Issue{arrivalWisp("eas-hand-batch-a"), arrivalWisp("eas-hand-batch-b")}
	if err := env.store.CreateIssues(ctx, batch, "actor"); err != nil {
		t.Fatalf("CreateIssues: %v", err)
	}
	inTx := arrivalWisp("eas-hand-tx")
	if err := env.store.RunInTransaction(ctx, "create in a transaction", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, inTx, "actor")
	}); err != nil {
		t.Fatalf("Transaction.CreateIssue: %v", err)
	}
	durable := &types.Issue{ID: "eas-hand-durable", Title: "t", IssueType: types.TypeTask, Status: types.StatusOpen}
	if err := env.store.CreateIssue(ctx, durable, "actor"); err != nil {
		t.Fatalf("CreateIssue (durable): %v", err)
	}
	env.store.SetEventsJournalEnabled(false)
	off := arrivalWisp("eas-hand-off")
	err := env.store.CreateIssue(ctx, off, "actor")
	env.store.SetEventsJournalEnabled(true)
	if err != nil {
		t.Fatalf("CreateIssue (journal off): %v", err)
	}

	stamps, _ := arrivalStamps(t, env)
	for _, issue := range []*types.Issue{single, batch[0], batch[1], inTx} {
		stamp := stamps[issue.ID]
		if !stamp.Valid {
			t.Fatalf("%s is unstamped", issue.ID)
		}
		if issue.ArrivalSeq != stamp.Int64 {
			t.Errorf("%s: the caller's issue carries arrival_seq %d after the create, its row %d", issue.ID, issue.ArrivalSeq, stamp.Int64)
		}
	}
	for _, issue := range []*types.Issue{durable, off} {
		if issue.ArrivalSeq != 0 {
			t.Errorf("%s: the caller's issue carries arrival_seq %d, but its row has none", issue.ID, issue.ArrivalSeq)
		}
	}
}

// TestArrivalSeq_EmbeddedReplayedCounterSeedClearsTheStamps covers the one way
// a counter can land below surviving stamps WITHOUT a collision to heal it.
// When bd_events_seq goes missing, the ignored series' cursor floor replays it
// from ignored/0022, which re-creates the counter and seeds it from the
// journal's MAX(seq) alone. With the journal pruned past every record that seed
// is 0, and the lowest stamps are free once their wisps are gone, so the next
// create would take one: no unique-index collision, no heal, and a new row that
// sorts BEFORE an older surviving one. ignored/0029 re-runs in the same replay
// and raises the counter past MAX(arrival_seq).
func TestArrivalSeq_EmbeddedReplayedCounterSeedClearsTheStamps(t *testing.T) {
	env := newTestEnv(t, "eas")
	env.store.SetEventsJournalEnabled(true)
	ctx := context.Background()
	for _, id := range []string{"eas-replay-a", "eas-replay-b", "eas-replay-c"} {
		if err := env.store.CreateIssue(ctx, arrivalWisp(id), "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// The two oldest go away, freeing the lowest stamps; the newest survives.
	for _, id := range []string{"eas-replay-a", "eas-replay-b"} {
		if err := env.store.DeleteIssue(ctx, id); err != nil {
			t.Fatalf("delete %s: %v", id, err)
		}
	}
	var survivor int64
	env.queryScalar(t, ctx, "SELECT arrival_seq FROM wisps WHERE id = ?", []any{"eas-replay-c"}, &survivor)
	if survivor <= 2 {
		t.Fatalf("surviving stamp = %d, want above the two deleted wisps' stamps", survivor)
	}
	env.exec(t, ctx, "DELETE FROM bd_events_journal")
	env.exec(t, ctx, "DROP TABLE bd_events_seq")

	// What the next open does: the missing counter floors the ignored cursor,
	// and the series replays from 0022 on.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, env.dataDir, env.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = cleanup()
		t.Fatalf("Conn: %v", err)
	}
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		_ = conn.Close()
		_ = cleanup()
		t.Fatalf("MigrateUp with the counter table missing: %v", err)
	}
	var next int64
	err = conn.QueryRowContext(ctx, "SELECT next_seq FROM bd_events_seq WHERE id = 0").Scan(&next)
	_ = conn.Close()
	_ = cleanup()
	if err != nil {
		t.Fatalf("reading the re-seeded counter: %v", err)
	}
	if next < survivor {
		t.Fatalf("the replay re-seeded the counter at %d, below the surviving stamp %d: the next create "+
			"would take a free value below it and sort before an older row", next, survivor)
	}

	if err := env.store.CreateIssue(ctx, arrivalWisp("eas-replay-d"), "actor"); err != nil {
		t.Fatalf("create after the replay: %v", err)
	}
	stamps, _ := arrivalStamps(t, env)
	if got := stamps["eas-replay-d"]; !got.Valid || got.Int64 <= survivor {
		t.Fatalf("the first wisp created after the replay stamped %v, want above the surviving %d", got, survivor)
	}
}
