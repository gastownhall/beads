package db

import (
	"database/sql"
	"errors"
	"sync"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/testutil"
)

// isSerializationFailure reports whether err is a Dolt/MySQL serialization
// failure (1213 deadlock / 1205 lock-wait-timeout). Dolt has no blocking row
// locks in server mode; concurrent writers to the same cell lose the
// commit-time merge with one of these codes and the loser is rolled back.
func isSerializationFailure(err error) bool {
	var myErr *gomysql.MySQLError
	if errors.As(err, &myErr) {
		return myErr.Number == 1213 || myErr.Number == 1205
	}
	return false
}

// TestEventsJournal_CommitOrderedGaplessSeq is the FINDING 1 proof: under two
// OVERLAPPING transactions, the journal seq order equals commit-visibility order
// with no gaps, on bd's actual (deferred-commit, optimistic-merge) Dolt server
// model.
//
// The seq is drawn from the single-row bd_events_seq counter inside each
// mutation's own transaction. The two inserts touch DIFFERENT issue rows and
// DIFFERENT event rows — the ONLY cell they share is the counter row — so any
// serialization here is caused solely by the counter. That is exactly the point:
// with the former AUTO_INCREMENT seq the two commits would NOT conflict (distinct
// PKs assigned at insert), so commit order could invert seq order and a tailing
// consumer would skip the lower seq. The counter converts that silent skip into
// a detected conflict + retry that preserves commit order.
func (s *testSuite) TestEventsJournal_CommitOrderedGaplessSeq() {
	s.journalEnabled = true
	s.T().Cleanup(func() { s.journalEnabled = false })
	ctx := s.Ctx()
	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)

	port := testutil.DoltContainerPortInt()
	s.Require().NotZero(port)
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Database: s.dbName}.String()
	dbB, err := sql.Open("mysql", dsn)
	s.Require().NoError(err)
	defer dbB.Close()

	insertOnTx := func(tx *sql.Tx, id string) error {
		ir := NewIssueSQLRepository(tx)
		return ir.Insert(ctx, newTestIssue(id, "t"), "actor", domain.InsertIssueOpts{})
	}

	// Two overlapping transactions on independent connections. Both allocate a
	// seq from the counter BEFORE either commits.
	txA, err := s.db.BeginTx(ctx, nil)
	s.Require().NoError(err)
	txB, err := dbB.BeginTx(ctx, nil)
	s.Require().NoError(err)

	s.Require().NoError(insertOnTx(txA, "bd-seq-a"), "insert A")
	s.Require().NoError(insertOnTx(txB, "bd-seq-b"), "insert B")

	// Commit A first; it wins.
	s.Require().NoError(txA.Commit(), "commit A")
	// B committed second and both bumped the same counter row from the same base,
	// so B must lose the commit-time merge (rolled back server-side) rather than
	// silently committing an out-of-order/duplicate seq.
	errB := txB.Commit()
	s.Require().Error(errB, "B must not commit an inverted/duplicate seq")
	s.Require().Truef(isSerializationFailure(errB),
		"B must fail with a serialization error (guaranteed rollback), got: %v", errB)

	// Retry B in a fresh transaction (this is what the real write paths —
	// withRetryTx / uow.RunTx — do). It re-reads the committed counter and gets a
	// strictly greater seq.
	txB2, err := dbB.BeginTx(ctx, nil)
	s.Require().NoError(err)
	s.Require().NoError(insertOnTx(txB2, "bd-seq-b"), "retry insert B")
	s.Require().NoError(txB2.Commit(), "commit retried B")

	got := s.readJournal()
	s.Require().Len(got, 2, "exactly two journal rows survive: %+v", got)
	// seq order == commit order: A (committed first) < B (committed second).
	s.Equal("bd-seq-a", got[0].IssueID, "first seq is the first committer")
	s.Equal("bd-seq-b", got[1].IssueID, "second seq is the second committer")
	// Gapless: the two surviving seqs are consecutive — B's rolled-back attempt
	// burned no seq.
	s.Equalf(got[0].Seq+1, got[1].Seq, "seqs must be gapless: %d then %d", got[0].Seq, got[1].Seq)
}

// TestEventsJournal_ConcurrentWritersGaplessNoDup drives many real journaled
// mutations through independent connections at once and asserts the journal ends
// up with exactly one gapless, duplicate-free seq per committed mutation. It
// exercises the end-to-end safety property (a committed seq=N implies every
// seq<N is already committed) through concurrent writers, each retrying its own
// serialization losses — mirroring what withRetryTx / uow.RunTx do in production.
func (s *testSuite) TestEventsJournal_ConcurrentWritersGaplessNoDup() {
	s.journalEnabled = true
	s.T().Cleanup(func() { s.journalEnabled = false })
	ctx := s.Ctx()
	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)

	port := testutil.DoltContainerPortInt()
	s.Require().NotZero(port)
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Database: s.dbName}.String()

	const writers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			db, err := sql.Open("mysql", dsn)
			if err != nil {
				errCh <- err
				return
			}
			defer db.Close()
			id := "bd-cw-" + string(rune('a'+k))
			// Retry the whole unit of work on serialization failure, exactly like
			// the production write paths.
			for attempt := 0; attempt < 50; attempt++ {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					errCh <- err
					return
				}
				ir := NewIssueSQLRepository(tx)
				if err := ir.Insert(ctx, newTestIssue(id, "t"), "actor", domain.InsertIssueOpts{}); err != nil {
					_ = tx.Rollback()
					errCh <- err
					return
				}
				if err := tx.Commit(); err != nil {
					if isSerializationFailure(err) {
						continue // rolled back server-side; redo the whole unit of work
					}
					errCh <- err
					return
				}
				return
			}
			errCh <- errors.New("exceeded serialization retries")
		}(i)
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		s.Require().NoError(e)
	}

	got := s.readJournal()
	s.Require().Len(got, writers, "one journal row per committed mutation")
	seen := map[int64]bool{}
	for i, r := range got {
		s.Falsef(seen[r.Seq], "duplicate seq %d", r.Seq)
		seen[r.Seq] = true
		if i > 0 {
			// Contiguous: gapless across the whole concurrent batch.
			s.Equalf(got[i-1].Seq+1, r.Seq, "seqs must be gapless and ordered: %d then %d", got[i-1].Seq, r.Seq)
		}
	}
}
