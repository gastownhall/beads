package dolt

import (
	"context"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestLifecycleDeleteSweepPostTxPublication pins GH#5752's ordering contract
// over the writers that run on their own transaction runners rather than the
// issue-operation funnel: the lifecycle transaction, the legacy single and
// batch deletes, the role deleter (unguarded and fenced) and the role sweeper.
// Each publishes its DOLT_ADD/DOLT_COMMIT only after its SQL runner returned —
// after the SQL COMMIT, outside any open transaction — with the table set and
// message it always used, plus the versioned-history tables when that history
// is on.
func TestLifecycleDeleteSweepPostTxPublication(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(*publicationOrderDriver, *DoltStore)
		run      func(*testing.T, context.Context, *DoltStore) error
		mutation func(pubEvent) bool
		tables   []string
		message  string
		// after, when set, runs further checks over the recorded events.
		after func(*testing.T, []pubEvent)
	}{
		{
			name: "RunInIssueLifecycleTransaction",
			run: func(t *testing.T, ctx context.Context, s *DoltStore) error {
				calls := 0
				err := s.RunInIssueLifecycleTransaction(ctx, "bd: lifecycle pub-1", func(tx storage.IssueLifecycleTransaction) error {
					calls++
					dtx := tx.(*doltTransaction)
					if _, err := dtx.regularTx.ExecContext(ctx, "UPDATE issues SET title = ? WHERE id = ?", "t", "pub-1"); err != nil {
						return err
					}
					dtx.markIssuesChanged("issues")
					return nil
				})
				if calls != 1 {
					t.Errorf("lifecycle callback calls = %d, want 1", calls)
				}
				return err
			},
			mutation: writesTo("issues"),
			tables:   []string{"issues"},
			message:  "bd: lifecycle pub-1",
		},
		{
			name: "DeleteIssue",
			run: func(_ *testing.T, ctx context.Context, s *DoltStore) error {
				return s.DeleteIssue(ctx, "pub-1")
			},
			mutation: deletesFrom("issues"),
			tables:   legacyDeleteTables,
			message:  "bd: delete pub-1",
		},
		{
			name:  "DeleteIssues",
			setup: func(d *publicationOrderDriver, _ *DoltStore) { d.rows = existingIssueRows },
			run: func(t *testing.T, ctx context.Context, s *DoltStore) error {
				res, err := s.DeleteIssues(ctx, []string{"pub-1"}, false, true, false)
				if err == nil && (res == nil || res.DeletedCount != 1) {
					t.Errorf("DeleteIssues result = %+v, want 1 deleted", res)
				}
				return err
			},
			mutation: deletesFrom("issues"),
			tables:   legacyDeleteTables,
			message:  "bd: delete 1 issue(s)",
		},
		{
			name:  "DeleterDelete",
			setup: func(d *publicationOrderDriver, _ *DoltStore) { d.rows = existingIssueRows },
			run: func(t *testing.T, ctx context.Context, s *DoltStore) error {
				return runRoleDelete(t, ctx, s, issueops.DeleteRequest{Actor: "alice", IDs: []string{"pub-1"}, Force: true})
			},
			mutation: deletesFrom("issues"),
			tables:   sweptTables,
			message:  "bd: delete 1 issue(s)",
		},
		{
			name: "DeleterDeleteStagesVersionedHistory",
			setup: func(d *publicationOrderDriver, s *DoltStore) {
				d.rows = existingIssueRows
				s.versionedHistory.Set(true)
			},
			run: func(t *testing.T, ctx context.Context, s *DoltStore) error {
				return runRoleDelete(t, ctx, s, issueops.DeleteRequest{Actor: "alice", IDs: []string{"pub-1"}, Force: true})
			},
			mutation: deletesFrom("issues"),
			tables:   withTablesAdded(sweptTables, storeops.VersionedHistoryStagedTables()...),
			message:  "bd: delete 1 issue(s)",
		},
		{
			name:     "DeleterDeleteFencedSingleConnection",
			setup:    func(d *publicationOrderDriver, s *DoltStore) { fencedSingleConnectionSetup(d, s, existingIssueRows) },
			run:      runFencedRoleDelete,
			mutation: deletesFrom("issues"),
			tables:   sweptTables,
			message:  "bd: delete 1 issue(s)",
			after: func(t *testing.T, evs []pubEvent) {
				release := -1
				for i, ev := range evs {
					if strings.Contains(ev.query, "RELEASE_LOCK(") {
						release = i
					}
				}
				for i, ev := range evs {
					if (isDoltAdd(ev) || isDoltCommit(ev)) && i < release {
						t.Fatalf("publication at event %d ran before the fence's RELEASE_LOCK at %d; events:%s", i, release, formatPubEvents(evs))
					}
				}
				if release < 0 {
					t.Fatalf("fenced delete never released its lock; events:%s", formatPubEvents(evs))
				}
			},
		},
		{
			name:     "SweeperSweep",
			setup:    func(d *publicationOrderDriver, _ *DoltStore) { d.rows = sweepCandidateRows },
			run:      runDurableSweep,
			mutation: deletesFrom("issues"),
			tables:   sweptTables,
			message:  "bd: sweep 1 durable bead(s)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newPublicationOrderDriver()
			s := d.store()
			t.Cleanup(func() { _ = s.db.Close() })
			if tc.setup != nil {
				tc.setup(d, s)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := tc.run(t, ctx, s); err != nil {
				t.Fatalf("%s: %v; events:%s", tc.name, err, formatPubEvents(d.snapshot()))
			}
			evs := d.snapshot()
			pub := requirePublishedAfterCommit(t, tc.name, evs, tc.mutation)
			requirePublication(t, tc.name, pub, tc.tables, tc.message)
			if tc.after != nil {
				tc.after(t, evs)
			}
		})
	}
}

