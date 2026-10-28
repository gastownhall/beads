package db

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// The arrival_seq tests on bd's Dolt sql-server: the stamp the journal's create
// seam writes on a wisp row (issueops.stampArrivalSeqInTx) is the seq of the
// row's create record, so it inherits the counter's commit-order guarantee —
// these drive exactly the overlapping and racing transactions
// journal_seq_test.go drives for the seq itself, on the wisp plane. wisps,
// bd_events_journal and bd_events_seq are all dolt_ignored, so DOLT_RESET
// clears none of them between tests: every id here is unique to its test.

// arrivalRow is a wisp row's stamp, read straight off the table.
func (s *testSuite) wispArrivalSeq(id string) int64 {
	var seq sql.NullInt64
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT arrival_seq FROM wisps WHERE id = ?", id).Scan(&seq))
	return seq.Int64
}

// createRecordSeq is the seq of the create record journaled for id.
func (s *testSuite) createRecordSeq(id string) int64 {
	for _, row := range s.readJournal() {
		if row.Op == "create" && row.IssueID == id {
			return row.Seq
		}
	}
	s.FailNowf("no create record", "no create record for %s", id)
	return 0
}

func newTestWisp(id string) *types.Issue {
	issue := newTestIssue(id, "wisp "+id)
	issue.Ephemeral = true
	return issue
}

// TestArrivalSeq_StampLeavesRevisionAndContentAlone pins that writing the
// stamp touches no other column ("Touches nothing else" in
// docs/reference/events-journal.md). row_lock is the caller's compare-and-set
// token, current_revision the version seam's, updated_at carries ON UPDATE
// CURRENT_TIMESTAMP on Dolt, and content_hash is the row's content identity —
// a create that moved any of them while journaling would hand its caller a
// revision that fails its very next guarded write.
func (s *testSuite) TestArrivalSeq_StampLeavesRevisionAndContentAlone() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	id := "bd-as6-stamp"

	tx, err := s.db.BeginTx(ctx, nil)
	s.Require().NoError(err)
	defer func() { _ = tx.Rollback() }()

	issue := newTestWisp(id)
	issue.ContentHash = issue.ComputeContentHash()
	normalizeIssueTimestamps(issue)
	// Back-date updated_at so an ON UPDATE CURRENT_TIMESTAMP rewrite could not
	// hide inside the same second.
	issue.UpdatedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	s.Require().NoError(insertIssueRow(ctx, tx, "wisps", issue))

	type cells struct {
		rowLock, currentRevision int64
		updatedAt, contentHash   string
		arrivalSeq               sql.NullInt64
	}
	read := func() cells {
		var c cells
		s.Require().NoError(tx.QueryRowContext(ctx,
			"SELECT row_lock, current_revision, CAST(updated_at AS CHAR), content_hash, arrival_seq FROM wisps WHERE id = ?", id).
			Scan(&c.rowLock, &c.currentRevision, &c.updatedAt, &c.contentHash, &c.arrivalSeq))
		return c
	}
	before := read()
	s.Require().False(before.arrivalSeq.Valid, "the insert itself must not stamp")

	s.Require().NoError(issueops.RecordEventInTx(ctx, tx, issueops.EventCreate, id, "actor"))
	after := read()
	s.Require().True(after.arrivalSeq.Valid, "the create record must stamp the wisp")
	s.Equal(before.rowLock, after.rowLock, "row_lock moved")
	s.Equal(before.currentRevision, after.currentRevision, "current_revision moved")
	s.Equal(before.updatedAt, after.updatedAt, "updated_at moved")
	s.Equal(before.contentHash, after.contentHash, "content_hash moved")
	s.Require().NoError(tx.Commit())

	s.Equal(s.createRecordSeq(id), after.arrivalSeq.Int64, "the stamp is the create record's seq")
}

