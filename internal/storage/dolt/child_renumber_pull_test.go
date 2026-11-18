package dolt

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
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
	if report.RenameParent != localHead {
		t.Errorf("report.RenameParent = %s, want the pre-pull head %s", report.RenameParent, localHead)
	}
	if report.Retained {
		t.Error("report.Retained = true on the first pull; no earlier rename commit exists")
	}
	if !report.Merged {
		t.Error("report.Merged = false; the pinned remote head was not merged")
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
	if report.Head != head {
		t.Errorf("report.Head = %s, want the branch head the pull ended on %s", report.Head, head)
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

// seedServerChildCollision builds GH#4796's report shape on the test's own
// branch with a uniquely named file:// remote, as TestPullRenumberChildren
// does: base P (counter 70) published, the remote clone's P.71/P.72 (counter
// 72) published, then the branch rewound and the local clone's
// P.71/P.72/P.73 (counter 73) committed after localLeg adds its own rows.
// It points store at the remote and returns exec for further setup.
func seedServerChildCollision(t *testing.T, ctx context.Context, store *DoltStore, p string, localLeg func(exec func(q string, args ...any))) (exec func(q string, args ...any)) {
	t.Helper()
	db := store.db
	exec = func(q string, args ...any) {
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
	remote := fmt.Sprintf("rn%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "CALL DOLT_REMOTE('remove', ?)", remote) })

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
	if localLeg != nil {
		localLeg(exec)
	}
	exec("CALL DOLT_COMMIT('-Am', 'local clone: P.71 P.72 P.73')")

	store.remote = remote
	store.branch = branch
	return exec
}

// serverCounters reads every child_counters row under p.
func serverCounters(t *testing.T, ctx context.Context, store *DoltStore, p string) map[string]int {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, "SELECT parent_id, last_child FROM child_counters WHERE parent_id = ? OR parent_id LIKE CONCAT(?, '.%')", p, p)
	if err != nil {
		t.Fatalf("read counters: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var parent string
		var n int
		if err := rows.Scan(&parent, &n); err != nil {
			t.Fatalf("scan counter: %v", err)
		}
		out[parent] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read counters: %v", err)
	}
	return out
}

// TestPullRenumberChildrenCarriesCounters is bd-28sb25 AC-4 on the server
// store, covering #4844's high-water and durable-subtree cases (Robin
// Carswell, @rjc123) and the no-counter case together. Locally P.73 was
// deleted (counter stays 73), a session wisp holds P.74, P.71 has child
// P.71.1 and its own counter 1, and P.72 has child P.72.1 but no counter.
// The renames land above the deleted slot and the wisp (P.75, P.76), P.71's
// counter moves to P.75 with its value, and no counter is synthesized for
// P.76 or the moved descendants.
func TestPullRenumberChildrenCarriesCounters(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)

	const p = "rnc-p"
	exec := seedServerChildCollision(t, ctx, store, p, func(exec func(q string, args ...any)) {
		for _, id := range []string{p + ".71.1", p + ".72.1"} {
			exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
				"VALUES (?, ?, '', '', '', '', 'open', 2, 'task')", id, "local "+strings.TrimPrefix(id, p+"."))
		}
		exec("INSERT INTO child_counters (parent_id, last_child) VALUES (?, 1)", p+".71")
	})
	exec("DELETE FROM issues WHERE id = ?", p+".73")
	exec("CALL DOLT_COMMIT('-Am', 'local deletes P.73, counter stays 73')")
	exec("INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
		"VALUES (?, 'wisp sibling', '', '', '', '', 'open', 2, 'task')", p+".74")

	report, err := store.PullRenumberChildren(ctx, "", "tester")
	if err != nil {
		t.Fatalf("PullRenumberChildren: %v", err)
	}
	want := []storage.ChildRenumber{
		{From: p + ".71", To: p + ".75"}, {From: p + ".72", To: p + ".76"},
		{From: p + ".71.1", To: p + ".75.1"}, {From: p + ".72.1", To: p + ".76.1"},
	}
	if !reflect.DeepEqual(report.Renames, want) {
		t.Fatalf("renames = %v, want %v", report.Renames, want)
	}
	wantCounters := map[string]int{p: 76, p + ".75": 1}
	if got := serverCounters(t, ctx, store, p); !reflect.DeepEqual(got, wantCounters) {
		t.Errorf("counters = %v, want %v", got, wantCounters)
	}
	var issueAt74, wispAt74, conflicts int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", p+".74").Scan(&issueAt74); err != nil {
		t.Fatalf("count issue P.74: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", p+".74").Scan(&wispAt74); err != nil {
		t.Fatalf("count wisp P.74: %v", err)
	}
	if issueAt74 != 0 || wispAt74 != 1 {
		t.Errorf("P.74: %d issues, %d wisps, want the wisp alone", issueAt74, wispAt74)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&conflicts); err != nil {
		t.Fatalf("read conflicts: %v", err)
	}
	if conflicts != 0 {
		t.Errorf("%d tables still conflicted", conflicts)
	}
}

// TestPullRenumberChildrenRefusesDestinationCounter is bd-28sb25 AC-4 on the
// server store: a stray counter row under P.75, the destination of the
// counterless P.72, is refused after P.71 was already renamed and its counter
// relocated. The stray row needs a schema without the counter's foreign key,
// so the fixture drops it; with no cascade, it is the transaction's rollback
// that puts P.71's counter back: HEAD, ids and every counter row are as they
// were, and the journal, on for the repair, keeps none of P.71's rename.
func TestPullRenumberChildrenRefusesDestinationCounter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)

	const p = "rnd-p"
	seedServerChildCollision(t, ctx, store, p, func(exec func(q string, args ...any)) {
		exec("ALTER TABLE child_counters DROP FOREIGN KEY fk_counter_parent")
		exec("INSERT INTO child_counters (parent_id, last_child) VALUES (?, 1)", p+".71")
		exec("INSERT INTO child_counters (parent_id, last_child) VALUES (?, 9)", p+".75")
	})
	enableJournalForTest(t, store)
	clearJournal(t, store)
	var before string
	if err := store.db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&before); err != nil {
		t.Fatalf("read HEAD: %v", err)
	}

	_, err := store.PullRenumberChildren(ctx, "", "tester")
	if err == nil || !strings.Contains(err.Error(), "destination "+p+".75 already has a child counter row") {
		t.Fatalf("PullRenumberChildren err = %v, want the destination-counter refusal", err)
	}
	if rows := readJournalRows(t, store); len(rows) != 0 {
		t.Errorf("journal rows after a refused repair = %+v, want none", rows)
	}
	var after string
	if err := store.db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&after); err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	if after != before {
		t.Errorf("HEAD moved from %s to %s on a refused repair", before, after)
	}
	wantCounters := map[string]int{p: 73, p + ".71": 1, p + ".75": 9}
	if got := serverCounters(t, ctx, store, p); !reflect.DeepEqual(got, wantCounters) {
		t.Errorf("counters = %v, want unchanged %v", got, wantCounters)
	}
	var ids []string
	rows, err := store.db.QueryContext(ctx, "SELECT id FROM issues WHERE id LIKE CONCAT(?, '.%') ORDER BY id", p)
	if err != nil {
		t.Fatalf("read ids: %v", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan id: %v", err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if want := []string{p + ".71", p + ".72", p + ".73"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want unchanged %v", ids, want)
	}
	var dirty int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d tables left dirty by a refused repair", dirty)
	}
}

