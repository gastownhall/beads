//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestEmbeddedPullRenumberChildren is bd-28sb25 AC-3 on the embedded store:
// over GH#4796's report fixture, `--renumber-children` keeps the remote's
// P.71/P.72, moves the local ones to P.74/P.75 with their labels, leaves the
// uncontested P.73 alone, settles the counter at 75 and leaves no conflicts.
func TestEmbeddedPullRenumberChildren(t *testing.T) {
	te := newTestEnv(t, "rn")
	ctx := t.Context()
	p := childCollisionParent
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		seedChildCollision(t, ctx, conn)
		if _, err := conn.ExecContext(ctx, "INSERT INTO labels (issue_id, label) VALUES (?, 'local-71')", p+".71"); err != nil {
			t.Fatalf("label local 71: %v", err)
		}
		if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local clone: label P.71')"); err != nil {
			t.Fatalf("commit label: %v", err)
		}
	})

	report, err := te.store.PullRenumberChildren(ctx, "", "tester")
	if err != nil {
		t.Fatalf("PullRenumberChildren: %v", err)
	}
	want := []storage.ChildRenumber{{From: p + ".71", To: p + ".74"}, {From: p + ".72", To: p + ".75"}}
	if !reflect.DeepEqual(report.Renames, want) {
		t.Errorf("renames = %v, want %v", report.Renames, want)
	}
	if report.RenameCommit == "" || !report.Merged || report.Remote != "origin" {
		t.Errorf("report = %+v, want a rename commit, a merge, remote origin", report)
	}

	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		wantTitles := map[string]string{
			p + ".71": "remote 71", p + ".72": "remote 72", p + ".73": "local 73",
			p + ".74": "local 71", p + ".75": "local 72",
		}
		if got := issueTitles(t, ctx, conn); !reflect.DeepEqual(got, wantTitles) {
			t.Errorf("children = %v, want %v", got, wantTitles)
		}
		var labelled string
		if err := conn.QueryRowContext(ctx, "SELECT issue_id FROM labels WHERE label = 'local-71'").Scan(&labelled); err != nil {
			t.Fatalf("read label: %v", err)
		}
		if labelled != p+".74" {
			t.Errorf("local 71's label is on %s, want %s", labelled, p+".74")
		}
		if n, ok := counterValue(t, ctx, conn, p); !ok || n != 75 {
			t.Errorf("counter = %d (present %v), want 75", n, ok)
		}
		var conflicts int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&conflicts); err != nil {
			t.Fatalf("read conflicts: %v", err)
		}
		if conflicts != 0 {
			t.Errorf("%d tables still conflicted", conflicts)
		}
		var remoteInHistory int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?", report.RemoteHead).Scan(&remoteInHistory); err != nil {
			t.Fatalf("read log: %v", err)
		}
		if remoteInHistory != 1 {
			t.Errorf("remote head %s is not in the local history after the pull", report.RemoteHead)
		}
	})
}

// TestEmbeddedPullRenumberChildrenRefusesDirtyWorkingSet pins the C2 clean
// state requirement for bd-28sb25 AC-3: unlike a plain pull,
// `--renumber-children` never commits pending changes into history. Over the
// report fixture with an uncommitted unrelated issue it refuses before
// fetching anything new, renames nothing, makes no commit, and leaves the
// pending issue uncommitted.
func TestEmbeddedPullRenumberChildrenRefusesDirtyWorkingSet(t *testing.T) {
	te := newTestEnv(t, "rn")
	ctx := t.Context()
	p := childCollisionParent
	var before string
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		seedChildCollision(t, ctx, conn)
		if _, err := conn.ExecContext(ctx, "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('rn-pending', 'pending work', '', '', '', '', 'open', 2, 'task')"); err != nil {
			t.Fatalf("write pending issue: %v", err)
		}
		before = headHash(t, ctx, conn)
	})

	report, err := te.store.PullRenumberChildren(ctx, "", "tester")
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("PullRenumberChildren over a dirty working set = (%+v, %v), want an uncommitted-changes refusal", report, err)
	}
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		if got := headHash(t, ctx, conn); got != before {
			t.Errorf("HEAD moved from %s to %s on a refused repair", before, got)
		}
		wantLocal := map[string]string{p + ".71": "local 71", p + ".72": "local 72", p + ".73": "local 73"}
		if got := issueTitles(t, ctx, conn); !reflect.DeepEqual(got, wantLocal) {
			t.Errorf("children = %v, want unchanged %v", got, wantLocal)
		}
		var pending int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = 'rn-pending'").Scan(&pending); err != nil {
			t.Fatalf("read pending issue: %v", err)
		}
		var committed int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues AS OF 'HEAD' WHERE id = 'rn-pending'").Scan(&committed); err != nil {
			t.Fatalf("read committed pending issue: %v", err)
		}
		if pending != 1 || committed != 0 {
			t.Errorf("pending issue in working set %d, in HEAD %d; want it kept and uncommitted", pending, committed)
		}
	})
}
