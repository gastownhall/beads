//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
)

// The first two tests here are adapted from Robin Carswell's (@rjc123) #4844
// TestEmbeddedRebaseRemoteDominatesHighWaterSlots and
// TestEmbeddedRebaseRemoteDominatesDurableSubtreeCounter: the same fixture
// and the same remote-keeps-its-ids outcome, reached through
// `bd dolt pull --renumber-children` (a real file:// remote) instead of a
// rebase against a peer branch.

// seedRemoteDominatesCollision is #4844's seedRebaseCollision over a file://
// remote. Base: parent P with child P.70 and counter 70. The remote clone
// adds P.71 "Tom" and P.72 "Eugene" (counter 72) and publishes; the local
// clone adds P.71/P.72/P.73 (counter 73) with a "vip" label on P.71. Both
// clones give their P.71 the parent edge `bd create --parent` writes, whose
// id derives from the child id, so the two edges share an id.
func seedRemoteDominatesCollision(t *testing.T, ctx context.Context, conn *sql.Conn, p string) {
	t.Helper()
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
	insertDep := func(issueID, dependsOn string) {
		t.Helper()
		exec("INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by, created_at) "+
			"VALUES (?, ?, ?, 'blocks', 'seed', '2026-01-02 03:04:05')", depid.New(issueID, dependsOn), issueID, dependsOn)
	}

	insertIssue(p, "Parent")
	insertIssue(p+".70", "Child70")
	exec("INSERT INTO child_counters (parent_id, last_child) VALUES (?, 70)", p)
	exec("CALL DOLT_COMMIT('-Am', 'seed base')")
	base := headHash(t, ctx, conn)
	exec("CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote"))
	exec("CALL DOLT_PUSH('origin', 'main')")

	insertIssue(p+".71", "Tom")
	insertIssue(p+".72", "Eugene")
	insertDep(p+".71", p)
	exec("UPDATE child_counters SET last_child = 72 WHERE parent_id = ?", p)
	exec("CALL DOLT_COMMIT('-Am', 'remote clone adds .71/.72')")
	exec("CALL DOLT_PUSH('origin', 'main')")

	exec("CALL DOLT_RESET('--hard', ?)", base)
	insertIssue(p+".71", "Local71")
	insertIssue(p+".72", "Local72")
	insertIssue(p+".73", "Local73")
	exec("INSERT INTO labels (issue_id, label) VALUES (?, 'vip')", p+".71")
	insertDep(p+".71", p)
	exec("UPDATE child_counters SET last_child = 73 WHERE parent_id = ?", p)
	exec("CALL DOLT_COMMIT('-Am', 'local clone adds .71/.72/.73')")
}

func renumberTitleOf(t *testing.T, ctx context.Context, conn *sql.Conn, table, id string) (string, bool) {
	t.Helper()
	var title string
	//nolint:gosec // G201: table is a test constant.
	err := conn.QueryRowContext(ctx, "SELECT title FROM `"+table+"` WHERE id = ?", id).Scan(&title)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s %s: %v", table, id, err)
	}
	return title, true
}

// assertNoConflictsAndClean checks the pull left no conflicts and nothing
// uncommitted.
func assertNoConflictsAndClean(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	var conflicts int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&conflicts); err != nil {
		t.Fatalf("read dolt_conflicts: %v", err)
	}
	if conflicts != 0 {
		t.Errorf("%d tables still conflicted", conflicts)
	}
	var dirty int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d tables left uncommitted", dirty)
	}
}