// TestLifecycleDeleteSweepSettleRecheckAfterPublication pins
// gastownhall/beads#7035 over the same writers: a write whose transaction
// recorded dependents for the post-commit blocked-state recheck settles them
// only after its own Dolt commit. The recheck stages the whole issues table
// when it changes a row, so a recheck settled first would publish the write's
// issues rows under its own message: for a delete, without the dependencies
// rows that named the deleted issue (TestCloseRecheckBlocked_* shows that on
// a real server). Here the recheck changes no row; the test pins that its
// transaction starts only after the write's DOLT_COMMIT. pub-dep blocks on
// what each delete and the sweep remove, so their bodies record it; the
// lifecycle callback records it as a close would. The fenced delete runs on a
// pool of one connection: a recheck settled under the fence could not get one.
func TestLifecycleDeleteSweepSettleRecheckAfterPublication(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(*publicationOrderDriver, *DoltStore)
		run      func(*testing.T, context.Context, *DoltStore) error
		mutation func(pubEvent) bool
		message  string
	}{
		{
			name: "RunInIssueLifecycleTransaction",
			run: func(t *testing.T, ctx context.Context, s *DoltStore) error {
				return s.RunInIssueLifecycleTransaction(ctx, "bd: lifecycle pub-1", func(tx storage.IssueLifecycleTransaction) error {
					dtx := tx.(*doltTransaction)
					if _, err := dtx.regularTx.ExecContext(ctx, "UPDATE issues SET status = ? WHERE id = ?", string(types.StatusClosed), "pub-1"); err != nil {
						return err
					}
					dtx.markIssuesChanged("issues")
					storeops.NoteStatusChangeBlockedRecheck(dtx.regularTx, "pub-1", string(types.StatusClosed), "alice", []string{"pub-dep"}, nil)
					return nil
				})
			},
			mutation: writesTo("issues"),
			message:  "bd: lifecycle pub-1",
		},
		{
			name:  "DeleteIssue",
			setup: func(d *publicationOrderDriver, _ *DoltStore) { d.rows = blockingDependerRows(nil) },
			run: func(_ *testing.T, ctx context.Context, s *DoltStore) error {
				return s.DeleteIssue(ctx, "pub-1")
			},
			mutation: deletesFrom("issues"),
			message:  "bd: delete pub-1",
		},
		{
			name:  "DeleteIssues",
			setup: func(d *publicationOrderDriver, _ *DoltStore) { d.rows = blockingDependerRows(existingIssueRows) },
			run: func(_ *testing.T, ctx context.Context, s *DoltStore) error {
				_, err := s.DeleteIssues(ctx, []string{"pub-1"}, false, true, false)
				return err
			},
			mutation: deletesFrom("issues"),
			message:  "bd: delete 1 issue(s)",
		},
		{
			name:  "DeleterDelete",
			setup: func(d *publicationOrderDriver, _ *DoltStore) { d.rows = blockingDependerRows(existingIssueRows) },
			run: func(t *testing.T, ctx context.Context, s *DoltStore) error {
				return runRoleDelete(t, ctx, s, issueops.DeleteRequest{Actor: "alice", IDs: []string{"pub-1"}, Force: true})
			},
			mutation: deletesFrom("issues"),
			message:  "bd: delete 1 issue(s)",
		},
		{
			name: "DeleterDeleteFencedSingleConnection",
			setup: func(d *publicationOrderDriver, s *DoltStore) {
				fencedSingleConnectionSetup(d, s, blockingDependerRows(existingIssueRows))
			},
			run:      runFencedRoleDelete,
			mutation: deletesFrom("issues"),
			message:  "bd: delete 1 issue(s)",
		},
		{
			name:     "SweeperSweep",
			setup:    func(d *publicationOrderDriver, _ *DoltStore) { d.rows = blockingDependerRows(sweepCandidateRows) },
			run:      runDurableSweep,
			mutation: deletesFrom("issues"),
			message:  "bd: sweep 1 durable bead(s)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newPublicationOrderDriver()
			s := d.store()
			t.Cleanup(func() { _ = s.db.Close() })
			if tc.setup != nil {
				tc.setup(d, s)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := tc.run(t, ctx, s); err != nil {
				t.Fatalf("%s: %v; events:%s", tc.name, err, formatPubEvents(d.snapshot()))
			}
			evs := d.snapshot()
			commitIdx := mutationCommitIndex(evs, tc.mutation)
			if commitIdx < 0 {
				t.Fatalf("no committed SQL transaction carried the mutation; events:%s", formatPubEvents(evs))
			}
			published, recheck := -1, -1
			for i, ev := range evs[commitIdx+1:] {
				switch {
				case published < 0 && isDoltCommit(ev) && len(ev.args) > 0 && ev.args[0] == tc.message:
					published = commitIdx + 1 + i
				case recheck < 0 && ev.inTx && strings.Contains(ev.query, "is_blocked"):
					recheck = commitIdx + 1 + i
				}
			}
			if published < 0 {
				t.Fatalf("no DOLT_COMMIT %q after the mutation's SQL COMMIT at %d; events:%s", tc.message, commitIdx, formatPubEvents(evs))
			}
			if recheck < 0 {
				t.Fatalf("no blocked-state recheck ran after the mutation's SQL COMMIT at %d: the write recorded no dependent, "+
					"or the recheck never got a connection; events:%s", commitIdx, formatPubEvents(evs))
			}
			if recheck < published {
				t.Fatalf("the blocked-state recheck ran at event %d, before the write's own DOLT_COMMIT at %d; events:%s",
					recheck, published, formatPubEvents(evs))
			}
		})
	}
}

