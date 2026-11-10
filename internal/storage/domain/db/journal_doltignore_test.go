package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// TestEventsJournal_IsDoltIgnored asserts the journal table is registered in
// dolt_ignore (so it is node-local working-set state) and therefore never shows
// up in dolt_status — the signal bd's auto-commit path uses to decide what to
// stage/commit.
func (s *testSuite) TestEventsJournal_IsDoltIgnored() {
	s.journalEnabled = true
	defer func() { s.journalEnabled = false }()
	ctx := s.Ctx()

	var ignored bool
	err := s.Runner().QueryRowContext(ctx,
		"SELECT ignored FROM dolt_ignore WHERE pattern = 'bd_events_journal'").Scan(&ignored)
	s.Require().NoError(err, "bd_events_journal must be registered in dolt_ignore")
	s.True(ignored, "bd_events_journal must be ignored=true in dolt_ignore")

	// Write a journal row, then confirm the ignored table never surfaces in
	// dolt_status (which is what the auto-commit/add path consults).
	s.Require().NoError(s.issueRepo().Insert(ctx, newTestIssue("bd-di-1", "t"), "actor", domain.InsertIssueOpts{}))

	var statusCount int
	s.Require().NoError(s.Runner().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'bd_events_journal'").Scan(&statusCount))
	s.Equal(0, statusCount, "ignored journal table must not appear in dolt_status")
}

// TestEventsJournal_SurvivesCommitAndReset proves the journal rows are
// node-local: they survive a dolt commit cycle AND a `dolt reset --hard` (like
// git-untracked state), because the table is dolt-ignored. If reset destroyed
// them, the outbox design would be unsound — this is the guard for that.
func (s *testSuite) TestEventsJournal_SurvivesCommitAndReset() {
	s.journalEnabled = true
	defer func() { s.journalEnabled = false }()
	ctx := s.Ctx()

	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)
	s.Require().NoError(s.issueRepo().Insert(ctx, newTestIssue("bd-sv-1", "t"), "actor", domain.InsertIssueOpts{}))

	count := func() int {
		var n int
		s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT COUNT(*) FROM bd_events_journal").Scan(&n))
		return n
	}
	s.Require().Equal(1, count(), "one journal row after the create")

	// Commit the versioned tables. The ignored journal table is not staged, so
	// its rows stay only in the working set.
	_, err = s.Runner().ExecContext(ctx, "CALL DOLT_ADD('-A')")
	s.Require().NoError(err)
	_, err = s.Runner().ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--allow-empty')", "commit cycle for journal survival test")
	s.Require().NoError(err)
	s.Equal(1, count(), "journal row must survive a dolt commit cycle")

	// A hard reset restores versioned tables to HEAD but must leave the ignored
	// journal (working-set-only) intact.
	_, err = s.Runner().ExecContext(ctx, "CALL DOLT_RESET('--hard', 'HEAD')")
	s.Require().NoError(err)
	s.Equal(1, count(), "journal row must survive `dolt reset --hard` (ignored table is working-set state)")
}

// TestEventsJournal_TxAtomicity proves the journal insert and the issue
// mutation share the transaction even though the journal table is ignored: a
// rollback drops both, a commit keeps both.
func (s *testSuite) TestEventsJournal_TxAtomicity() {
	s.journalEnabled = true
	defer func() { s.journalEnabled = false }()
	ctx := s.Ctx()

	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)
	s.seedIssueRow("bd-atom-1")

	journalCount := func() int {
		var n int
		s.Require().NoError(s.Runner().QueryRowContext(ctx,
			"SELECT COUNT(*) FROM bd_events_journal WHERE issue_id = 'bd-atom-1'").Scan(&n))
		return n
	}
	statusOf := func() string {
		var st string
		s.Require().NoError(s.Runner().QueryRowContext(ctx,
			"SELECT status FROM issues WHERE id = 'bd-atom-1'").Scan(&st))
		return st
	}

	// Rollback: close the issue in a tx, then roll back — both the status change
	// and the journal row must vanish.
	tx, err := s.db.BeginTx(ctx, nil)
	s.Require().NoError(err)
	_, err = issueops.CloseIssueInTx(ctx, tx, "bd-atom-1", "done", "actor", "")
	s.Require().NoError(err)
	s.Require().NoError(tx.Rollback())
	s.Equal(0, journalCount(), "rolled-back journal row must not persist")
	s.Equal("open", statusOf(), "rolled-back close must not persist")

	// Commit: the same close, committed — both the status change and the journal
	// row must persist together.
	tx, err = s.db.BeginTx(ctx, nil)
	s.Require().NoError(err)
	_, err = issueops.CloseIssueInTx(ctx, tx, "bd-atom-1", "done", "actor", "")
	s.Require().NoError(err)
	s.Require().NoError(tx.Commit())
	s.Equal(1, journalCount(), "committed journal row must persist")
	s.Equal("closed", statusOf(), "committed close must persist")
}