// TestPullRenumberChildrenJournalsRenames is bd-28sb25 on the server store
// with the events journal on: each rename journals as `bd rename` does
// (TestEventsJournal_SeamEntryPoints), its edges removed and re-added around
// the old node's delete and the new node's create. The rename transaction is
// the repair's own, so it journals only because the store scopes it.
func TestPullRenumberChildrenJournalsRenames(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)

	const p = "rnj-p"
	seedServerChildCollision(t, ctx, store, p, func(exec func(q string, args ...any)) {
		exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) " +
			"VALUES ('rnj-x', 'outside', '', '', '', '', 'open', 2, 'task')")
		exec("INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata, thread_id) "+
			"VALUES (?, ?, 'rnj-x', 'related', '2026-01-02 03:04:05', 'tester', '{}', '')", depid.New(p+".71", "rnj-x"), p+".71")
	})
	enableJournalForTest(t, store)
	clearJournal(t, store)

	if _, err := store.PullRenumberChildren(ctx, "", "tester"); err != nil {
		t.Fatalf("PullRenumberChildren: %v", err)
	}
	want := []string{
		"dep_remove:" + p + ".71->rnj-x:related", "delete:" + p + ".71", "create:" + p + ".74", "dep_add:" + p + ".74->rnj-x:related",
		"delete:" + p + ".72", "create:" + p + ".75",
	}
	if got := journalDepDeltas(t, store); !reflect.DeepEqual(got, want) {
		t.Errorf("journal = %v, want %v", got, want)
	}
}