// blockingDependerRows makes every blocking-depender read of a deletion find
// pub-dep, so the delete records it for the post-commit recheck; any other
// query goes to next, when set.
func blockingDependerRows(next func(string, []driver.NamedValue) (driver.Rows, bool)) func(string, []driver.NamedValue) (driver.Rows, bool) {
	return func(query string, args []driver.NamedValue) (driver.Rows, bool) {
		if strings.Contains(query, "SELECT issue_id FROM dependencies") && strings.Contains(query, "type = 'blocks'") {
			return &valRows{cols: []string{"issue_id"}, vals: [][]driver.Value{{"pub-dep"}}}, true
		}
		if next == nil {
			return nil, false
		}
		return next(query, args)
	}
}

// legacyDeleteTables is the table list DeleteIssue/DeleteIssues stage.
var legacyDeleteTables = []string{
	"issues", "dependencies", "labels", "comments", "events", "provenance_events",
	"child_counters", "issue_snapshots", "compaction_snapshots",
}

// withTablesAdded is tables plus each extra it does not already hold, the way
// withVersionedHistoryTables appends the history tables to a staging list.
func withTablesAdded(tables []string, extra ...string) []string {
	out := append([]string{}, tables...)
	for _, table := range extra {
		if !slices.Contains(out, table) {
			out = append(out, table)
		}
	}
	return out
}

// fencedSingleConnectionSetup makes the role deleter take its fenced path on
// a pool of one connection: the fence's GET_LOCK and RELEASE_LOCK succeed and
// the version probe reads 1, and every other query goes to next. Whatever runs
// after the write — the post-commit publisher, the blocked-state recheck —
// can only get a connection once the fence released the sole one the guarded
// delete pinned.
func fencedSingleConnectionSetup(d *publicationOrderDriver, s *DoltStore, next func(string, []driver.NamedValue) (driver.Rows, bool)) {
	d.rows = func(query string, args []driver.NamedValue) (driver.Rows, bool) {
		switch {
		case strings.Contains(query, "GET_LOCK("), strings.Contains(query, "RELEASE_LOCK("):
			return &valRows{cols: []string{"lock"}, vals: [][]driver.Value{{int64(1)}}}, true
		case strings.Contains(query, "SELECT row_lock FROM issues WHERE id"):
			return &valRows{cols: []string{"row_lock"}, vals: [][]driver.Value{{int64(1)}}}, true
		}
		return next(query, args)
	}
	s.serverMode = true
	s.db.SetMaxOpenConns(1)
}