// TestArrivalSeq_CommitOrderUnderOverlappingWriters is
// TestEventsJournal_CommitOrderedGaplessSeq on the wisp plane: two overlapping
// wisp creates on independent connections, A commits first, B loses the
// commit-time merge on the counter and retries. The stamps must order the way
// the commits did, and each must equal its own record's seq.
func (s *testSuite) TestArrivalSeq_CommitOrderUnderOverlappingWriters() {
	s.enableJournalForTest()
	ctx := s.Ctx()

	port := testutil.DoltContainerPortInt()
	s.Require().NotZero(port)
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Database: s.dbName}.String()
	dbB, err := sql.Open("mysql", dsn)
	s.Require().NoError(err)
	defer dbB.Close()

	insertOnTx := func(tx *sql.Tx, id string) error {
		return NewIssueSQLRepository(tx).Insert(ctx, newTestWisp(id), "actor", domain.InsertIssueOpts{UseWispsTable: true})
	}
	txA, err := s.db.BeginTx(ctx, nil)
	s.Require().NoError(err)
	txB, err := dbB.BeginTx(ctx, nil)
	s.Require().NoError(err)
	s.Require().NoError(insertOnTx(txA, "bd-as-ovl-a"), "insert A")
	s.Require().NoError(insertOnTx(txB, "bd-as-ovl-b"), "insert B")

	s.Require().NoError(txA.Commit(), "commit A")
	errB := txB.Commit()
	s.Require().Error(errB, "B must not commit a stamp drawn from the same counter base as A")
	s.Require().Truef(isSerializationFailure(errB), "B must fail with a serialization error, got: %v", errB)

	txB2, err := dbB.BeginTx(ctx, nil)
	s.Require().NoError(err)
	s.Require().NoError(insertOnTx(txB2, "bd-as-ovl-b"), "retry insert B")
	s.Require().NoError(txB2.Commit(), "commit retried B")

	a, b := s.wispArrivalSeq("bd-as-ovl-a"), s.wispArrivalSeq("bd-as-ovl-b")
	s.Less(a, b, "A committed first, so A's stamp is lower")
	s.Equal(s.createRecordSeq("bd-as-ovl-a"), a)
	s.Equal(s.createRecordSeq("bd-as-ovl-b"), b)
}

// TestArrivalSeq_ConcurrentWritersAreUnique races eight wisp creates and
// asserts every surviving stamp is distinct and equal to its record's seq.
func (s *testSuite) TestArrivalSeq_ConcurrentWritersAreUnique() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	port := testutil.DoltContainerPortInt()
	s.Require().NotZero(port)
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Database: s.dbName}.String()

	const writers = 8
	ids := make([]string, writers)
	for i := range ids {
		ids[i] = "bd-as-race-" + string(rune('a'+i))
	}
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			db, err := sql.Open("mysql", dsn)
			if err != nil {
				errCh <- err
				return
			}
			defer db.Close()
			for range 50 {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					errCh <- err
					return
				}
				if err := NewIssueSQLRepository(tx).Insert(ctx, newTestWisp(id), "actor", domain.InsertIssueOpts{UseWispsTable: true}); err != nil {
					_ = tx.Rollback()
					errCh <- err
					return
				}
				if err := tx.Commit(); err != nil {
					if isSerializationFailure(err) {
						continue
					}
					errCh <- err
					return
				}
				return
			}
			errCh <- errors.New("exceeded serialization retries")
		}(id)
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		s.Require().NoError(e)
	}
	seen := map[int64]string{}
	for _, id := range ids {
		got := s.wispArrivalSeq(id)
		s.NotZero(got, "%s unstamped", id)
		if prev, dup := seen[got]; dup {
			s.Failf("duplicate stamp", "%s and %s both carry arrival_seq %d", prev, id, got)
		}
		seen[got] = id
		s.Equal(s.createRecordSeq(id), got, "%s stamp must equal its record's seq", id)
	}
}

