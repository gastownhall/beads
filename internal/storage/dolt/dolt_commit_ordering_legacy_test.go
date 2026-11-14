package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Ordering contracts for the LEGACY issue-mutation paths GH#5752 migrates to
// the post-tx publisher: update (plain and checked), claim, ready claim,
// unclaim (both forms), lease reclaim, reopen, and close (both forms) must run
// their DOLT_ADD/DOLT_COMMIT strictly AFTER the driver-level COMMIT of the SQL
// transaction that applied the mutation — never inside it. Every case drives a
// real public method over the sequence recorder, so the assertion is about the
// method, not a body that resembles it.

// legacyMutationCase is one migrated method's fixture and expectations.
type legacyMutationCase struct {
	name   string
	wire   func(c *seqConnector)
	run    func(s *DoltStore) error
	tables []string // staged table SET (order-insensitive)
	msg    string   // commit message the case must publish
}

// serveLegacyIssueRow serves GetIssueInTx's issues LEFT JOIN leases read for id,
// overriding columns (e.g. assignee, status) from overrides.
func serveLegacyIssueRow(c *seqConnector, id string, overrides map[string]driver.Value) {
	prev := c.rows
	c.rows = func(q string) (driver.Rows, bool) {
		if strings.Contains(q, "FROM issues") && strings.Contains(q, "LEFT JOIN leases") && strings.Contains(q, "WHERE id = ?") {
			vals := claimBoundaryIssueValues(id)
			for i, col := range claimBoundaryIssueColumns() {
				if v, ok := overrides[col]; ok {
					vals[i] = v
				}
			}
			return &valRows{cols: claimBoundaryIssueColumns(), vals: [][]driver.Value{vals}}, true
		}
		if prev != nil {
			return prev(q)
		}
		return nil, false
	}
}

// legacyIssueEvents is the canned-rows wiring every legacy mutation needs: the
// wisp probe reports no wisp (durable plane), and the derived is_blocked
// fixpoint reports no change so it converges.
func legacyIssueEvents(c *seqConnector) {
	c.execRows = func(query string) (int64, bool) {
		if strings.Contains(query, "is_blocked") {
			return 0, true
		}
		return 0, false
	}
}

