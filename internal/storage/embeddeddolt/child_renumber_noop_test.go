//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestEmbeddedPullRenumberChildrenWithoutCollision is bd-28sb25 AC-8: with no
// hierarchical add/add collision the flag renames nothing and makes no rename
// commit, and the ordinary settlement of the pinned remote head decides the
// pull: distinct children merge, and a declined conflict fails it with HEAD
// where it was. The clean-state requirement still holds with nothing to
// rename, and a repeat on the now in-sync store adds no commit.
func TestEmbeddedPullRenumberChildrenWithoutCollision(t *testing.T) {
	p := childCollisionParent
	cases := []struct {
		name         string
		localTitle   string
		dirty        bool
		wantConflict bool
		wantErr      string
	}{
		{name: "clean_merge"},
		{name: "declined_conflict", localTitle: "local epic", wantConflict: true},
		{name: "dirty_working_set", dirty: true, wantErr: "uncommitted changes"},
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
				if tc.dirty {
					exec("UPDATE issues SET notes = 'uncommitted' WHERE id = 'rn-other'")
				}
				before = headHash(t, ctx, conn)
			})

			report, err := te.store.PullRenumberChildren(ctx, "", "tester")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("pull = %v, want an error naming %q", err, tc.wantErr)
				}
				if report != nil {
					t.Errorf("report = %+v, want none from a refused pull", report)
				}
			} else {
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
				if len(report.Renames) != 0 || report.RenameCommit != "" || report.RenameParent != "" || report.Retained {
					t.Errorf("report = %+v, want no renames and no rename commit", report)
				}
				if report.LocalHead != before {
					t.Errorf("report local head = %s, want %s", report.LocalHead, before)
				}
				if report.Merged == tc.wantConflict {
					t.Errorf("merged = %v, want %v", report.Merged, !tc.wantConflict)
				}
			}

			var afterMerge string
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
				afterMerge = headHash(t, ctx, conn)
				switch {
				case tc.wantConflict || tc.dirty:
					if afterMerge != before {
						t.Errorf("HEAD moved from %s to %s on a failed pull", before, afterMerge)
					}
					if ids != 1 {
						t.Errorf("%d of P.71/rn-other present after the failed pull, want only rn-other", ids)
					}
				default:
					if ids != 2 {
						t.Errorf("%d of P.71/rn-other present after the merge, want both", ids)
					}
					// One accurate result: the merge it reports is HEAD, and its
					// second parent is the remote head the plan pinned.
					if report.Head != afterMerge {
						t.Errorf("report head = %s, want HEAD %s", report.Head, afterMerge)
					}
					var theirs string
					if err := conn.QueryRowContext(ctx,
						"SELECT parent_hash FROM dolt_commit_ancestors WHERE commit_hash = ? AND parent_index = 1", afterMerge).Scan(&theirs); err != nil {
						t.Fatalf("read merge parents: %v", err)
					}
					if theirs != report.RemoteHead {
						t.Errorf("merged %s, want the pinned remote head %s", theirs, report.RemoteHead)
					}
				}
				if tc.dirty {
					var notes string
					if err := conn.QueryRowContext(ctx, "SELECT notes FROM issues WHERE id = 'rn-other'").Scan(&notes); err != nil {
						t.Fatalf("read notes: %v", err)
					}
					if notes != "uncommitted" {
						t.Errorf("the uncommitted edit = %q, want it kept", notes)
					}
				}
			})
			if tc.wantConflict || tc.dirty {
				return
			}

			// glm's repeat: on the now in-sync store a second run still has
			// nothing to rename and adds no commit, rename or otherwise.
			again, err := te.store.PullRenumberChildren(ctx, "", "tester")
			if err != nil {
				t.Fatalf("second pull: %v", err)
			}
			if len(again.Renames) != 0 || again.RenameCommit != "" || again.Retained {
				t.Errorf("second report = %+v, want nothing renamed or retained", again)
			}
			withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
				if got := headHash(t, ctx, conn); got != afterMerge {
					t.Errorf("HEAD moved %s -> %s on an in-sync second run", afterMerge, got)
				}
			})
		})
	}
}

// insertIdenticalChild inserts a full row both clones write byte for byte:
// every column, timestamps and content hash included, is pinned.
func insertIdenticalChild(t *testing.T, ctx context.Context, conn *sql.Conn, id, title, hash string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO issues (id, content_hash, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at) "+
			"VALUES (?, ?, ?, '', '', '', '', 'open', 2, 'task', '2026-01-01 00:00:00', '2026-01-01 00:00:00')",
		id, hash, title); err != nil {
		t.Fatalf("insert issue %s: %v", id, err)
	}
}