// TestArrivalSeq_HealRaisesTheCounterPastTheStamps is the stale-counter heal
// with the journal EMPTY: the stamps outlive their records once the journal is
// pruned, so a counter wound back collides with a stamp rather than with a
// record. The heal must raise the counter past MAX(arrival_seq), and the
// create must land at the next value.
//
// The wind-back is three, not one: wound back to high-1 the first draw is
// high, the counter's own value already sits there, and a heal that consulted
// only the (empty) journal would land on high+1 too. Wound back three, a
// journal-only heal leaves the counter in place and the retry collides with
// the next surviving stamp, so only a heal that reads MAX(arrival_seq) passes.
func (s *testSuite) TestArrivalSeq_HealRaisesTheCounterPastTheStamps() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	for _, id := range []string{"bd-as-heal-a", "bd-as-heal-b", "bd-as-heal-c"} {
		s.Require().NoError(s.issueRepo().Insert(ctx, newTestWisp(id), "actor", domain.InsertIssueOpts{UseWispsTable: true}))
	}
	var high int64
	s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT MAX(arrival_seq) FROM wisps").Scan(&high))
	s.clearJournal()
	// Wind the counter back so its next two draws are both surviving stamps.
	_, err := s.Runner().ExecContext(ctx, "UPDATE bd_events_seq SET next_seq = ? WHERE id = 0", high-3)
	s.Require().NoError(err)

	s.Require().NoError(s.issueRepo().Insert(ctx, newTestWisp("bd-as-heal-d"), "actor", domain.InsertIssueOpts{UseWispsTable: true}),
		"a create whose first seq collides with a surviving stamp must heal, not wedge")
	s.Equal(high+1, s.wispArrivalSeq("bd-as-heal-d"), "the healed create lands just past the highest stamp")
	s.Equal(high+1, s.createRecordSeq("bd-as-heal-d"))
}

// TestArrivalSeq_JournalCollisionMovesTheStamp drives the OTHER order of
// failure: the stamp lands, then the journal INSERT collides with a surviving
// record. The retry must move the stamp to the healed seq rather than leave
// the row carrying a value no record has.
func (s *testSuite) TestArrivalSeq_JournalCollisionMovesTheStamp() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	// wisps and bd_events_seq survive DOLT_RESET, so stamps an earlier test
	// left behind are still here. Lift the counter past every one of them
	// first, so the records below sit above all surviving stamps by
	// construction rather than by test order.
	_, err := s.Runner().ExecContext(ctx, `UPDATE bd_events_seq
		SET next_seq = GREATEST(next_seq, COALESCE((SELECT MAX(arrival_seq) FROM wisps), 0)) WHERE id = 0`)
	s.Require().NoError(err)
	// Durable creates: three records, no stamps.
	for _, id := range []string{"bd-as-move-a", "bd-as-move-b", "bd-as-move-c"} {
		s.Require().NoError(s.issueRepo().Insert(ctx, newTestIssue(id, "t"), "actor", domain.InsertIssueOpts{}))
	}
	var journalHigh, stampHigh sql.NullInt64
	s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT MAX(seq) FROM bd_events_journal").Scan(&journalHigh))
	s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT MAX(arrival_seq) FROM wisps").Scan(&stampHigh))
	// Reset the counter to a value no stamp holds but a record does.
	reset := journalHigh.Int64 - 1
	s.Require().Greater(reset, stampHigh.Int64, "setup: the reset must clear every stamp so the stamp itself lands")
	_, err = s.Runner().ExecContext(ctx, "UPDATE bd_events_seq SET next_seq = ? WHERE id = 0", reset)
	s.Require().NoError(err)

	id := "bd-as-move-wisp"
	s.Require().NoError(s.issueRepo().Insert(ctx, newTestWisp(id), "actor", domain.InsertIssueOpts{UseWispsTable: true}))
	got := s.wispArrivalSeq(id)
	s.Equal(journalHigh.Int64+1, got, "the retry lands past the journal's high-water mark")
	s.Equal(s.createRecordSeq(id), got, "the stamp moved with the record rather than staying at the collided seq")
}