func legacyMutationCases() []legacyMutationCase {
	return []legacyMutationCase{
		{
			name: "update issue",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				serveLegacyIssueRow(c, "legacy-1", nil)
			},
			run: func(s *DoltStore) error {
				return s.UpdateIssue(context.Background(), "legacy-1", map[string]interface{}{"title": "changed"}, "alice")
			},
			tables: []string{"issues", "events"},
			msg:    "bd: update legacy-1",
		},
		{
			name: "checked update",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				serveLegacyIssueRow(c, "legacy-1", nil)
				c.rows = func(q string) (driver.Rows, bool) {
					if strings.Contains(q, "SELECT assignee, status FROM issues WHERE id = ?") {
						return &valRows{cols: []string{"assignee", "status"}, vals: [][]driver.Value{{nil, string(types.StatusOpen)}}}, true
					}
					if strings.Contains(q, "FROM issues") && strings.Contains(q, "LEFT JOIN leases") {
						return &valRows{cols: claimBoundaryIssueColumns(), vals: [][]driver.Value{claimBoundaryIssueValues("legacy-1")}}, true
					}
					return nil, false
				}
			},
			run: func(s *DoltStore) error {
				expected := string(types.StatusOpen)
				return s.UpdateIssueChecked(context.Background(), "legacy-1", map[string]interface{}{"title": "changed"}, "alice",
					storage.UpdateIssueOptions{ExpectedStatus: &expected})
			},
			tables: []string{"issues", "events"},
			msg:    "bd: update legacy-1",
		},
		{
			name: "claim",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				serveLegacyIssueRow(c, "legacy-1", nil)
			},
			run: func(s *DoltStore) error {
				return s.ClaimIssue(context.Background(), "legacy-1", "alice")
			},
			tables: []string{"issues", "events"},
			msg:    "bd: claim legacy-1",
		},
		{
			name: "ready claim",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				serveLegacyIssueRow(c, "legacy-1", nil)
				c.rows = func(q string) (driver.Rows, bool) {
					if strings.Contains(q, "SELECT id FROM issues") {
						return &valRows{cols: []string{"id"}, vals: [][]driver.Value{{"legacy-1"}}}, true
					}
					if strings.Contains(q, "FROM issues") && strings.Contains(q, "LEFT JOIN leases") {
						return &valRows{cols: claimBoundaryIssueColumns(), vals: [][]driver.Value{claimBoundaryIssueValues("legacy-1")}}, true
					}
					return nil, false
				}
			},
			run: func(s *DoltStore) error {
				_, err := s.ClaimReadyIssue(context.Background(), types.WorkFilter{}, "alice")
				return err
			},
			tables: []string{"issues", "events"},
			msg:    "bd: claim ready legacy-1",
		},
		{
			name: "unclaim",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				serveLegacyIssueRow(c, "legacy-1", map[string]driver.Value{
					"assignee": "alice", "status": string(types.StatusInProgress),
				})
			},
			run: func(s *DoltStore) error {
				return s.UnclaimIssue(context.Background(), "legacy-1", "alice", true)
			},
			tables: []string{"issues", "events"},
			msg:    "bd: unclaim legacy-1",
		},
		{
			name: "unclaim if assignee",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				serveLegacyIssueRow(c, "legacy-1", map[string]driver.Value{
					"assignee": "alice", "status": string(types.StatusInProgress),
				})
			},
			run: func(s *DoltStore) error {
				return s.UnclaimIssueIfAssignee(context.Background(), "legacy-1", "alice", "alice")
			},
			tables: []string{"issues", "events"},
			msg:    "bd: unclaim legacy-1",
		},
		{
			name: "reclaim expired leases",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				c.rows = func(q string) (driver.Rows, bool) {
					if strings.Contains(q, "FROM leases l") && strings.Contains(q, "JOIN issues i") {
						return &valRows{cols: []string{"issue_id", "assignee", "granted_node"},
							vals: [][]driver.Value{{"legacy-1", "bob", "node-1"}}}, true
					}
					return nil, false
				}
			},
			run: func(s *DoltStore) error {
				_, err := s.ReclaimExpiredLeases(context.Background(), time.Hour, types.ReclaimFilter{}, "reaper")
				return err
			},
			tables: []string{"issues", "events"},
			msg:    "bd: reclaim 1 expired lease(s)",
		},
		{
			name: "close",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				serveLegacyIssueRow(c, "legacy-1", nil)
			},
			run: func(s *DoltStore) error {
				return s.CloseIssue(context.Background(), "legacy-1", "done", "alice", "sess")
			},
			tables: []string{"issues", "events"},
			msg:    "bd: close legacy-1",
		},
		{
			name: "checked close",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				serveLegacyIssueRow(c, "legacy-1", nil)
				c.rows = func(q string) (driver.Rows, bool) {
					if strings.Contains(q, "SELECT status FROM issues WHERE id = ?") {
						return &valRows{cols: []string{"status"}, vals: [][]driver.Value{{string(types.StatusOpen)}}}, true
					}
					if strings.Contains(q, "SELECT is_template, pinned, status, assignee FROM issues WHERE id = ?") {
						return &valRows{cols: []string{"is_template", "pinned", "status", "assignee"},
							vals: [][]driver.Value{{false, false, string(types.StatusOpen), nil}}}, true
					}
					if strings.Contains(q, "SELECT COUNT(DISTINCT dependency.issue_id)") {
						// No open children (durable or wisp): the close is not blocked.
						return &valRows{cols: []string{"count"}, vals: [][]driver.Value{{int64(0)}}}, true
					}
					if strings.Contains(q, "FROM issues") && strings.Contains(q, "LEFT JOIN leases") {
						return &valRows{cols: claimBoundaryIssueColumns(), vals: [][]driver.Value{claimBoundaryIssueValues("legacy-1")}}, true
					}
					return nil, false
				}
			},
			run: func(s *DoltStore) error {
				_, err := s.CloseIssueChecked(context.Background(), "legacy-1", "alice", storage.CloseIssueOptions{Reason: "done"})
				return err
			},
			tables: []string{"issues", "events"},
			msg:    "bd: close legacy-1",
		},
	}
}

func TestLegacyIssueMutationsDoltCommitRunsAfterTxCommit(t *testing.T) {
	for _, tc := range legacyMutationCases() {
		t.Run(tc.name, func(t *testing.T) {
			rec := &seqRecorder{}
			connector := &seqConnector{rec: rec}
			tc.wire(connector)
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()

			s := &DoltStore{db: db}
			if err := tc.run(s); err != nil {
				t.Fatalf("%s: %v; events: %v", tc.name, err, rec.snapshot())
			}

			assertPostTxPublication(t, rec.snapshot(), tc.tables, tc.msg)
		})
	}
}