// TestEmbeddedPullRenumberChildrenSkipsHighWaterSlots (bd-28sb25 AC-4,
// adapted from #4844): the local clone deleted its highest child P.73 (the
// counter stays 73: a peer may hold .73) and holds a session wisp at P.74
// that no counter records. The renamed children land above both, at
// P.75/P.76, the wisp keeps its slot, and the parent counter covers them.
func TestEmbeddedPullRenumberChildrenSkipsHighWaterSlots(t *testing.T) {
	te := newTestEnv(t, "rbhw")
	ctx := t.Context()
	p := "rbhw-p"
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		seedRemoteDominatesCollision(t, ctx, conn, p)
		for _, q := range []string{
			"DELETE FROM issues WHERE id = '" + p + ".73'",
			"CALL DOLT_COMMIT('-Am', 'local deletes highest child .73, counter stays 73')",
			// Wisps are dolt-ignored: working-set only, and visible to the
			// plan only through the wisps table.
			"INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) " +
				"VALUES ('" + p + ".74', 'WispSibling', '', '', '', '', 'open', 2, 'task')",
		} {
			if _, err := conn.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	})

	report, err := te.store.PullRenumberChildren(ctx, "", "tester")
	if err != nil {
		t.Fatalf("PullRenumberChildren: %v", err)
	}
	want := []storage.ChildRenumber{{From: p + ".71", To: p + ".75"}, {From: p + ".72", To: p + ".76"}}
	if !reflect.DeepEqual(report.Renames, want) {
		t.Fatalf("renames = %v, want %v", report.Renames, want)
	}

	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		for id, title := range map[string]string{p + ".71": "Tom", p + ".72": "Eugene", p + ".75": "Local71", p + ".76": "Local72"} {
			if got, _ := renumberTitleOf(t, ctx, conn, "issues", id); got != title {
				t.Errorf("%s = %q, want %q", id, got, title)
			}
		}
		for _, slot := range []string{p + ".73", p + ".74"} {
			if got, ok := renumberTitleOf(t, ctx, conn, "issues", slot); ok {
				t.Errorf("issue minted at reserved slot %s (%q)", slot, got)
			}
		}
		if got, ok := renumberTitleOf(t, ctx, conn, "wisps", p+".74"); !ok || got != "WispSibling" {
			t.Errorf("wisp %s.74 = %q (present %v), want it kept", p, got, ok)
		}
		if n, ok := counterValue(t, ctx, conn, p); !ok || n != 76 {
			t.Errorf("counter %s = %d (present %v), want 76", p, n, ok)
		}
		var vip, edgeTarget string
		if err := conn.QueryRowContext(ctx, "SELECT issue_id FROM labels WHERE label = 'vip'").Scan(&vip); err != nil {
			t.Fatalf("read vip label: %v", err)
		}
		if vip != p+".75" {
			t.Errorf("vip label on %s, want %s.75", vip, p)
		}
		if err := conn.QueryRowContext(ctx, "SELECT depends_on_issue_id FROM dependencies WHERE id = ?",
			depid.New(p+".75", p)).Scan(&edgeTarget); err != nil {
			t.Fatalf("edge %s.75 -> %s not under its derived id: %v", p, p, err)
		}
		if edgeTarget != p {
			t.Errorf("edge under %s.75's derived id targets %s, want %s", p, edgeTarget, p)
		}
		assertNoConflictsAndClean(t, ctx, conn)
	})
}

// TestEmbeddedPullRenumberChildrenMovesDurableSubtreeCounter (bd-28sb25
// AC-4, adapted from #4844): the renamed P.71 is itself a parent, with a
// durable child P.71.1 and its own counter 1. The child moves with it to
// P.74.1 and the counter row moves to P.74 with its value, leaving none
// under P.71.
func TestEmbeddedPullRenumberChildrenMovesDurableSubtreeCounter(t *testing.T) {
	te := newTestEnv(t, "rbsub")
	ctx := t.Context()
	p := "rbsub-p"
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		seedRemoteDominatesCollision(t, ctx, conn, p)
		for _, q := range []string{
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) " +
				"VALUES ('" + p + ".71.1', 'GrandChild', '', '', '', '', 'open', 2, 'task')",
			"INSERT INTO child_counters (parent_id, last_child) VALUES ('" + p + ".71', 1)",
			"CALL DOLT_COMMIT('-Am', 'local: durable grandchild + counter under .71')",
		} {
			if _, err := conn.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	})

	report, err := te.store.PullRenumberChildren(ctx, "", "tester")
	if err != nil {
		t.Fatalf("PullRenumberChildren: %v", err)
	}
	want := []storage.ChildRenumber{
		{From: p + ".71", To: p + ".74"}, {From: p + ".72", To: p + ".75"}, {From: p + ".71.1", To: p + ".74.1"},
	}
	if !reflect.DeepEqual(report.Renames, want) {
		t.Fatalf("renames = %v, want %v", report.Renames, want)
	}

	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		if got, _ := renumberTitleOf(t, ctx, conn, "issues", p+".74.1"); got != "GrandChild" {
			t.Errorf("%s.74.1 = %q, want the grandchild moved with its root", p, got)
		}
		if _, ok := renumberTitleOf(t, ctx, conn, "issues", p+".71.1"); ok {
			t.Errorf("grandchild stranded at %s.71.1", p)
		}
		if n, ok := counterValue(t, ctx, conn, p+".71"); ok {
			t.Errorf("counter %s.71 = %d survived its rename", p, n)
		}
		if n, ok := counterValue(t, ctx, conn, p+".74"); !ok || n != 1 {
			t.Errorf("counter %s.74 = %d (present %v), want 1 moved from .71", p, n, ok)
		}
		assertNoConflictsAndClean(t, ctx, conn)
	})
}

