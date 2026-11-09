package dolt

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// TestPullRenumberChildren is bd-28sb25 AC-3 on the server store: GH#4796's
// report shape (base counter 70; the remote clone mints P.71/P.72, the local
// clone P.71/P.72/P.73), pulled with --renumber-children, keeps the remote's
// P.71/P.72, moves the local ones to P.74/P.75 with their labels, leaves P.73
// alone, settles the counter at 75 and leaves no conflicts.
func TestPullRenumberChildren(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)
	db := store.db

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	insertIssue := func(id, title string) {
		t.Helper()
		exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES (?, ?, '', '', '', '', 'open', 2, 'task')", id, title)
	}
	var branch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&branch); err != nil {
		t.Fatalf("read branch: %v", err)
	}
	// Remotes are database-wide and every setupTestStore test shares the
	// database, so the remote gets a name of its own.
	remote := fmt.Sprintf("rn%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "CALL DOLT_REMOTE('remove', ?)", remote) })

	const p = "rn-p"
	insertIssue(p, "epic")
	exec("INSERT INTO child_counters (parent_id, last_child) VALUES (?, 70)", p)
	exec("CALL DOLT_COMMIT('-Am', 'seed: counter 70')")
	var base string
	if err := db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&base); err != nil {
		t.Fatalf("read base: %v", err)
	}
	exec("CALL DOLT_REMOTE('add', ?, ?)", remote, "file://"+filepath.Join(t.TempDir(), "remote"))
	exec("CALL DOLT_PUSH(?, ?)", remote, branch)

	insertIssue(p+".71", "remote 71")
	insertIssue(p+".72", "remote 72")
	exec("UPDATE child_counters SET last_child = 72 WHERE parent_id = ?", p)
	exec("CALL DOLT_COMMIT('-Am', 'remote clone: P.71 P.72')")
	exec("CALL DOLT_PUSH(?, ?)", remote, branch)

	exec("CALL DOLT_RESET('--hard', ?)", base)
	insertIssue(p+".71", "local 71")
	insertIssue(p+".72", "local 72")
	insertIssue(p+".73", "local 73")
	exec("UPDATE child_counters SET last_child = 73 WHERE parent_id = ?", p)
	exec("INSERT INTO labels (issue_id, label) VALUES (?, 'local-71')", p+".71")
	exec("CALL DOLT_COMMIT('-Am', 'local clone: P.71 P.72 P.73')")

	store.remote = remote
	store.branch = branch
	report, err := store.PullRenumberChildren(ctx, "", "tester")
	if err != nil {
		t.Fatalf("PullRenumberChildren: %v", err)
	}
	want := []storage.ChildRenumber{{From: p + ".71", To: p + ".74"}, {From: p + ".72", To: p + ".75"}}
	if !reflect.DeepEqual(report.Renames, want) {
		t.Errorf("renames = %v, want %v", report.Renames, want)
	}
	if report.RenameCommit == "" || !report.Merged || report.Remote != remote {
		t.Errorf("report = %+v, want a rename commit, a merge, remote %s", report, remote)
	}

	titles := map[string]string{}
	rows, err := db.QueryContext(ctx, "SELECT id, title FROM issues WHERE id LIKE 'rn-p.%'")
	if err != nil {
		t.Fatalf("read children: %v", err)
	}
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			t.Fatalf("scan child: %v", err)
		}
		titles[id] = title
	}
	_ = rows.Close()
	wantTitles := map[string]string{
		p + ".71": "remote 71", p + ".72": "remote 72", p + ".73": "local 73",
		p + ".74": "local 71", p + ".75": "local 72",
	}
	if !reflect.DeepEqual(titles, wantTitles) {
		t.Errorf("children = %v, want %v", titles, wantTitles)
	}
	var labelled string
	if err := db.QueryRowContext(ctx, "SELECT issue_id FROM labels WHERE label = 'local-71'").Scan(&labelled); err != nil {
		t.Fatalf("read label: %v", err)
	}
	if labelled != p+".74" {
		t.Errorf("local 71's label is on %s, want %s", labelled, p+".74")
	}
	var counter, conflicts int
	if err := db.QueryRowContext(ctx, "SELECT last_child FROM child_counters WHERE parent_id = ?", p).Scan(&counter); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if counter != 75 {
		t.Errorf("counter = %d, want 75", counter)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&conflicts); err != nil {
		t.Fatalf("read conflicts: %v", err)
	}
	if conflicts != 0 {
		t.Errorf("%d tables still conflicted", conflicts)
	}
}