func runFencedRoleDelete(t *testing.T, ctx context.Context, s *DoltStore) error {
	version := int64(1)
	return runRoleDelete(t, ctx, s, issueops.DeleteRequest{Actor: "alice", IDs: []string{"pub-1"}, Force: true, ExpectedVersion: &version})
}

func runRoleDelete(t *testing.T, ctx context.Context, s *DoltStore, req issueops.DeleteRequest) error {
	t.Helper()
	del, err := s.Deleter()
	if err != nil {
		return err
	}
	res, err := del.Delete(ctx, req)
	if err == nil && res.Deleted != 1 {
		t.Errorf("Delete result = %+v, want 1 deleted", res)
	}
	return err
}

// deletesFrom matches a statement that deletes rows of the given table.
func deletesFrom(table string) func(pubEvent) bool {
	return func(ev pubEvent) bool {
		q := strings.ToUpper(strings.Join(strings.Fields(ev.query), " "))
		return strings.Contains(q, "DELETE FROM "+strings.ToUpper(table)+" ")
	}
}

// existingIssueRows makes the deletion paths' existence probes find pub-1.
func existingIssueRows(query string, _ []driver.NamedValue) (driver.Rows, bool) {
	if strings.Contains(query, "SELECT id FROM issues WHERE id IN") {
		return &valRows{cols: []string{"id"}, vals: [][]driver.Value{{"pub-1"}}}, true
	}
	return nil, false
}

func runDurableSweep(t *testing.T, ctx context.Context, s *DoltStore) error {
	sw, err := s.Sweeper()
	if err != nil {
		return err
	}
	res, err := sw.Sweep(ctx, issueops.SweepRequest{Actor: "alice", Tier: issueops.SweepDurable, IDPattern: "*"})
	if err == nil && res.Swept != 1 {
		t.Errorf("Sweep result = %+v, want 1 swept", res)
	}
	return err
}

// sweepCandidateRows makes a durable sweep's candidate query find one closed
// issue, pub-1.
func sweepCandidateRows(query string, args []driver.NamedValue) (driver.Rows, bool) {
	if !strings.Contains(query, "FROM issues") || len(args) == 0 || args[0].Value != string(types.StatusClosed) {
		return nil, false
	}
	values := claimBoundaryIssueValues("pub-1")
	for i, column := range claimBoundaryIssueColumns() {
		switch column {
		case "status":
			values[i] = string(types.StatusClosed)
		case "closed_at":
			values[i] = time.Now().Add(-48 * time.Hour)
		}
	}
	return &valRows{cols: claimBoundaryIssueColumns(), vals: [][]driver.Value{values}}, true
}

// TestLifecycleCallbackRunsOnceAndMintsNoDoltCommitOnAmbiguousCommit pins the
// lifecycle half of GH#5752's failure contract: when the SQL COMMIT's response
// is lost, RunInIssueLifecycleTransaction reports ErrCommitIndeterminate, never
// replays the callback, and mints no Dolt publication off the ambiguous commit.
func TestLifecycleCallbackRunsOnceAndMintsNoDoltCommitOnAmbiguousCommit(t *testing.T) {
	d := newPublicationOrderDriver()
	d.commitErr = func(int) error { return testConnectionLoss }
	s := d.store()
	defer func() { _ = s.db.Close() }()

	ctx := context.Background()
	calls := 0
	err := s.RunInIssueLifecycleTransaction(ctx, "bd: lifecycle pub-1", func(tx storage.IssueLifecycleTransaction) error {
		calls++
		dtx := tx.(*doltTransaction)
		if _, err := dtx.regularTx.ExecContext(ctx, "UPDATE issues SET title = ? WHERE id = ?", "t", "pub-1"); err != nil {
			return err
		}
		dtx.markIssuesChanged("issues")
		return nil
	})
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("RunInIssueLifecycleTransaction error = %v, want ErrCommitIndeterminate", err)
	}
	if calls != 1 {
		t.Fatalf("callback ran %d times, want exactly 1 (never replayed)", calls)
	}
	for _, ev := range d.snapshot() {
		if isDoltAdd(ev) || isDoltCommit(ev) {
			t.Fatalf("no Dolt publication may be minted off an ambiguous SQL commit; events:%s", formatPubEvents(d.snapshot()))
		}
	}
}