// TestPullRenumberChildrenFetchesOverLongTimeoutConn pins the server store's
// renumbering fetch to a dedicated long-timeout connection, the route the
// plain pull's SQL transport and refreshTrackingRef take, never a pooled
// session with its 10s read timeout. store.connStr, which only that
// dedicated connection reads, is pointed at an address that cannot resolve
// while the pool stays healthy: a fetch over the pool would succeed and
// repair, the dedicated route fails before anything is renamed.
func TestPullRenumberChildrenFetchesOverLongTimeoutConn(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)

	const p = "rnt-p"
	seedServerChildCollision(t, ctx, store, p, nil)
	var before string
	if err := store.db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&before); err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	// An RFC 2606 .invalid name fails DNS fast and for good (see
	// TestHistory_UsesDedicatedLongTimeoutConnection).
	const unreachable = "bd-28sb25-fetch-route.invalid"
	cfg, err := mysql.ParseDSN(store.connStr)
	if err != nil {
		t.Fatalf("parse store.connStr: %v", err)
	}
	connStr := store.connStr
	cfg.Addr = unreachable + ":3306"
	store.connStr = cfg.FormatDSN()

	_, err = store.PullRenumberChildren(ctx, "", "tester")
	store.connStr = connStr
	// The is_blocked recompute after a merge dials the same address, so the
	// error must be the fetch's own.
	if err == nil || !strings.Contains(err.Error(), "fetch from "+store.remote) || !strings.Contains(err.Error(), unreachable) {
		t.Fatalf("PullRenumberChildren = %v, want the fetch to fail dialing %s: it must not run on the pool", err, unreachable)
	}
	var after string
	if err := store.db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&after); err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	if after != before {
		t.Errorf("HEAD moved from %s to %s on a failed fetch", before, after)
	}
	var renamed int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id IN (?, ?)", p+".74", p+".75").Scan(&renamed); err != nil {
		t.Fatalf("count renamed children: %v", err)
	}
	if renamed != 0 {
		t.Errorf("%d children renamed although the fetch failed", renamed)
	}
}

