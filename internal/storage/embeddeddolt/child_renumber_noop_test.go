//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestEmbeddedPullRenumberChildrenWithoutCollision is bd-28sb25 AC-8: with no
// hierarchical add/add collision the flag renames nothing and makes no rename
// commit, and the ordinary settlement decides the pull: distinct children
// merge, and a declined conflict fails it with HEAD where it was.
func TestEmbeddedPullRenumberChildrenWithoutCollision(t *testing.T) {
	p := childCollisionParent
	cases := []struct {
		name         string
		localTitle   string
		wantConflict bool
	}{
		{name: "clean_merge"},
		{name: "declined_conflict", localTitle: "local epic", wantConflict: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			te := newTestEnv(t, "rn")
			ctx := t.Context()
			var before string
			withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
				exec := func(q string, args ...any) {
					t.Helper()
					if _, err := conn.ExecContext(ctx, q, args...); err != nil {
						t.Fatalf("%s: %v", q, err)
					}
				}
				insertIssue := func(id, title string) {
					t.Helper()
					exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
						"VALUES (?, ?, '', '', '', '', 'open', 2, 'task')", id, title)
				}
				insertIssue(p, "epic")
				exec("INSERT INTO child_counters (parent_id, last_child) VALUES (?, 70)", p)
				exec("CALL DOLT_COMMIT('-Am', 'seed: counter 70')")
				base := headHash(t, ctx, conn)
				exec("CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote"))
				exec("CALL DOLT_PUSH('origin', 'main')")

				insertIssue(p+".71", "remote 71")
				exec("UPDATE child_counters SET last_child = 71 WHERE parent_id = ?", p)
				// Both retitles carry the same updated_at, so the auto-resolver
				// cannot settle the title last-write-wins and declines it.
				exec("UPDATE issues SET title = 'remote epic', updated_at = '2026-01-02 03:04:05' WHERE id = ?", p)
				exec("CALL DOLT_COMMIT('-Am', 'remote clone: P.71')")
				exec("CALL DOLT_PUSH('origin', 'main')")

				exec("CALL DOLT_RESET('--hard', ?)", base)
				insertIssue("rn-other", "local top-level")
				if tc.localTitle != "" {
					exec("UPDATE issues SET title = ?, updated_at = '2026-01-02 03:04:05' WHERE id = ?", tc.localTitle, p)
				}
				exec("CALL DOLT_COMMIT('-Am', 'local clone')")
				before = headHash(t, ctx, conn)
			})

			report, err := te.store.PullRenumberChildren(ctx, "", "tester")
			var mce *versioncontrolops.MergeConflictsError
			if tc.wantConflict != errors.As(err, &mce) {
				t.Fatalf("pull = %v, want conflict %v", err, tc.wantConflict)
			}
			if !tc.wantConflict && err != nil {
				t.Fatalf("pull: %v", err)
			}
			if report == nil {
				t.Fatal("no report")
			}
			if len(report.Renames) != 0 || report.RenameCommit != "" || len(report.PriorRenameCommits) != 0 {
				t.Errorf("report = %+v, want no renames and no rename commit", report)
			}
			if report.Merged == tc.wantConflict {
				t.Errorf("merged = %v, want %v", report.Merged, !tc.wantConflict)
			}

			withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
				var renameCommits int
				if err := conn.QueryRowContext(ctx,
					"SELECT COUNT(*) FROM dolt_log WHERE message LIKE 'bd: renumber colliding children%'").Scan(&renameCommits); err != nil {
					t.Fatalf("read log: %v", err)
				}
				if renameCommits != 0 {
					t.Errorf("%d rename commits, want none", renameCommits)
				}
				var ids int
				if err := conn.QueryRowContext(ctx,
					"SELECT COUNT(*) FROM issues WHERE id IN (?, ?)", p+".71", "rn-other").Scan(&ids); err != nil {
					t.Fatalf("read issues: %v", err)
				}
				if tc.wantConflict {
					if got := headHash(t, ctx, conn); got != before {
						t.Errorf("HEAD moved from %s to %s on a failed pull", before, got)
					}
					if ids != 1 {
						t.Errorf("%d of P.71/rn-other present after the failed pull, want only rn-other", ids)
					}
				} else if ids != 2 {
					t.Errorf("%d of P.71/rn-other present after the merge, want both", ids)
				}
			})
		})
	}
}
