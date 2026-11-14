package dolt

import (
	"context"
	"database/sql/driver"
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
			name: "DeleterDeleteFencedSingleConnection",
			setup: func(d *publicationOrderDriver, s *DoltStore) {
				d.rows = func(query string, args []driver.NamedValue) (driver.Rows, bool) {
					switch {
					case strings.Contains(query, "GET_LOCK("), strings.Contains(query, "RELEASE_LOCK("):
						return &valRows{cols: []string{"lock"}, vals: [][]driver.Value{{int64(1)}}}, true
					case strings.Contains(query, "SELECT row_lock FROM issues WHERE id"):
						return &valRows{cols: []string{"row_lock"}, vals: [][]driver.Value{{int64(1)}}}, true
					}
					return existingIssueRows(query, args)
				}
				s.serverMode = true
				// The post-commit publisher needs a pooled connection of its
				// own: it can only get one once the fence released the sole
				// connection the guarded delete pinned.
				s.db.SetMaxOpenConns(1)
			},
			run: func(t *testing.T, ctx context.Context, s *DoltStore) error {
				version := int64(1)
				return runRoleDelete(t, ctx, s, issueops.DeleteRequest{Actor: "alice", IDs: []string{"pub-1"}, Force: true, ExpectedVersion: &version})
			},
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
			name:  "SweeperSweep",
			setup: func(d *publicationOrderDriver, _ *DoltStore) { d.rows = sweepCandidateRows },
			run: func(t *testing.T, ctx context.Context, s *DoltStore) error {
				sw, err := s.Sweeper()
				if err != nil {
					return err
				}
				res, err := sw.Sweep(ctx, issueops.SweepRequest{Actor: "alice", Tier: issueops.SweepDurable, IDPattern: "*"})
				if err == nil && res.Swept != 1 {
					t.Errorf("Sweep result = %+v, want 1 swept", res)
				}
				return err
			},
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
