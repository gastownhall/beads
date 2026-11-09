package dolt

import (
	"context"
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

// TestRenumberCollidingChildren (GH#4796, bd-28sb25 AC-3): the report fixture
// under the authorized repair, on the server-backed store, with the remote leg
// minted by a real second database rather than a reset of this one. Two clones each
// mint children under the same parent offline — the local database holds
// bd-rnp.71/.72/.73 (counter 73), the peer published bd-rnp.71/.72 with its
// own content (counter 72), common ancestor at counter 70 — so the next pull
// is unmergeable. PullRenumberChildren renames the colliding local
// children to bd-rnp.74/.75 in one commit and merges the remote revision
// clean: both sides' children survive under distinct ids, the untouched
// .73 stays, and the counter settles at the raised floor 75.
func TestRenumberCollidingChildren(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)
	peerDB := dbName + "_peer"

	store, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", peerDB))
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	mustExec := func(stage, query string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}
	seedChild := func(id, title string) {
		t.Helper()
		mustExec("seed "+id, "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES (?, ?, '', '', '', '', 'open', 2, 'task')", id, title)
	}

	// C0: the shared ancestor — parent with counter 70 and no children.
	mustExec("seed parent", "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
		"VALUES ('bd-rnp', 'parent epic', '', '', '', '', 'open', 2, 'epic')")
	mustExec("seed counter 70", "INSERT INTO child_counters (parent_id, last_child) VALUES ('bd-rnp', 70)")
	mustExec("commit C0", "CALL DOLT_COMMIT('-Am', 'test: seed ancestor counter 70 (C0)')")

	remoteURL := "file://" + filepath.Join(tmpDir, "renumber-remote")
	mustExec("add remote", "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
	mustExec("push C0", "CALL DOLT_PUSH('origin', 'main')")

	// The local clone's leg: children 71-73, counter 73.
	seedChild("bd-rnp.71", "local child 71")
	seedChild("bd-rnp.72", "local child 72")
	seedChild("bd-rnp.73", "local child 73")
	mustExec("local counter 73", "UPDATE child_counters SET last_child = 73 WHERE parent_id = 'bd-rnp'")
	mustExec("commit local leg", "CALL DOLT_COMMIT('-Am', 'test: local children 71-73')")

	// The other clone: a real database cloned from the remote, publishing its
	// own children 71-72 with distinct content, counter 72.
	mustExec("clone peer", "CALL DOLT_CLONE(?, ?)", remoteURL, peerDB)
	peer := openPeerDB(t, store.connStr, peerDB)
	defer peer.Close()
	mustPeerExec := func(stage, query string, args ...any) {
		t.Helper()
		if _, err := peer.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("peer %s: %v", stage, err)
		}
	}
	mustPeerExec("seed remote .71", "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
		"VALUES ('bd-rnp.71', 'remote child 71', '', '', '', '', 'open', 2, 'task')")
	mustPeerExec("seed remote .72", "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
		"VALUES ('bd-rnp.72', 'remote child 72', '', '', '', '', 'open', 2, 'task')")
	mustPeerExec("remote counter 72", "UPDATE child_counters SET last_child = 72 WHERE parent_id = 'bd-rnp'")
	mustPeerExec("commit remote leg", "CALL DOLT_COMMIT('-Am', 'test: remote children 71-72')")
	mustPeerExec("push remote leg", "CALL DOLT_PUSH('origin', 'main')")

	store.remote = "origin"
	store.branch = "main"

	var localHead string
	if err := store.db.QueryRowContext(ctx,
		"SELECT hash FROM dolt_branches WHERE name = 'main'").Scan(&localHead); err != nil {
		t.Fatalf("read local head: %v", err)
	}

	report, err := store.PullRenumberChildren(ctx, "origin", "tester")
	if err != nil {
		t.Fatalf("PullRenumberChildren: %v", err)
	}
	if report.RenameCommit == "" {
		t.Fatal("report.RenameCommit is empty; the renames were not committed")
	}
	if report.RenameCommit == localHead {
		t.Errorf("report.RenameCommit = localHead %s; no rename commit was created", localHead)
	}
	want := []storage.ChildRenumber{
		{From: "bd-rnp.71", To: "bd-rnp.74"},
		{From: "bd-rnp.72", To: "bd-rnp.75"},
	}
	if !reflect.DeepEqual(report.Renames, want) {
		t.Errorf("renames = %+v, want %+v", report.Renames, want)
	}
	if report.LocalHead != localHead {
		t.Errorf("report.LocalHead = %s, want the pre-pull head %s", report.LocalHead, localHead)
	}

	// The merged database: the remote's .71/.72, the renamed .74/.75, and the
	// untouched .73, each carrying its own side's content.
	for id, title := range map[string]string{
		"bd-rnp.71": "remote child 71",
		"bd-rnp.72": "remote child 72",
		"bd-rnp.73": "local child 73",
		"bd-rnp.74": "local child 71",
		"bd-rnp.75": "local child 72",
	} {
		var got string
		if err := store.db.QueryRowContext(ctx,
			"SELECT title FROM issues WHERE id = ?", id).Scan(&got); err != nil {
			t.Fatalf("read %s after repair: %v", id, err)
		}
		if got != title {
			t.Errorf("title of %s = %q, want %q", id, got, title)
		}
	}

	var last int
	if err := store.db.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = 'bd-rnp'").Scan(&last); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if last != 75 {
		t.Errorf("counter after repair = %d, want the raised floor 75", last)
	}

	var conflicts int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&conflicts); err != nil {
		t.Fatalf("count dolt_conflicts: %v", err)
	}
	if conflicts != 0 {
		t.Errorf("dolt_conflicts after repair = %d, want 0", conflicts)
	}

	// Ancestry: the rename commit sits directly on the pre-pull head, and the
	// head the pull ends on merges it with the pinned remote head.
	var head string
	if err := store.db.QueryRowContext(ctx,
		"SELECT hash FROM dolt_branches WHERE name = 'main'").Scan(&head); err != nil {
		t.Fatalf("read branch head: %v", err)
	}
	var renameParent string
	if err := store.db.QueryRowContext(ctx,
		"SELECT parent_hash FROM dolt_commit_ancestors WHERE commit_hash = ?", report.RenameCommit).Scan(&renameParent); err != nil {
		t.Fatalf("read rename commit parent: %v", err)
	}
	if renameParent != localHead {
		t.Errorf("rename commit parent = %s, want the pre-pull head %s", renameParent, localHead)
	}
	parents := map[string]bool{}
	rows, err := store.db.QueryContext(ctx,
		"SELECT parent_hash FROM dolt_commit_ancestors WHERE commit_hash = ?", head)
	if err != nil {
		t.Fatalf("read merge parents: %v", err)
	}
	for rows.Next() {
		var parent string
		if err := rows.Scan(&parent); err != nil {
			t.Fatalf("scan merge parent: %v", err)
		}
		parents[parent] = true
	}
	_ = rows.Close()
	if !parents[report.RenameCommit] || !parents[report.RemoteHead] || len(parents) != 2 {
		t.Errorf("merge %s parents = %v, want the rename commit %s and the pinned remote head %s",
			head, parents, report.RenameCommit, report.RemoteHead)
	}
}
