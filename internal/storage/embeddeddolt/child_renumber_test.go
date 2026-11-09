//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// childCollisionParent is the epic both clones mint children under.
const childCollisionParent = "rn-p"

// seedChildCollision builds GH#4796's report shape in one database with a
// real file:// remote: base counter 70 published, the "remote clone" mints
// P.71/P.72 (counter 72) and publishes, then the branch is rewound to the
// base and the "local clone" mints its own P.71/P.72/P.73 (counter 73).
// origin/main is therefore the remote clone's tip, as a fetch leaves it.
// Returns the base commit.
func seedChildCollision(t *testing.T, ctx context.Context, conn *sql.Conn) (base string) {
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
	p := childCollisionParent
	insertIssue(p, "epic")
	exec("INSERT INTO child_counters (parent_id, last_child) VALUES (?, 70)", p)
	exec("CALL DOLT_COMMIT('-Am', 'seed: counter 70')")
	if err := conn.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&base); err != nil {
		t.Fatalf("read base: %v", err)
	}
	exec("CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote"))
	exec("CALL DOLT_PUSH('origin', 'main')")

	insertIssue(p+".71", "remote 71")
	insertIssue(p+".72", "remote 72")
	exec("UPDATE child_counters SET last_child = 72 WHERE parent_id = ?", p)
	exec("CALL DOLT_COMMIT('-Am', 'remote clone: P.71 P.72')")
	exec("CALL DOLT_PUSH('origin', 'main')")

	exec("CALL DOLT_RESET('--hard', ?)", base)
	insertIssue(p+".71", "local 71")
	insertIssue(p+".72", "local 72")
	insertIssue(p+".73", "local 73")
	exec("UPDATE child_counters SET last_child = 73 WHERE parent_id = ?", p)
	exec("CALL DOLT_COMMIT('-Am', 'local clone: P.71 P.72 P.73')")
	return base
}

// issueTitles reads id -> title for the parent's direct and nested children.
func issueTitles(t *testing.T, ctx context.Context, conn *sql.Conn) map[string]string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT id, title FROM issues WHERE id LIKE CONCAT(?, '.%')", childCollisionParent)
	if err != nil {
		t.Fatalf("read titles: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			t.Fatalf("scan title: %v", err)
		}
		out[id] = title
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read titles: %v", err)
	}
	return out
}

func headHash(t *testing.T, ctx context.Context, conn *sql.Conn) string {
	t.Helper()
	var h string
	if err := conn.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&h); err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	return h
}

// TestEmbeddedPullChildCollisionPlansRenumber is bd-28sb25 AC-2: an ordinary
// pull over the report fixture still fails (and is aborted, so nothing is
// renamed), and the read-only plan proposes P.71->P.74, P.72->P.75: above the
// local counter 73, the remote counter 72 and every used suffix.
func TestEmbeddedPullChildCollisionPlansRenumber(t *testing.T) {
	te := newTestEnv(t, "rn")
	ctx := t.Context()
	var before string
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		seedChildCollision(t, ctx, conn)
		before = headHash(t, ctx, conn)
	})

	err := te.store.Pull(ctx)
	var mce *versioncontrolops.MergeConflictsError
	if !errors.As(err, &mce) {
		t.Fatalf("ordinary pull = %v, want a MergeConflictsError", err)
	}
	p := childCollisionParent
	wantLocal := map[string]string{p + ".71": "local 71", p + ".72": "local 72", p + ".73": "local 73"}
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		if got := headHash(t, ctx, conn); got != before {
			t.Errorf("HEAD moved from %s to %s on a failed pull", before, got)
		}
		if got := issueTitles(t, ctx, conn); !reflect.DeepEqual(got, wantLocal) {
			t.Errorf("after failed pull children = %v, want unchanged %v", got, wantLocal)
		}
	})

	report, err := te.store.PlanChildRenumbers(ctx, "")
	if err != nil {
		t.Fatalf("PlanChildRenumbers: %v", err)
	}
	want := []storage.ChildRenumber{{From: p + ".71", To: p + ".74"}, {From: p + ".72", To: p + ".75"}}
	if !reflect.DeepEqual(report.Renames, want) {
		t.Errorf("plan = %v, want %v", report.Renames, want)
	}
	if report.Remote != "origin" || report.LocalHead != before || report.RemoteHead == "" || report.MergeBase == "" {
		t.Errorf("plan revisions = %+v, want origin with both heads and a merge base", report)
	}
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		if got := issueTitles(t, ctx, conn); !reflect.DeepEqual(got, wantLocal) {
			t.Errorf("planning renamed children: %v", got)
		}
	})
}
