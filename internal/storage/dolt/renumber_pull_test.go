package dolt

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// TestRenumberCollidingChildren (GH#4796, bd-28sb25 AC-3): the report fixture
// under the authorized repair, on the server-backed store. Two clones each
// mint children under the same parent offline — the local database holds
// bd-rnp.71/.72/.73 (counter 73), the peer published bd-rnp.71/.72 with its
// own content (counter 72), common ancestor at counter 70 — so the next pull
// is unmergeable. PullWithChildRenumbers renames the colliding local
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

	result, err := store.PullWithChildRenumbers(ctx, "origin")
	if err != nil {
		t.Fatalf("PullWithChildRenumbers: %v", err)
	}
	if result == nil {
		t.Fatal("PullWithChildRenumbers returned a nil result on success")
	}
	if result.Retained {
		t.Errorf("result.Retained = true; the first invocation has no earlier repair to retain")
	}
	if result.RenameCommit == "" {
		t.Fatal("result.RenameCommit is empty; the renames were not committed")
	}
	if result.RenameCommit == localHead {
		t.Errorf("result.RenameCommit = localHead %s; no rename commit was created", localHead)
	}
	want := []storage.ChildRenumberMapping{
		{OldID: "bd-rnp.71", NewID: "bd-rnp.74", Root: true},
		{OldID: "bd-rnp.72", NewID: "bd-rnp.75", Root: true},
	}
	if result.Plan == nil || len(result.Plan.Mappings) != len(want) {
		t.Fatalf("result plan mappings = %+v, want %+v", result.Plan, want)
	}
	for i, m := range want {
		if result.Plan.Mappings[i] != m {
			t.Errorf("result mapping %d = %+v, want %+v", i, result.Plan.Mappings[i], m)
		}
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

	var head string
	if err := store.db.QueryRowContext(ctx,
		"SELECT hash FROM dolt_branches WHERE name = 'main'").Scan(&head); err != nil {
		t.Fatalf("read branch head: %v", err)
	}
	if head == localHead || head == result.RenameCommit {
		t.Errorf("branch head = %s; the merge of the remote revision never advanced past the local leg (%s) or the rename commit (%s)",
			head, localHead, result.RenameCommit)
	}
}