// assertPostTxPublication checks the GH#5752 ordering contract on a recorded
// event log: the DOLT_ADD/DOLT_COMMIT pair runs after the LAST driver-level
// data COMMIT, on a connection with no open transaction, staging exactly
// wantTables, with a message containing wantMsg.
func assertPostTxPublication(t *testing.T, events []string, wantTables []string, wantMsg string) {
	t.Helper()
	idx := func(pred func(string) bool) int {
		for i, ev := range events {
			if pred(ev) {
				return i
			}
		}
		return -1
	}
	doltAddIdx := idx(func(ev string) bool { return strings.Contains(ev, "DOLT_ADD") })
	doltCommitIdx := idx(func(ev string) bool { return strings.Contains(ev, "DOLT_COMMIT") })
	if doltAddIdx == -1 || doltCommitIdx == -1 {
		t.Fatalf("DOLT_ADD/DOLT_COMMIT not recorded; events: %v", events)
	}
	commitIdx := -1
	for i, ev := range events {
		if ev == "COMMIT" {
			commitIdx = i
		}
	}
	if commitIdx == -1 {
		t.Fatalf("no driver-level tx COMMIT recorded; events: %v", events)
	}
	if doltAddIdx < commitIdx {
		t.Errorf("DOLT_ADD ran INSIDE the open transaction (index %d < COMMIT index %d) — lost-update ordering hazard; events: %v",
			doltAddIdx, commitIdx, events)
	}
	if doltCommitIdx < commitIdx {
		t.Errorf("DOLT_COMMIT ran INSIDE the open transaction (index %d < COMMIT index %d) — lost-update ordering hazard; events: %v",
			doltCommitIdx, commitIdx, events)
	}
	for i := commitIdx + 1; i < doltCommitIdx; i++ {
		if events[i] == "BEGIN" {
			t.Errorf("unexpected BEGIN between tx COMMIT and DOLT_COMMIT at index %d; events: %v", i, events)
		}
	}

	// The staged table set must be exactly the site's fixed list (the
	// versioned-history tables are appended only when the switch is on).
	var gotTables []string
	for _, ev := range events {
		if !strings.Contains(ev, "DOLT_ADD") {
			continue
		}
		parts := strings.Fields(ev)
		if len(parts) < 2 {
			t.Fatalf("DOLT_ADD event carries no table argument: %q", ev)
		}
		gotTables = append(gotTables, strings.Trim(parts[len(parts)-1], `[]"`))
	}
	sort.Strings(gotTables)
	want := append([]string(nil), wantTables...)
	sort.Strings(want)
	if strings.Join(gotTables, ",") != strings.Join(want, ",") {
		t.Errorf("staged tables = %v, want %v; events: %v", gotTables, want, events)
	}
	if !strings.Contains(events[doltCommitIdx], wantMsg) {
		t.Errorf("DOLT_COMMIT message %q does not contain %q; events: %v", events[doltCommitIdx], wantMsg, events)
	}
}

// TestReopenIssueDoltCommitRunsAfterTxCommit extends the post-tx ordering
// contract (TestIssueOperationDoltCommitRunsAfterTxCommit) to the reopen
// write path (GH#5752): ReopenIssue must run DOLT_ADD/DOLT_COMMIT strictly
// after the driver-level tx COMMIT, through the shared post-tx publisher.
func TestReopenIssueDoltCommitRunsAfterTxCommit(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	connector.rows = func(query string) (driver.Rows, bool) {
		if strings.Contains(query, "SELECT status FROM issues WHERE id") {
			return &valRows{cols: []string{"status"}, vals: [][]driver.Value{{"closed"}}}, true
		}
		return nil, false
	}
	connector.execRows = func(query string) (int64, bool) {
		// Derived blocked-state maintenance is a fixpoint loop: report no
		// change so it converges.
		if strings.Contains(query, "is_blocked") {
			return 0, true
		}
		return 0, false
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.ReopenIssue(ctx, "test-1", "", "tester"); err != nil {
		t.Fatalf("ReopenIssue: %v; events: %v", err, rec.snapshot())
	}

	assertPostTxPublication(t, rec.snapshot(), []string{"issues", "events"}, "bd: reopen test-1")
}
