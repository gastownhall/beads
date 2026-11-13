//go:build cgo

package embeddeddolt_test

import (
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// freezeDeferHistory is the database's Dolt history at one moment.
type freezeDeferHistory struct {
	head  string
	count int
}

// readFreezeDeferHistory reads the HEAD hash and commit count through a
// direct SQL handle. Callers close every store first, so the handle never
// shares the data directory with one.
func readFreezeDeferHistory(t *testing.T, beadsDir, database string) freezeDeferHistory {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("closing OpenSQL: %v", err)
		}
	}()
	var h freezeDeferHistory
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&h.count); err != nil {
		t.Fatalf("counting dolt_log: %v", err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT HASHOF('HEAD')").Scan(&h.head); err != nil {
		t.Fatalf("HASHOF('HEAD'): %v", err)
	}
	return h
}

// seedExpiredDefer creates a fresh embedded database holding one issue with an
// expired DATED defer (status='deferred', defer_until in the past: exactly
// what WakeExpiredDefersInTx looks for), committed so the working set is
// clean, and closes every handle it opened.
func seedExpiredDefer(t *testing.T, database, issueID string) (beadsDir string) {
	t.Helper()
	ctx := t.Context()
	beadsDir = filepath.Join(t.TempDir(), ".beads")

	bootstrap, err := embeddeddolt.Open(ctx, beadsDir, database, "main")
	if err != nil {
		t.Fatalf("Open (bootstrap): %v", err)
	}
	if err := bootstrap.SetConfig(ctx, "issue_prefix", database); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := bootstrap.CreateIssue(ctx, &types.Issue{
		ID: issueID, Title: "deferred issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap store: %v", err)
	}

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("closing OpenSQL: %v", err)
		}
	}()
	if _, err := db.ExecContext(ctx,
		`UPDATE issues SET status = 'deferred', defer_until = DATE_SUB(UTC_TIMESTAMP(), INTERVAL 1 MINUTE) WHERE id = ?`,
		issueID); err != nil {
		t.Fatalf("seed expired defer: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed expired defer')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	return beadsDir
}

// TestEmbeddedWakeExpiredDefersSkipsDuringMigrationFreeze pins GH#7634: a
// classified-read open (bd ready / bd list) opened with frozen=true must
// answer from current state without flipping an expired dated defer back to
// open. The sweep is a write, and dc-6jaq's contract is that a read may run
// during an active MIGRATION-FREEZE marker but no write may follow it. Every
// entry point that runs the sweep is checked, each on its own freshly seeded
// database, and each is followed by an independently reopened unfrozen store
// that DOES wake the defer: frozen disqualifies the sweep for that open, it
// does not disable the sweep outright.
func TestEmbeddedWakeExpiredDefersSkipsDuringMigrationFreeze(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	// Each reader returns how many ready issues it saw, or -1 when it
	// returns no listing (the advisory sweep).
	readers := []struct {
		name string
		read func(t *testing.T, s *embeddeddolt.EmbeddedDoltStore) (int, error)
	}{
		{"GetReadyWork", func(t *testing.T, s *embeddeddolt.EmbeddedDoltStore) (int, error) {
			issues, err := s.GetReadyWork(t.Context(), types.WorkFilter{})
			return len(issues), err
		}},
		{"GetReadyWorkWithCounts", func(t *testing.T, s *embeddeddolt.EmbeddedDoltStore) (int, error) {
			issues, err := s.GetReadyWorkWithCounts(t.Context(), types.WorkFilter{})
			return len(issues), err
		}},
		{"GetReadyWorkWithCountsAndTotal", func(t *testing.T, s *embeddeddolt.EmbeddedDoltStore) (int, error) {
			issues, total, err := s.GetReadyWorkWithCountsAndTotal(t.Context(), types.WorkFilter{})
			if err == nil && total != len(issues) {
				t.Errorf("GetReadyWorkWithCountsAndTotal: total=%d but %d issues listed", total, len(issues))
			}
			return len(issues), err
		}},
		{"CountReadyWork", func(t *testing.T, s *embeddeddolt.EmbeddedDoltStore) (int, error) {
			return s.CountReadyWork(t.Context(), types.WorkFilter{})
		}},
		{"WakeExpiredDefersAdvisory", func(t *testing.T, s *embeddeddolt.EmbeddedDoltStore) (int, error) {
			s.WakeExpiredDefersAdvisory(t.Context())
			return -1, nil
		}},
	}

	for _, r := range readers {
		t.Run(r.name, func(t *testing.T) {
			ctx := t.Context()
			const database = "fz"
			const issueID = database + "-1"
			beadsDir := seedExpiredDefer(t, database, issueID)

			// open opens a classified-read store, runs the reader, and returns
			// the issue's state; the store is closed before returning.
			open := func(frozen bool) (ready int, issue *types.Issue) {
				t.Helper()
				s, err := embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, database, "main", frozen)
				if err != nil {
					t.Fatalf("OpenForReadOnlyCommand(frozen=%v): %v", frozen, err)
				}
				defer func() {
					if err := s.Close(); err != nil {
						t.Fatalf("close store (frozen=%v): %v", frozen, err)
					}
				}()
				ready, err = r.read(t, s)
				if err != nil {
					t.Fatalf("%s (frozen=%v): %v", r.name, frozen, err)
				}
				issue, err = s.GetIssue(ctx, issueID)
				if err != nil {
					t.Fatalf("GetIssue (frozen=%v): %v", frozen, err)
				}
				return ready, issue
			}

			// A frozen classified-read open must not wake the defer, list
			// it, or touch Dolt history.
			before := readFreezeDeferHistory(t, beadsDir, database)
			ready, issue := open(true)
			frozenAfter := readFreezeDeferHistory(t, beadsDir, database)
			if issue.Status != types.StatusDeferred || issue.DeferUntil == nil {
				t.Errorf("a frozen classified-read open woke the defer: status=%q deferUntil=%v, want it to stay deferred", issue.Status, issue.DeferUntil)
			}
			if ready > 0 {
				t.Errorf("frozen %s saw %d ready issue(s), want 0: the expired defer must stay hidden", r.name, ready)
			}
			if frozenAfter != before {
				t.Errorf("frozen %s moved Dolt history: HEAD %s -> %s, commits %d -> %d",
					r.name, before.head, frozenAfter.head, before.count, frozenAfter.count)
			}
			deferUntil := issue.DeferUntil

			// The same sweep, unfrozen, must still wake it and record the
			// wake commit.
			ready, issue = open(false)
			after := readFreezeDeferHistory(t, beadsDir, database)
			if issue.Status != types.StatusOpen || issue.DeferUntil != nil {
				t.Errorf("an unfrozen classified-read open did not wake the defer (was until %v): status=%q deferUntil=%v",
					deferUntil, issue.Status, issue.DeferUntil)
			}
			if ready == 0 {
				t.Errorf("unfrozen %s saw no ready issues, want the woken defer", r.name)
			}
			if after.count != frozenAfter.count+1 || after.head == frozenAfter.head {
				t.Errorf("unfrozen %s: commits %d -> %d, HEAD %s -> %s; want exactly one wake commit",
					r.name, frozenAfter.count, after.count, frozenAfter.head, after.head)
			}
		})
	}
}
