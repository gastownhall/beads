package dolt

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestReopenIssueDoltCommitRunsAfterTxCommit extends the post-tx ordering
// contract (TestIssueOperationDoltCommitRunsAfterTxCommit) to the legacy
// reopen path (GH#5752): ReopenIssue must run DOLT_ADD/DOLT_COMMIT strictly
// after the driver-level tx COMMIT.
func TestReopenIssueDoltCommitRunsAfterTxCommit(t *testing.T) {
	d := newPublicationOrderDriver()
	d.issueStatus = types.StatusClosed
	s := d.store()
	t.Cleanup(func() { _ = s.db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.ReopenIssue(ctx, "pub-1", "", "tester"); err != nil {
		t.Fatalf("ReopenIssue: %v; events:%s", err, formatPubEvents(d.snapshot()))
	}
	pub := requirePublishedAfterCommit(t, "ReopenIssue", d.snapshot(), writesTo("issues"))
	requirePublication(t, "ReopenIssue", pub, []string{"issues", "events"}, "bd: reopen pub-1")
}

// TestLegacyIssueWritesPublishAfterSQLCommit pins GH#5752's ordering contract
// over the real legacy DoltStore issue-mutation methods: each one's DOLT_ADD
// and DOLT_COMMIT run after its successful SQL COMMIT, with no SQL transaction
// open on the publishing connection, staging the same table set and using the
// same message the in-transaction helper used.
func TestLegacyIssueWritesPublishAfterSQLCommit(t *testing.T) {
	issueEvents := []string{"issues", "events"}
	cases := []struct {
		name     string
		status   types.Status
		assignee string
		rows     func(query string, args []driver.NamedValue) (driver.Rows, bool)
		run      func(context.Context, *DoltStore) error
		mutation func(pubEvent) bool
		tables   []string
		message  string
	}{
		{
			name: "UpdateIssue",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.UpdateIssue(ctx, "pub-1", map[string]interface{}{"title": "retitled"}, "alice")
			},
			mutation: writesTo("issues"),
			tables:   issueEvents,
			message:  "bd: update pub-1",
		},
		{
			name: "UpdateIssueChecked",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.UpdateIssueChecked(ctx, "pub-1", map[string]interface{}{"title": "retitled"}, "alice", storage.UpdateIssueOptions{})
			},
			mutation: writesTo("issues"),
			tables:   issueEvents,
			message:  "bd: update pub-1",
		},
		{
			name: "ClaimIssue",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.ClaimIssue(ctx, "pub-1", "alice")
			},
			mutation: writesTo("issues"),
			tables:   issueEvents,
			message:  "bd: claim pub-1",
		},
		{
			name: "ClaimReadyIssue",
			rows: func(query string, _ []driver.NamedValue) (driver.Rows, bool) {
				if strings.Contains(query, "SELECT id FROM issues") {
					return &valRows{cols: []string{"id"}, vals: [][]driver.Value{{"pub-1"}}}, true
				}
				return nil, false
			},
			run: func(ctx context.Context, s *DoltStore) error {
				claimed, err := s.ClaimReadyIssue(ctx, types.WorkFilter{}, "alice")
				if err == nil && (claimed == nil || claimed.ID != "pub-1") {
					t.Errorf("ClaimReadyIssue claimed %+v, want pub-1", claimed)
				}
				return err
			},
			mutation: writesTo("issues"),
			tables:   issueEvents,
			message:  "bd: claim ready pub-1",
		},
		{
			name:     "UnclaimIssue",
			status:   types.StatusInProgress,
			assignee: "alice",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.UnclaimIssue(ctx, "pub-1", "alice", false)
			},
			mutation: writesTo("issues"),
			tables:   issueEvents,
			message:  "bd: unclaim pub-1",
		},
		{
			name:     "UnclaimIssueIfAssignee",
			status:   types.StatusInProgress,
			assignee: "alice",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.UnclaimIssueIfAssignee(ctx, "pub-1", "alice", "alice")
			},
			mutation: writesTo("issues"),
			tables:   issueEvents,
			message:  "bd: unclaim pub-1",
		},
		{
			name: "ReclaimExpiredLeases",
			rows: func(query string, _ []driver.NamedValue) (driver.Rows, bool) {
				if strings.Contains(query, "FROM leases l") && strings.Contains(query, "lease_expires_at < ?") &&
					strings.Contains(query, "SELECT l.issue_id") {
					return &valRows{cols: []string{"issue_id", "assignee", "granted_node"}, vals: [][]driver.Value{{"pub-1", "alice", ""}}}, true
				}
				return nil, false
			},
			run: func(ctx context.Context, s *DoltStore) error {
				got, err := s.ReclaimExpiredLeases(ctx, time.Minute, types.ReclaimFilter{AnyReplica: true}, "reaper")
				if err == nil && len(got) != 1 {
					t.Errorf("ReclaimExpiredLeases reclaimed %d lease(s), want 1", len(got))
				}
				return err
			},
			mutation: writesTo("issues"),
			tables:   issueEvents,
			message:  "bd: reclaim 1 expired lease(s)",
		},
		{
			name:   "ReopenIssue",
			status: types.StatusClosed,
			run: func(ctx context.Context, s *DoltStore) error {
				return s.ReopenIssue(ctx, "pub-1", "again", "alice")
			},
			mutation: writesTo("issues"),
			tables:   issueEvents,
			message:  "bd: reopen pub-1",
		},
		{
			name: "CloseIssue",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.CloseIssue(ctx, "pub-1", "done", "alice", "")
			},
			mutation: writesTo("issues"),
			tables:   issueEvents,
			message:  "bd: close pub-1",
		},
		{
			name: "CloseIssueChecked",
			run: func(ctx context.Context, s *DoltStore) error {
				_, err := s.CloseIssueChecked(ctx, "pub-1", "alice", storage.CloseIssueOptions{Reason: "done", Force: true})
				return err
			},
			mutation: writesTo("issues"),
			tables:   issueEvents,
			message:  "bd: close pub-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newPublicationOrderDriver()
			if tc.status != "" {
				d.issueStatus = tc.status
			}
			d.issueAssignee = tc.assignee
			d.rows = tc.rows
			s := d.store()
			t.Cleanup(func() { _ = s.db.Close() })

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := tc.run(ctx, s); err != nil {
				t.Fatalf("%s: %v; events:%s", tc.name, err, formatPubEvents(d.snapshot()))
			}
			pub := requirePublishedAfterCommit(t, tc.name, d.snapshot(), tc.mutation)
			requirePublication(t, tc.name, pub, tc.tables, tc.message)
		})
	}
}
