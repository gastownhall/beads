//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/rowid"
)

// sharedRowTime is when both clones wrote their identical comment and edge.
const sharedRowTime = "2026-01-02 03:04:05"

// TestEmbeddedPullRenumberChildrenCarriesGraph is bd-28sb25 AC-4. Both clones
// add P.71 and P.72 (the report shape, plus a colliding descendant P.72.1)
// and give each the same derived-id rows: an identical comment, and the same
// outbound edge P.71 -> X. Locally P.71 also has an inbound edge and a
// child counter of 1 with no live child; P.72 has the counter 1 its child
// P.72.1 set.
//
// After the repair the local graph follows the local identities (P.74,
// P.75, P.75.1) and the remote's keeps the remote ones; every derived key
// matches its row, so the result merges and pushes without conflicts or
// duplicates; and each renamed node's counter moved with it, so the next
// child of P.74 is P.74.2.
func TestEmbeddedPullRenumberChildrenCarriesGraph(t *testing.T) {
	te := newTestEnv(t, "rn")
	ctx := t.Context()
	p := childCollisionParent
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
		addDep := func(from, to, kind string) {
			t.Helper()
			exec("INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata, thread_id) "+
				"VALUES (?, ?, ?, ?, ?, 'tester', '{}', '')", depid.New(from, to), from, to, kind, sharedRowTime)
		}
		addComment := func(issueID string) {
			t.Helper()
			if _, _, err := issueops.InsertDerivedComment(ctx, conn, "comments", issueID, "alice", "same note", sharedRowTime); err != nil {
				t.Fatalf("comment on %s: %v", issueID, err)
			}
		}
		// Rows both clones write identically under P.71 and P.72.
		sharedChildren := func(side string) {
			insertIssue(p+".71", side+" 71")
			insertIssue(p+".72", side+" 72")
			insertIssue(p+".72.1", side+" 72.1")
			addDep(p+".72.1", p+".72", "parent-child")
			addDep(p+".71", "rn-x", "related")
			addComment(p + ".71")
			exec("INSERT INTO child_counters (parent_id, last_child) VALUES (?, 1)", p+".72")
		}

		insertIssue(p, "epic")
		insertIssue("rn-x", "outside")
		exec("INSERT INTO child_counters (parent_id, last_child) VALUES (?, 70)", p)
		exec("CALL DOLT_COMMIT('-Am', 'seed: counter 70')")
		var base string
		if err := conn.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&base); err != nil {
			t.Fatalf("read base: %v", err)
		}
		exec("CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote"))
		exec("CALL DOLT_PUSH('origin', 'main')")

		sharedChildren("remote")
		exec("UPDATE child_counters SET last_child = 72 WHERE parent_id = ?", p)
		exec("CALL DOLT_COMMIT('-Am', 'remote clone')")
		exec("CALL DOLT_PUSH('origin', 'main')")

		exec("CALL DOLT_RESET('--hard', ?)", base)
		sharedChildren("local")
		// A second, byte-identical local comment: duplicates are legal and
		// take the next ordinal of the same digest (glm's multiplicity case).
		exec("INSERT INTO comments (id, issue_id, author, text, created_at) VALUES (?, ?, 'alice', 'same note', ?)",
			commentRowID(p+".71", 1), p+".71", sharedRowTime)
		insertIssue(p+".73", "local 73")
		addDep("rn-x", p+".71", "blocks")
		exec("INSERT INTO child_counters (parent_id, last_child) VALUES (?, 1)", p+".71")
		exec("UPDATE child_counters SET last_child = 73 WHERE parent_id = ?", p)
		exec("CALL DOLT_COMMIT('-Am', 'local clone')")
	})

	report, err := te.store.PullRenumberChildren(ctx, "", "tester")
	if err != nil {
		t.Fatalf("PullRenumberChildren: %v", err)
	}
	wantRenames := []storage.ChildRenumber{
		{From: p + ".71", To: p + ".74"}, {From: p + ".72", To: p + ".75"}, {From: p + ".72.1", To: p + ".75.1"},
	}
	if !reflect.DeepEqual(report.Renames, wantRenames) {
		t.Fatalf("renames = %v, want %v", report.Renames, wantRenames)
	}

	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		wantTitles := map[string]string{
			p + ".71": "remote 71", p + ".72": "remote 72", p + ".72.1": "remote 72.1", p + ".73": "local 73",
			p + ".74": "local 71", p + ".75": "local 72", p + ".75.1": "local 72.1",
		}
		if got := issueTitles(t, ctx, conn); !reflect.DeepEqual(got, wantTitles) {
			t.Errorf("children = %v, want %v", got, wantTitles)
		}

		wantDeps := []string{
			p + ".71 related rn-x", p + ".72.1 parent-child " + p + ".72",
			p + ".74 related rn-x", p + ".75.1 parent-child " + p + ".75",
			"rn-x blocks " + p + ".74",
		}
		if got := renumberedEdges(t, ctx, conn); !reflect.DeepEqual(got, wantDeps) {
			t.Errorf("edges = %v, want %v", got, wantDeps)
		}

		// The remote's comment stays on P.71 and both local copies moved to
		// P.74, each keyed by the id its own row derives at its ordinal:
		// multiplicity survives the re-key and nothing collapses on merge.
		rows, err := conn.QueryContext(ctx, "SELECT id, issue_id FROM comments WHERE text = 'same note'")
		if err != nil {
			t.Fatalf("read comments: %v", err)
		}
		commentIDs := map[string][]string{}
		for rows.Next() {
			var id, issueID string
			if err := rows.Scan(&id, &issueID); err != nil {
				t.Fatalf("scan comment: %v", err)
			}
			commentIDs[issueID] = append(commentIDs[issueID], id)
		}
		_ = rows.Close()
		for _, ids := range commentIDs {
			sort.Strings(ids)
		}
		wantComments := map[string][]string{
			p + ".71": {commentRowID(p+".71", 0)},
			p + ".74": {commentRowID(p+".74", 0), commentRowID(p+".74", 1)},
		}
		for _, ids := range wantComments {
			sort.Strings(ids)
		}
		if !reflect.DeepEqual(commentIDs, wantComments) {
			t.Errorf("comment ids = %v, want %v", commentIDs, wantComments)
		}

		wantCounters := map[string]int{p: 75, p + ".72": 1, p + ".74": 1, p + ".75": 1}
		for parent, want := range wantCounters {
			if n, ok := counterValue(t, ctx, conn, parent); !ok || n != want {
				t.Errorf("counter %s = %d (present %v), want %d", parent, n, ok, want)
			}
		}
		for _, gone := range []string{p + ".71"} {
			if n, ok := counterValue(t, ctx, conn, gone); ok {
				t.Errorf("counter %s = %d survived its rename", gone, n)
			}
		}
	})

	next, err := te.store.GetNextChildID(ctx, p+".74")
	if err != nil {
		t.Fatalf("GetNextChildID: %v", err)
	}
	if next != p+".74.2" {
		t.Errorf("next child of %s = %s, want %s.2", p+".74", next, p+".74")
	}

	// The result is an ordinary descendant of the remote: it pushes, and a
	// second plain pull has nothing left to merge.
	if err := te.store.Push(ctx); err != nil {
		t.Fatalf("push after repair: %v", err)
	}
	if err := te.store.Pull(ctx); err != nil {
		t.Fatalf("pull after repair: %v", err)
	}
}

// renumberedEdges lists the dependency edges touching the fixture as
// "source type target", after checking each row's derived id.
func renumberedEdges(t *testing.T, ctx context.Context, conn *sql.Conn) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx,
		"SELECT id, issue_id, type, depends_on_issue_id FROM dependencies WHERE issue_id LIKE 'rn-%' AND depends_on_issue_id IS NOT NULL")
	if err != nil {
		t.Fatalf("read edges: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, from, kind, to string
		if err := rows.Scan(&id, &from, &kind, &to); err != nil {
			t.Fatalf("scan edge: %v", err)
		}
		if want := depid.New(from, to); id != want {
			t.Errorf("edge %s -> %s keyed %s, want %s", from, to, id, want)
		}
		out = append(out, from+" "+kind+" "+to)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read edges: %v", err)
	}
	sort.Strings(out)
	return out
}

// commentRowID is the derived id of the fixture's shared comment on issueID
// at ordinal n.
func commentRowID(issueID string, n int) string {
	return rowid.New("comments", n, rowid.Digest([]sql.NullString{
		{String: issueID, Valid: true}, {String: "alice", Valid: true},
		{String: "same note", Valid: true}, {String: sharedRowTime, Valid: true},
	}))
}