// TestApplyChildRenumbersRefusesConcurrentWrites is bd-28sb25 AC-7 on the
// server: the plan is made on one pinned session, then a second session
// commits a new child at a planned destination (HEAD moves) or leaves an
// unrelated uncommitted edit. The apply, re-validating inside its
// transaction, refuses either way: nothing is renamed, no rename commit
// lands, and the other session's work is kept as it left it.
func TestApplyChildRenumbersRefusesConcurrentWrites(t *testing.T) {
	cases := []struct {
		name    string
		between func(exec func(q string, args ...any), p string)
		wantErr string
		wantIDs []string
		dirty   bool
	}{
		{
			name: "head_moved",
			between: func(exec func(q string, args ...any), p string) {
				exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
					"VALUES (?, 'concurrent 74', '', '', '', '', 'open', 2, 'task')", p+".74")
				exec("CALL DOLT_COMMIT('-Am', 'concurrent child P.74')")
			},
			wantErr: "the branch moved since the plan",
			wantIDs: []string{".71", ".72", ".73", ".74"},
		},
		{
			name: "unrelated_dirty_write",
			between: func(exec func(q string, args ...any), p string) {
				exec("UPDATE issues SET notes = 'written after the plan' WHERE id = ?", p)
			},
			wantErr: "uncommitted changes",
			wantIDs: []string{".71", ".72", ".73"},
			dirty:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			t.Cleanup(cleanup)
			ctx, cancel := testContext(t)
			t.Cleanup(cancel)

			const p = "rnc-p"
			seedServerChildCollision(t, ctx, store, p, nil)
			// The store's pool holds one session, which conn pins; the other
			// writer is a raw session on the same test branch.
			peer := rawTestConn(t, testServerPort)
			defer func() { _ = peer.Close() }()
			other, err := peer.Conn(ctx)
			if err != nil {
				t.Fatalf("open second session: %v", err)
			}
			defer func() { _ = other.Close() }()
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := other.ExecContext(ctx, q, args...); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			exec("USE `" + testSharedDB + "/" + store.branch + "`")
			conn, err := store.db.Conn(ctx)
			if err != nil {
				t.Fatalf("pin session: %v", err)
			}
			defer func() { _ = conn.Close() }()

			report, err := versioncontrolops.PlanChildRenumbers(ctx, conn, store.branch, store.remote+"/"+store.branch)
			if err != nil {
				t.Fatalf("PlanChildRenumbers: %v", err)
			}
			if want := []storage.ChildRenumber{{From: p + ".71", To: p + ".74"}, {From: p + ".72", To: p + ".75"}}; !reflect.DeepEqual(report.Renames, want) {
				t.Fatalf("plan = %v, want %v", report.Renames, want)
			}
			tc.between(exec, p)
			var before string
			if err := conn.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&before); err != nil {
				t.Fatalf("read HEAD: %v", err)
			}

			err = versioncontrolops.ApplyChildRenumbers(ctx, conn, report, "tester", store.scopeEventsJournalTransaction)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ApplyChildRenumbers = %v, want an error naming %q", err, tc.wantErr)
			}
			// Release conn so the reads below can use the store's pool.
			_ = conn.Close()
			var after string
			if err := store.db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&after); err != nil {
				t.Fatalf("read HEAD: %v", err)
			}
			if after != before {
				t.Errorf("HEAD moved from %s to %s on a refused repair", before, after)
			}
			var ids []string
			rows, err := store.db.QueryContext(ctx, "SELECT id FROM issues WHERE id LIKE CONCAT(?, '.%') ORDER BY id", p)
			if err != nil {
				t.Fatalf("read ids: %v", err)
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatalf("scan id: %v", err)
				}
				ids = append(ids, strings.TrimPrefix(id, p))
			}
			_ = rows.Close()
			if !reflect.DeepEqual(ids, tc.wantIDs) {
				t.Errorf("ids = %v, want %v", ids, tc.wantIDs)
			}
			if got, want := serverCounters(t, ctx, store, p), map[string]int{p: 73}; !reflect.DeepEqual(got, want) {
				t.Errorf("counters = %v, want unchanged %v", got, want)
			}
			var dirty int
			if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
				t.Fatalf("read status: %v", err)
			}
			if (dirty > 0) != tc.dirty {
				t.Errorf("%d dirty tables after the refusal, want dirty=%v", dirty, tc.dirty)
			}
			if tc.dirty {
				var notes string
				if err := store.db.QueryRowContext(ctx, "SELECT notes FROM issues WHERE id = ?", p).Scan(&notes); err != nil {
					t.Fatalf("read notes: %v", err)
				}
				if notes != "written after the plan" {
					t.Errorf("the other session's uncommitted edit = %q, want it kept", notes)
				}
			}
		})
	}
}
