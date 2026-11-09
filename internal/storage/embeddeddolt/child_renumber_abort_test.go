//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestEmbeddedApplyChildRenumbersAbortsWhole is bd-28sb25 AC-7. The plan is
// made on one session; then a second session changes the database in the
// window before the apply, or the rename set fails part way inside the
// transaction. Each case leaves none of the renames, counter moves, rename
// events or a rename commit behind, HEAD where the other writer left it, and
// the other writer's data as it wrote it.
func TestEmbeddedApplyChildRenumbersAbortsWhole(t *testing.T) {
	p := childCollisionParent
	cases := []struct {
		name string
		// between runs on a second session after the plan was made.
		between func(t *testing.T, ctx context.Context, other *sql.Conn)
		// edit rewrites the plan's renames before the apply.
		edit    func(renames []storage.ChildRenumber) []storage.ChildRenumber
		wantErr string
		// wantIs, when set, is the error type the apply must return.
		wantIs func(error) bool
		// extra lists the children the other session added, beyond the
		// local fixture's, that must survive.
		extra map[string]string
		// dirty reports that the other session left uncommitted changes the
		// abort must keep uncommitted.
		dirty bool
	}{
		{
			// A new child committed at a planned destination moves HEAD.
			name: "head_moved",
			between: func(t *testing.T, ctx context.Context, other *sql.Conn) {
				otherExec(t, ctx, other, "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
					"VALUES (?, 'concurrent 74', '', '', '', '', 'open', 2, 'task')", p+".74")
				otherExec(t, ctx, other, "CALL DOLT_COMMIT('-Am', 'concurrent child P.74')")
			},
			wantErr: "the branch moved since the plan",
			extra:   map[string]string{p + ".74": "concurrent 74"},
		},
		{
			// An uncommitted unrelated edit would otherwise ride along in the
			// rename commit.
			name: "unrelated_dirty_write",
			between: func(t *testing.T, ctx context.Context, other *sql.Conn) {
				otherExec(t, ctx, other, "UPDATE issues SET notes = 'written after the plan' WHERE id = ?", p)
			},
			wantErr: "uncommitted changes",
			dirty:   true,
		},
		{
			// A wisp minted at the second destination after the plan: the
			// branch and working set are unchanged, so only the in-transaction
			// occupancy check sees it, after P.71 -> P.74 already ran.
			name: "destination_taken_by_wisp",
			between: func(t *testing.T, ctx context.Context, other *sql.Conn) {
				otherExec(t, ctx, other, "INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
					"VALUES (?, 'concurrent wisp', '', '', '', '', 'open', 2, 'task')", p+".75")
			},
			wantErr: p + ".75 already exists; pull again to re-plan",
		},
		{
			// glm's case: the second destination retired by a tombstone after
			// the plan (clone-local, so neither HEAD nor the working set
			// moves); UpdateIssueIDInTx refuses it after P.71 -> P.74 ran.
			name: "destination_retired",
			between: func(t *testing.T, ctx context.Context, other *sql.Conn) {
				otherExec(t, ctx, other, "INSERT INTO wisp_id_tombstones (id, closed_at) VALUES (?, '2026-01-01 00:00:00')", p+".75")
			},
			wantErr: p + ".75",
			wantIs: func(err error) bool {
				var expired *publicops.IdempotencyExpiredError
				return errors.As(err, &expired)
			},
		},
		{
			// opus's stale plan: a destination an issue already holds.
			name: "stale_plan",
			edit: func(renames []storage.ChildRenumber) []storage.ChildRenumber {
				return []storage.ChildRenumber{renames[0], {From: p + ".72", To: p + ".73"}}
			},
			wantErr: p + ".73 already exists",
		},
		{
			// A later rename the database rejects (the id overflows its
			// column) after the first one ran inside the transaction.
			name: "rename_fails_mid_transaction",
			edit: func(renames []storage.ChildRenumber) []storage.ChildRenumber {
				return []storage.ChildRenumber{renames[0], {From: p + ".72", To: p + "." + strings.Repeat("9", 300)}}
			},
			wantErr: p + ".72",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			te := newTestEnv(t, "rn")
			ctx := t.Context()
			pool, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "")
			if err != nil {
				t.Fatalf("OpenSQL: %v", err)
			}
			defer func() { _ = cleanup() }()
			conn, err := pool.Conn(ctx)
			if err != nil {
				t.Fatalf("pin connection: %v", err)
			}
			defer func() { _ = conn.Close() }()
			other, err := pool.Conn(ctx)
			if err != nil {
				t.Fatalf("pin second connection: %v", err)
			}
			defer func() { _ = other.Close() }()

			seedChildCollision(t, ctx, conn)
			otherExec(t, ctx, conn, "INSERT INTO child_counters (parent_id, last_child) VALUES (?, 4)", p+".71")
			otherExec(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'local clone: P.71 counter')")

			report, err := versioncontrolops.PlanChildRenumbers(ctx, conn, "main", "origin/main")
			if err != nil {
				t.Fatalf("PlanChildRenumbers: %v", err)
			}
			wantPlan := []storage.ChildRenumber{{From: p + ".71", To: p + ".74"}, {From: p + ".72", To: p + ".75"}}
			if !reflect.DeepEqual(report.Renames, wantPlan) {
				t.Fatalf("plan = %v, want %v", report.Renames, wantPlan)
			}
			if tc.between != nil {
				tc.between(t, ctx, other)
			}
			if tc.edit != nil {
				report.Renames = tc.edit(report.Renames)
			}
			before := headHash(t, ctx, conn)

			err = versioncontrolops.ApplyChildRenumbers(ctx, conn, report, "tester")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ApplyChildRenumbers = %v, want an error naming %q", err, tc.wantErr)
			}
			if tc.wantIs != nil && !tc.wantIs(err) {
				t.Errorf("ApplyChildRenumbers error %T (%v) is not the expected type", err, err)
			}
			if report.RenameCommit != "" || report.Mentions != nil {
				t.Errorf("report carries a rename result after the abort: commit %q, mentions %v", report.RenameCommit, report.Mentions)
			}

			wantTitles := map[string]string{p + ".71": "local 71", p + ".72": "local 72", p + ".73": "local 73"}
			for id, title := range tc.extra {
				wantTitles[id] = title
			}
			if got := issueTitles(t, ctx, conn); !reflect.DeepEqual(got, wantTitles) {
				t.Errorf("children = %v, want %v", got, wantTitles)
			}
			for parent, want := range map[string]int{p: 73, p + ".71": 4} {
				if n, ok := counterValue(t, ctx, conn, parent); !ok || n != want {
					t.Errorf("counter %s = %d (present %v), want %d", parent, n, ok, want)
				}
			}
			if n, ok := counterValue(t, ctx, conn, p+".74"); ok {
				t.Errorf("counter %s.74 = %d persisted", p, n)
			}
			var renamed int
			if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE event_type = 'renamed'").Scan(&renamed); err != nil {
				t.Fatalf("read events: %v", err)
			}
			if renamed != 0 {
				t.Errorf("%d rename events persisted", renamed)
			}
			var renameCommits int
			if err := conn.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM dolt_log WHERE message LIKE 'bd: renumber colliding children%'").Scan(&renameCommits); err != nil {
				t.Fatalf("read log: %v", err)
			}
			if renameCommits != 0 {
				t.Errorf("%d rename commit(s) on the branch", renameCommits)
			}
			if got := headHash(t, ctx, conn); got != before {
				t.Errorf("HEAD moved from %s to %s", before, got)
			}
			clean, err := versioncontrolops.WorkingSetClean(ctx, conn)
			if err != nil {
				t.Fatalf("WorkingSetClean: %v", err)
			}
			if clean == tc.dirty {
				t.Errorf("working set clean = %v after the abort, want %v", clean, !tc.dirty)
			}
			if tc.dirty {
				var notes string
				if err := conn.QueryRowContext(ctx, "SELECT notes FROM issues WHERE id = ?", p).Scan(&notes); err != nil {
					t.Fatalf("read notes: %v", err)
				}
				if notes != "written after the plan" {
					t.Errorf("the other session's uncommitted edit = %q, want it kept", notes)
				}
			}
		})
	}
}

func otherExec(t *testing.T, ctx context.Context, conn *sql.Conn, q string, args ...any) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
