//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// TestEmbeddedPullRenumberChildrenReportsMentions is bd-28sb25 AC-5: local
// text that names a renamed id is reported, by issue, field or comment, and
// left exactly as written; the report names the rename commit and the head
// the pull ended on.
func TestEmbeddedPullRenumberChildrenReportsMentions(t *testing.T) {
	te := newTestEnv(t, "rn")
	ctx := t.Context()
	p := childCollisionParent
	var commentID string
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		seedChildCollision(t, ctx, conn)
		exec := func(q string, args ...any) {
			t.Helper()
			if _, err := conn.ExecContext(ctx, q, args...); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		exec("UPDATE issues SET description = ? WHERE id = ?", "follows rn-p.72; not rn-p.721 or rn-p.72.1", p+".71")
		exec("UPDATE issues SET notes = ? WHERE id = ?", "split from rn-p.71.", p+".73")
		var err error
		commentID, _, err = issueops.InsertDerivedComment(ctx, conn, "comments", p+".73", "alice", "see rn-p.72", "2026-01-02 03:04:05")
		if err != nil {
			t.Fatalf("comment: %v", err)
		}
		exec("CALL DOLT_COMMIT('-Am', 'local clone: cross references')")
	})

	report, err := te.store.PullRenumberChildren(ctx, "", "tester")
	if err != nil {
		t.Fatalf("PullRenumberChildren: %v", err)
	}
	want := []storage.ChildIDMention{
		{IssueID: p + ".73", Field: "comment", CommentID: commentID, ID: p + ".72", RenamedTo: p + ".75"},
		{IssueID: p + ".73", Field: "notes", ID: p + ".71", RenamedTo: p + ".74"},
		{IssueID: p + ".74", Field: "description", ID: p + ".72", RenamedTo: p + ".75"},
	}
	if !reflect.DeepEqual(report.Mentions, want) {
		t.Errorf("mentions = %+v\nwant %+v", report.Mentions, want)
	}
	if report.RenameCommit == "" || report.Head == "" || report.Head == report.RenameCommit {
		t.Errorf("report commits = rename %q head %q, want both, the head past the rename", report.RenameCommit, report.Head)
	}

	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		if got := headHash(t, ctx, conn); got != report.Head {
			t.Errorf("HEAD = %s, report says %s", got, report.Head)
		}
		var desc, notes, text string
		if err := conn.QueryRowContext(ctx, "SELECT description FROM issues WHERE id = ?", p+".74").Scan(&desc); err != nil {
			t.Fatalf("read description: %v", err)
		}
		if err := conn.QueryRowContext(ctx, "SELECT notes FROM issues WHERE id = ?", p+".73").Scan(&notes); err != nil {
			t.Fatalf("read notes: %v", err)
		}
		if err := conn.QueryRowContext(ctx, "SELECT text FROM comments WHERE id = ?", commentID).Scan(&text); err != nil {
			t.Fatalf("read comment: %v", err)
		}
		if desc != "follows rn-p.72; not rn-p.721 or rn-p.72.1" || notes != "split from rn-p.71." || text != "see rn-p.72" {
			t.Errorf("text rewritten: description %q, notes %q, comment %q", desc, notes, text)
		}
	})
}