// TestEmbeddedPullRenumberChildrenNeverSynthesizesACounter (bd-28sb25 AC-4):
// a renamed node without a counter row (its children predate the counter, so
// P.72.1 exists with none under P.72) gets none under its new id either; only
// the allocation parent's counter is raised.
func TestEmbeddedPullRenumberChildrenNeverSynthesizesACounter(t *testing.T) {
	te := newTestEnv(t, "rn")
	ctx := t.Context()
	p := childCollisionParent
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		seedChildCollision(t, ctx, conn)
		for _, q := range []string{
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) " +
				"VALUES ('" + p + ".72.1', 'local 72.1', '', '', '', '', 'open', 2, 'task')",
			"CALL DOLT_COMMIT('-Am', 'local: counterless child of P.72')",
		} {
			if _, err := conn.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	})

	report, err := te.store.PullRenumberChildren(ctx, "", "tester")
	if err != nil {
		t.Fatalf("PullRenumberChildren: %v", err)
	}
	want := []storage.ChildRenumber{
		{From: p + ".71", To: p + ".74"}, {From: p + ".72", To: p + ".75"}, {From: p + ".72.1", To: p + ".75.1"},
	}
	if !reflect.DeepEqual(report.Renames, want) {
		t.Fatalf("renames = %v, want %v", report.Renames, want)
	}
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		for _, id := range []string{p + ".72", p + ".74", p + ".75", p + ".75.1"} {
			if n, ok := counterValue(t, ctx, conn, id); ok {
				t.Errorf("counter %s = %d was synthesized; no renamed node had one", id, n)
			}
		}
		if n, ok := counterValue(t, ctx, conn, p); !ok || n != 75 {
			t.Errorf("counter %s = %d (present %v), want 75", p, n, ok)
		}
		assertNoConflictsAndClean(t, ctx, conn)
	})
}

// TestEmbeddedPullRenumberChildrenRefusesDestinationCounter (bd-28sb25 AC-4):
// a counter row already under a planned destination is state the repair does
// not understand, whether the source has a counter to move onto it or not.
// Such a row needs a schema without the counter's foreign key (which would
// require an issue there), so the fixture drops it; with no cascade, a
// relocation already done for an earlier rename is undone only by the
// transaction's rollback, which the repair must leave whole.
func TestEmbeddedPullRenumberChildrenRefusesDestinationCounter(t *testing.T) {
	p := childCollisionParent
	for _, tc := range []struct {
		name, stray string
	}{
		// P.71 carries counter 1 and is refused before anything is renamed.
		{name: "source with a counter", stray: p + ".74"},
		// P.72 has no counter; P.71 was already renamed and its counter
		// moved when the second rename is refused.
		{name: "source without a counter", stray: p + ".75"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te := newTestEnv(t, "rn")
			ctx := t.Context()
			var before string
			withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
				seedChildCollision(t, ctx, conn)
				for _, q := range []string{
					"ALTER TABLE child_counters DROP FOREIGN KEY fk_counter_parent",
					"INSERT INTO child_counters (parent_id, last_child) VALUES ('" + p + ".71', 1)",
					"INSERT INTO child_counters (parent_id, last_child) VALUES ('" + tc.stray + "', 9)",
					"CALL DOLT_COMMIT('-Am', 'local: counter on P.71 and a stray destination counter')",
				} {
					if _, err := conn.ExecContext(ctx, q); err != nil {
						t.Fatalf("%s: %v", q, err)
					}
				}
				before = headHash(t, ctx, conn)
			})

			_, err := te.store.PullRenumberChildren(ctx, "", "tester")
			if err == nil || !strings.Contains(err.Error(), "destination "+tc.stray+" already has a child counter row") {
				t.Fatalf("PullRenumberChildren err = %v, want the destination-counter refusal for %s", err, tc.stray)
			}
			withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
				if got := headHash(t, ctx, conn); got != before {
					t.Errorf("HEAD moved from %s to %s on a refused repair", before, got)
				}
				wantTitles := map[string]string{p + ".71": "local 71", p + ".72": "local 72", p + ".73": "local 73"}
				if got := issueTitles(t, ctx, conn); !reflect.DeepEqual(got, wantTitles) {
					t.Errorf("children = %v, want unchanged %v", got, wantTitles)
				}
				wantCounters := map[string]int{p: 73, p + ".71": 1, tc.stray: 9}
				for parent, want := range wantCounters {
					if n, ok := counterValue(t, ctx, conn, parent); !ok || n != want {
						t.Errorf("counter %s = %d (present %v), want %d", parent, n, ok, want)
					}
				}
				for _, id := range []string{p + ".74", p + ".75"} {
					if id == tc.stray {
						continue
					}
					if n, ok := counterValue(t, ctx, conn, id); ok {
						t.Errorf("counter %s = %d left by a rolled-back relocation", id, n)
					}
				}
				assertNoConflictsAndClean(t, ctx, conn)
			})
		})
	}
}