// TestArrivalSeq_HealFallsBackWithoutTheWispColumn pins the drifted-clone
// fallback: on a database whose wisps table lacks arrival_seq, the heal cannot
// read MAX(arrival_seq) and must still raise the counter to the journal's
// high-water mark instead of failing every write.
func (s *testSuite) TestArrivalSeq_HealFallsBackWithoutTheWispColumn() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	s.Require().NoError(s.issueRepo().Insert(ctx, newTestIssue("bd-as-drift-a", "t"), "actor", domain.InsertIssueOpts{}))

	_, err := s.Runner().ExecContext(context.Background(), "ALTER TABLE wisps DROP COLUMN arrival_seq")
	s.Require().NoError(err)
	// wisps is dolt_ignored, so SetupTest's DOLT_RESET never restores the
	// column: a failed restore here would surface as unrelated failures in
	// every later test, so it is reported against this one.
	t := s.T()
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := s.db.ExecContext(ctx, "ALTER TABLE wisps ADD COLUMN arrival_seq BIGINT"); err != nil {
			t.Errorf("restoring wisps.arrival_seq for the rest of the suite: %v", err)
			return
		}
		if _, err := s.db.ExecContext(ctx, "CREATE UNIQUE INDEX uk_wisps_arrival_seq ON wisps (arrival_seq)"); err != nil {
			t.Errorf("restoring uk_wisps_arrival_seq for the rest of the suite: %v", err)
		}
	})
	var journalHigh int64
	s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT MAX(seq) FROM bd_events_journal").Scan(&journalHigh))
	// Wind the counter back so its next value collides with the one record.
	_, err = s.Runner().ExecContext(ctx, "UPDATE bd_events_seq SET next_seq = ? WHERE id = 0", journalHigh-1)
	s.Require().NoError(err)

	s.Require().NoError(s.issueRepo().Insert(ctx, newTestIssue("bd-as-drift-b", "t"), "actor", domain.InsertIssueOpts{}),
		"the heal must fall back to the journal alone when wisps has no arrival_seq")
	rows := s.readJournal()
	s.Require().Len(rows, 2)
	s.Equal(rows[0].Seq+1, rows[1].Seq)
}

// TestArrivalSeq_InsertLeavesTheStampOnTheCallersIssue pins the repository's
// half of the hand-back: Insert writes the stored row back onto the issue it
// was given (the content hash, the timestamps), and once it returns that issue
// carries the stamp its row was given — on both the upsert and the create-only
// path — rather than the zero Insert wrote on the way in. A durable insert
// stays at zero, matching its row.
func (s *testSuite) TestArrivalSeq_InsertLeavesTheStampOnTheCallersIssue() {
	s.enableJournalForTest()
	ctx := s.Ctx()

	upsert := newTestWisp("bd-as-hand-upsert")
	s.Require().NoError(s.issueRepo().Insert(ctx, upsert, "actor", domain.InsertIssueOpts{UseWispsTable: true}))
	createOnly := newTestWisp("bd-as-hand-create-only")
	s.Require().NoError(s.issueRepo().Insert(ctx, createOnly, "actor", domain.InsertIssueOpts{UseWispsTable: true, CreateOnly: true}))
	for _, issue := range []*types.Issue{upsert, createOnly} {
		row := s.wispArrivalSeq(issue.ID)
		s.NotZero(row, "%s unstamped", issue.ID)
		s.Equal(row, issue.ArrivalSeq, "%s: the caller's issue must carry its row's stamp once Insert returns", issue.ID)
	}

	durable := newTestIssue("bd-as-hand-durable", "t")
	s.Require().NoError(s.issueRepo().Insert(ctx, durable, "actor", domain.InsertIssueOpts{}))
	s.Zero(durable.ArrivalSeq, "a durable row carries no stamp, so neither does its caller's issue")
}