// seedIdenticalAddAddPull publishes base, then the remote clone's rows, then
// rewinds and commits the local clone's rows, the shape a fetch leaves.
func seedIdenticalAddAddPull(t *testing.T, ctx context.Context, conn *sql.Conn, p string, remote, local func()) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
		"VALUES (?, 'Parent', '', '', '', '', 'open', 2, 'task')", p)
	exec("CALL DOLT_COMMIT('-Am', 'seed base')")
	base := headHash(t, ctx, conn)
	exec("CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote"))
	exec("CALL DOLT_PUSH('origin', 'main')")
	remote()
	exec("CALL DOLT_COMMIT('-Am', 'remote clone')")
	exec("CALL DOLT_PUSH('origin', 'main')")
	exec("CALL DOLT_RESET('--hard', ?)", base)
	local()
	exec("CALL DOLT_COMMIT('-Am', 'local clone')")
}

// TestEmbeddedPullIdenticalAddAddIsNotRenumbered is adapted from @rjc123's
// TestEmbeddedRebaseIdenticalAddAddIsNotRenumbered (#4844): an id both
// clones added with byte-identical rows is ONE issue that reached both out of
// band, not two issues contesting a slot. Renumbering it would fork it in
// two, irreversibly. Only the genuinely different .72 moves.
func TestEmbeddedPullIdenticalAddAddIsNotRenumbered(t *testing.T) {
	te := newTestEnv(t, "rn")
	ctx := t.Context()
	p := "rn-ident"
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		seedIdenticalAddAddPull(t, ctx, conn, p, func() {
			insertIdenticalChild(t, ctx, conn, p+".71", "SharedIssue", "hash-shared")
			insertIdenticalChild(t, ctx, conn, p+".72", "Eugene", "hash-remote-72")
		}, func() {
			insertIdenticalChild(t, ctx, conn, p+".71", "SharedIssue", "hash-shared")
			insertIdenticalChild(t, ctx, conn, p+".72", "Local72", "hash-local-72")
		})
	})

	report, err := te.store.PullRenumberChildren(ctx, "", "tester")
	if err != nil {
		t.Fatalf("pull (identical add/add): %v", err)
	}
	if want := []storage.ChildRenumber{{From: p + ".72", To: p + ".73"}}; len(report.Renames) != 1 || report.Renames[0] != want[0] {
		t.Fatalf("renames = %v, want %v (only the genuinely different .72)", report.Renames, want)
	}
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		want := map[string]string{p + ".71": "SharedIssue", p + ".72": "Eugene", p + ".73": "Local72"}
		for id, title := range want {
			var got string
			var n int
			if err := conn.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(MAX(title), '') FROM issues WHERE id = ?", id).Scan(&n, &got); err != nil {
				t.Fatalf("read %s: %v", id, err)
			}
			if n != 1 || got != title {
				t.Errorf("%s = %q in %d row(s), want %q in one", id, got, n, title)
			}
		}
		var forked int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", p+".74").Scan(&forked); err != nil {
			t.Fatalf("read %s.74: %v", p, err)
		}
		if forked != 0 {
			t.Errorf("%s.74 exists: the identical .71 was forked into a second row", p)
		}
	})
}

// TestEmbeddedPullCollidingChildUnderIdenticalParent is adapted from
// @rjc123's TestEmbeddedRebaseCollidingChildUnderIdenticalParent (#4844): .71
// is identical on both sides and its child .71.1 is not. The identical parent
// is not a collision root, so it must not swallow its child's collision: .71
// stays, and .71.1 is renumbered in its own right.
func TestEmbeddedPullCollidingChildUnderIdenticalParent(t *testing.T) {
	te := newTestEnv(t, "rn")
	ctx := t.Context()
	p := "rn-identkid"
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		seedIdenticalAddAddPull(t, ctx, conn, p, func() {
			insertIdenticalChild(t, ctx, conn, p+".71", "SharedParent", "hash-shared-parent")
			insertIdenticalChild(t, ctx, conn, p+".71.1", "RemoteChild", "hash-remote-child")
		}, func() {
			insertIdenticalChild(t, ctx, conn, p+".71", "SharedParent", "hash-shared-parent")
			insertIdenticalChild(t, ctx, conn, p+".71.1", "LocalChild", "hash-local-child")
		})
	})

	report, err := te.store.PullRenumberChildren(ctx, "", "tester")
	if err != nil {
		t.Fatalf("pull (colliding child under identical parent): %v", err)
	}
	if want := (storage.ChildRenumber{From: p + ".71.1", To: p + ".71.2"}); len(report.Renames) != 1 || report.Renames[0] != want {
		t.Fatalf("renames = %v, want [%v] (the child, not its identical parent)", report.Renames, want)
	}
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		for id, title := range map[string]string{p + ".71": "SharedParent", p + ".71.1": "RemoteChild", p + ".71.2": "LocalChild"} {
			var got string
			if err := conn.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", id).Scan(&got); err != nil {
				t.Fatalf("read %s: %v", id, err)
			}
			if got != title {
				t.Errorf("%s = %q, want %q", id, got, title)
			}
		}
	})
}
