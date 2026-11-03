package dolt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// configureFederationExcludeTypes sets federation.exclude_types for one test,
// ignoring the repository's tracked config so only these values apply.
func configureFederationExcludeTypes(t *testing.T, excludeTypes ...string) {
	t.Helper()
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
	config.Set("federation.exclude_types", excludeTypes)
}

// filteredSyncFixture is a store whose peer already holds an excluded-type
// issue, plus a new local excluded issue with a label, comment and dependency
// and a leaked ephemeral row in the committed issues table. The peer is a
// server-local file:// remote, so it works against a containerized server.
type filteredSyncFixture struct {
	store                           *DoltStore
	peer, remoteURL                 string
	task, peerChore, newChore, wisp string
}

func newFilteredSyncFixture(t *testing.T, ctx context.Context) *filteredSyncFixture {
	t.Helper()
	store, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup)
	if err := store.db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&store.branch); err != nil {
		t.Fatalf("align store source branch: %v", err)
	}
	f := &filteredSyncFixture{
		store:     store,
		peer:      "peer",
		task:      "test-task",
		peerChore: "test-peerchore",
		newChore:  "test-newchore",
		wisp:      "test-leak",
	}
	remoteDir := "/tmp/" + uniqueTestDBName(t) + "_remote"
	t.Cleanup(func() { _ = os.RemoveAll(remoteDir) })
	f.remoteURL = "file://" + remoteDir
	// A credential-free remote: a federation_peers row would get its
	// last_sync rewritten by Sync's own fetch, after the pre-pull commit
	// (TestFilteredSyncRegisteredPeer covers that path).
	if err := store.AddRemote(ctx, f.peer, f.remoteURL); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}

	// The peer already holds an excluded-type issue.
	f.create(t, ctx, &types.Issue{ID: f.peerChore, Title: "held by peer", IssueType: types.TypeChore, Status: types.StatusOpen, Priority: 2})
	f.commit(t, ctx, "seed peer chore")
	if err := store.PushTo(ctx, f.peer); err != nil {
		t.Fatalf("seed peer: %v", err)
	}

	f.create(t, ctx, &types.Issue{ID: f.task, Title: "shared task", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2})
	f.create(t, ctx, &types.Issue{ID: f.newChore, Title: "local chore", IssueType: types.TypeChore, Status: types.StatusOpen, Priority: 2})
	if err := store.AddLabel(ctx, f.newChore, "local-label", "test"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if _, err := store.AddIssueComment(ctx, f.newChore, "test", "local comment"); err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: f.task, DependsOnID: f.newChore, Type: types.DepBlocks}, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	// An ephemeral row that leaked into the committed issues table.
	if _, err := store.db.ExecContext(ctx, `INSERT INTO issues
		(id, title, description, design, acceptance_criteria, notes, issue_type, status, priority, ephemeral)
		VALUES (?, 'leaked wisp', '', '', '', '', 'task', 'open', 2, 1)`, f.wisp); err != nil {
		t.Fatalf("insert ephemeral row: %v", err)
	}
	f.commit(t, ctx, "local excluded data")
	return f
}

func (f *filteredSyncFixture) create(t *testing.T, ctx context.Context, issue *types.Issue) {
	t.Helper()
	if err := f.store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
	}
}

func (f *filteredSyncFixture) commit(t *testing.T, ctx context.Context, msg string) {
	t.Helper()
	if _, err := f.store.CommitAll(ctx, msg); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

// peerTip refreshes and returns the remote-tracking ref of the peer's branch.
func (f *filteredSyncFixture) peerTip(t *testing.T, ctx context.Context) string {
	t.Helper()
	if err := f.store.Fetch(ctx, f.peer); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	return "remotes/" + f.peer + "/" + f.store.branch
}

func (f *filteredSyncFixture) countAt(t *testing.T, ctx context.Context, ref, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.store.db.QueryRowContext(ctx, fmt.Sprintf(query, "AS OF '"+ref+"'"), args...).Scan(&n); err != nil {
		t.Fatalf("count at %s: %v", ref, err)
	}
	return n
}

func (f *filteredSyncFixture) issueCount(t *testing.T, ctx context.Context, ref, id string) int {
	return f.countAt(t, ctx, ref, "SELECT COUNT(*) FROM issues %s WHERE id = ?", id)
}

func (f *filteredSyncFixture) childRows(t *testing.T, ctx context.Context, ref, id string) int {
	return f.countAt(t, ctx, ref, "SELECT COUNT(*) FROM labels %s WHERE issue_id = ?", id) +
		f.countAt(t, ctx, ref, "SELECT COUNT(*) FROM comments %s WHERE issue_id = ?", id) +
		f.countAt(t, ctx, ref, "SELECT COUNT(*) FROM dependencies %s WHERE issue_id = ? OR depends_on_issue_id = ?", id, id)
}

// A filtered sync withholds the new local excluded issue and its rows from the
// peer while leaving the excluded issue the peer already holds in place.
func TestFilteredSyncExcludesTypes(t *testing.T) {
	ctx, cancel := testContext(t)
	defer cancel()
	f := newFilteredSyncFixture(t, ctx)
	configureFederationExcludeTypes(t, "chore", "wisp")

	res, err := f.store.Sync(ctx, f.peer, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.PushError != nil || !res.Pushed || !res.PushFiltered {
		t.Fatalf("Sync pushed=%v filtered=%v push error=%v, want a filtered publication", res.Pushed, res.PushFiltered, res.PushError)
	}

	tip := f.peerTip(t, ctx)
	if f.issueCount(t, ctx, tip, f.task) != 1 {
		t.Fatal("peer tip lacks the shared task; the publication did not land")
	}
	if n := f.issueCount(t, ctx, tip, f.newChore); n != 0 {
		t.Errorf("peer tip holds the new excluded issue (%d rows)", n)
	}
	if n := f.childRows(t, ctx, tip, f.newChore); n != 0 {
		t.Errorf("peer tip holds %d label/comment/dependency rows of the new excluded issue", n)
	}
	if n := f.issueCount(t, ctx, tip, f.wisp); n != 0 {
		t.Errorf("peer tip holds the leaked ephemeral row (%d rows)", n)
	}
	if n := f.issueCount(t, ctx, tip, f.peerChore); n != 1 {
		t.Errorf("peer tip lost the excluded issue it already held (%d rows)", n)
	}

	src := f.store.branch
	if f.issueCount(t, ctx, src, f.newChore) != 1 || f.childRows(t, ctx, src, f.newChore) != 3 || f.issueCount(t, ctx, src, f.wisp) != 1 {
		t.Error("filtered sync removed local excluded data")
	}
}

// checkFilterAncestryAtPush fails each filtered publication whose filter commit
// is not yet an ancestor of the source branch, and returns a count of the
// filtered publications it checked.
func checkFilterAncestryAtPush(t *testing.T) *int {
	t.Helper()
	checked := new(int)
	t.Cleanup(versioncontrolops.SetFilteredPushStageHookForTest(func(ctx context.Context, stage string, conn versioncontrolops.DBConn, req versioncontrolops.FilteredPushRequest) error {
		if stage != "push" {
			return nil
		}
		var head, msg string
		if err := conn.QueryRowContext(ctx, fmt.Sprintf("SELECT commit_hash, message FROM dolt_log('%s') LIMIT 1", req.Staging)).Scan(&head, &msg); err != nil {
			return err
		}
		if msg != versioncontrolops.FilteredPushCommitMsg {
			return nil
		}
		*checked++
		var n int
		if err := conn.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM dolt_log('%s') WHERE commit_hash = ?", req.Source), head).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("filter commit %s is not an ancestor of %s at publication", head, req.Source)
		}
		return nil
	}))
	return checked
}

func (f *filteredSyncFixture) sync(t *testing.T, ctx context.Context) {
	t.Helper()
	res, err := f.store.Sync(ctx, f.peer, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.PushError != nil {
		t.Fatalf("Sync push error: %v", res.PushError)
	}
}

// Later syncs keep the sender's excluded data: the filter commit is an ancestor
// of the source when it is published, so merging the peer's tip back cannot
// delete the withheld rows.
func TestFilteredSyncKeepsLocalExcludedIssues(t *testing.T) {
	ctx, cancel := testContext(t)
	defer cancel()
	f := newFilteredSyncFixture(t, ctx)
	configureFederationExcludeTypes(t, "chore", "wisp")
	checked := checkFilterAncestryAtPush(t)

	f.sync(t, ctx)
	// Edit the excluded issue locally and add a shared one between syncs.
	if _, err := f.store.AddIssueComment(ctx, f.newChore, "test", "second comment"); err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}
	if err := f.store.UpdateIssue(ctx, f.newChore, map[string]interface{}{"title": "local chore, edited"}, "test"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	task2 := "test-task2"
	f.create(t, ctx, &types.Issue{ID: task2, Title: "second shared task", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2})
	f.commit(t, ctx, "second round")
	f.sync(t, ctx)
	f.sync(t, ctx)

	if *checked < 2 {
		t.Fatalf("checked %d filtered publications, want 2", *checked)
	}
	src := f.store.branch
	if f.issueCount(t, ctx, src, f.newChore) != 1 || f.childRows(t, ctx, src, f.newChore) != 4 || f.issueCount(t, ctx, src, f.wisp) != 1 {
		t.Errorf("later sync removed local excluded data: chore=%d rows=%d wisp=%d",
			f.issueCount(t, ctx, src, f.newChore), f.childRows(t, ctx, src, f.newChore), f.issueCount(t, ctx, src, f.wisp))
	}
	tip := f.peerTip(t, ctx)
	if f.issueCount(t, ctx, tip, task2) != 1 {
		t.Fatal("peer tip lacks the second shared task")
	}
	if f.issueCount(t, ctx, tip, f.newChore) != 0 || f.childRows(t, ctx, tip, f.newChore) != 0 || f.issueCount(t, ctx, tip, f.wisp) != 0 {
		t.Error("peer tip holds withheld data after the later sync")
	}
}

// peerCommits runs query on the peer's branch, as another clone of the peer
// would: on a branch cut from the fetched peer tip, committed and pushed to
// the peer's branch. It fetches on its own connection, not through the
// store, so it never records the peer's last_sync.
func (f *filteredSyncFixture) peerCommits(t *testing.T, ctx context.Context, query string, args ...any) {
	t.Helper()
	conn, err := f.store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Close()
	if err := func() error {
		if err := versioncontrolops.Fetch(ctx, conn, f.peer, ""); err != nil {
			return err
		}
		tip, err := versioncontrolops.RemoteBranchTip(ctx, conn, f.peer, f.store.branch)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH('peerwork', ?)", tip); err != nil {
			return err
		}
		if err := versioncontrolops.CheckoutBranch(ctx, conn, "peerwork"); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'peer change')"); err != nil {
			return err
		}
		if err := versioncontrolops.Push(ctx, conn, f.peer, "peerwork:"+f.store.branch, ""); err != nil {
			return err
		}
		if err := versioncontrolops.CheckoutBranch(ctx, conn, f.store.branch); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, "CALL DOLT_BRANCH('-D', 'peerwork')")
		return err
	}(); err != nil {
		t.Fatalf("peer commits %q: %v", query, err)
	}
}

// assertNotMerging fails when a merge is left in progress on the source.
func (f *filteredSyncFixture) assertNotMerging(t *testing.T, ctx context.Context) {
	t.Helper()
	var merging bool
	if err := f.store.db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging); err != nil {
		t.Fatalf("read merge status: %v", err)
	}
	if merging {
		t.Error("a merge is left in progress")
	}
}

// titleAt reads the title of issue id at ref.
func (f *filteredSyncFixture) titleAt(t *testing.T, ctx context.Context, ref, id string) string {
	t.Helper()
	var title string
	if err := f.store.db.QueryRowContext(ctx, "SELECT title FROM issues AS OF '"+ref+"' WHERE id = ?", id).Scan(&title); err != nil {
		t.Fatalf("read title of %s at %s: %v", id, ref, err)
	}
	return title
}

// assertSyncTakesPeerDelete runs the sync after the peer deleted id and
// checks that it settles: id is gone locally and from the peer's tip, the
// withheld chore keeps choreRows label/comment/dependency rows, no merge is
// left in progress, and the sync after it works too.
func (f *filteredSyncFixture) assertSyncTakesPeerDelete(t *testing.T, ctx context.Context, id string, choreRows int) {
	t.Helper()
	res, err := f.store.Sync(ctx, f.peer, "")
	if err != nil {
		t.Fatalf("Sync after the peer deleted %s: %v", id, err)
	}
	if res.PushError != nil || len(res.Conflicts) != 0 {
		t.Fatalf("Sync push error %v, conflicts %v; want a settled sync", res.PushError, res.Conflicts)
	}
	src := f.store.branch
	if n := f.issueCount(t, ctx, src, id); n != 0 {
		t.Errorf("%s survived locally (%d rows); want the peer's delete taken", id, n)
	}
	if n, rows := f.issueCount(t, ctx, src, f.newChore), f.childRows(t, ctx, src, f.newChore); n != 1 || rows != choreRows {
		t.Errorf("withheld chore: %d rows with %d label/comment/dependency rows, want 1 with %d", n, rows, choreRows)
	}
	f.assertNotMerging(t, ctx)
	tip := f.peerTip(t, ctx)
	if f.issueCount(t, ctx, tip, id) != 0 || f.issueCount(t, ctx, tip, f.newChore) != 0 {
		t.Error("peer tip holds the deleted issue or the withheld chore")
	}
	f.sync(t, ctx)
}

// A peer that deletes a published issue linked to a withheld one does not
// wedge later syncs. The kept filter revert makes the withheld chore's edges,
// and the is_blocked they set, local changes against the peer's tip, so the
// next merge meets a delete/modify conflict whose local side changed only
// is_blocked (the open task the chore blocks) or a foreign key violation on a
// restored edge (a closed task the chore relates to, which bd prune deletes).
func TestFilteredSyncTakesPeerDeletesOfLinkedIssues(t *testing.T) {
	t.Run("open task the withheld chore blocks", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		f := newFilteredSyncFixture(t, ctx)
		configureFederationExcludeTypes(t, "chore", "wisp")
		f.sync(t, ctx)
		f.peerCommits(t, ctx, "DELETE FROM issues WHERE id = ?", f.task)
		// The label and comment; the task's edge went with the task.
		f.assertSyncTakesPeerDelete(t, ctx, f.task, 2)
	})
	t.Run("closed task the withheld chore relates to", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		f := newFilteredSyncFixture(t, ctx)
		configureFederationExcludeTypes(t, "chore", "wisp")
		closed := "test-closed"
		f.create(t, ctx, &types.Issue{ID: closed, Title: "finished task", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2})
		if err := f.store.CloseIssue(ctx, closed, "done", "test", ""); err != nil {
			t.Fatalf("CloseIssue: %v", err)
		}
		if err := f.store.AddDependency(ctx, &types.Dependency{IssueID: f.newChore, DependsOnID: closed, Type: types.DepRelated}, "test"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		f.commit(t, ctx, "closed task the chore relates to")
		f.sync(t, ctx)
		f.peerCommits(t, ctx, "DELETE FROM issues WHERE id = ?", closed)
		// The label, the comment and the open task's blocking edge.
		f.assertSyncTakesPeerDelete(t, ctx, closed, 3)
	})
	// The delete is taken on our side, so its cascade removes our provenance
	// row there; the commit that concludes the merge must take that too.
	t.Run("open task with a local provenance row", func(t *testing.T) {
		ctx, cancel := testContext(t)
		defer cancel()
		f := newFilteredSyncFixture(t, ctx)
		configureFederationExcludeTypes(t, "chore", "wisp")
		f.sync(t, ctx)
		f.exec(t, ctx, `INSERT INTO provenance_events (id, issue_id, kind, ref, ref_kind, source)
			VALUES (UUID(), ?, 'commit', 'abc123', 'git_sha', 'test')`, f.task)
		f.commit(t, ctx, "local provenance row")
		f.peerCommits(t, ctx, "DELETE FROM issues WHERE id = ?", f.task)
		f.assertSyncTakesPeerDelete(t, ctx, f.task, 2)
		if n := f.countAt(t, ctx, f.store.branch, "SELECT COUNT(*) FROM provenance_events %s WHERE issue_id = ?", f.task); n != 0 {
			t.Errorf("the source branch keeps %d provenance row(s) of the deleted task", n)
		}
		f.assertMergeCommitted(t, ctx)
	})
}

// A merge that needs the settle path (here a field-level merge of one issue
// both sides edited) must commit everything else it brought in too: the
// peer's label on another issue and its memory row reach our branch and stay
// on the peer's tip after the push, whether or not an issue is withheld.
// Committing only the tables the settle pass touched left them in our working
// set, and the push then took them off the peer.
func TestFilteredSyncSettledMergeKeepsPeerChanges(t *testing.T) {
	for _, exclude := range [][]string{{"epic"}, {"chore", "wisp"}} {
		t.Run(fmt.Sprint(exclude), func(t *testing.T) {
			ctx, cancel := testContext(t)
			defer cancel()
			f := newFilteredSyncFixture(t, ctx)
			configureFederationExcludeTypes(t, exclude...)
			shared, other := "test-shared", "test-other"
			f.create(t, ctx, &types.Issue{ID: shared, Title: "shared", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2})
			f.create(t, ctx, &types.Issue{ID: other, Title: "other", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2})
			f.commit(t, ctx, "shared issues")
			f.sync(t, ctx)

			// Both sides edit the shared issue and stamp updated_at, so the
			// row conflicts and the field-level merge settles it.
			f.exec(t, ctx, "UPDATE issues SET title = 'ours', updated_at = '2026-01-02 03:04:05' WHERE id = ?", shared)
			f.commit(t, ctx, "retitle locally")
			f.peerCommits(t, ctx, "UPDATE issues SET description = 'theirs', updated_at = '2026-01-02 03:04:06' WHERE id = ?", shared)
			f.peerCommits(t, ctx, "INSERT INTO labels (issue_id, label) VALUES (?, 'peer-label')", other)
			f.peerCommits(t, ctx, "INSERT INTO config (`key`, value) VALUES ('kv.memory.peer-note', 'from the peer')")

			res, err := f.store.Sync(ctx, f.peer, "")
			if err != nil || res.PushError != nil || len(res.Conflicts) != 0 || !res.Pushed {
				t.Fatalf("Sync = (pushed %v, conflicts %v, push error %v, err %v), want a settled sync that pushed",
					res.Pushed, res.Conflicts, res.PushError, err)
			}
			var title, description string
			if err := f.store.db.QueryRowContext(ctx, "SELECT title, description FROM issues WHERE id = ?", shared).Scan(&title, &description); err != nil {
				t.Fatalf("read the shared issue: %v", err)
			}
			if title != "ours" || description != "theirs" {
				t.Fatalf("shared issue = (%q, %q), want the field-level merge (ours, theirs)", title, description)
			}
			f.assertNotMerging(t, ctx)
			f.assertMergeCommitted(t, ctx)
			for _, ref := range []string{f.store.branch, f.peerTip(t, ctx)} {
				if n := f.countAt(t, ctx, ref, "SELECT COUNT(*) FROM labels %s WHERE issue_id = ? AND label = 'peer-label'", other); n != 1 {
					t.Errorf("%s holds %d of the peer's label, want 1", ref, n)
				}
				if n := f.countAt(t, ctx, ref, "SELECT COUNT(*) FROM config %s WHERE `key` = 'kv.memory.peer-note'"); n != 1 {
					t.Errorf("%s holds %d of the peer's memory row, want 1", ref, n)
				}
			}
		})
	}
}

// A peer registered with AddFederationPeer, as bd federation add-peer
// registers it, syncs through the same fetch, settled merge and publication
// as the bare remote the fixture uses. The sync's fetch rewrites the peer
// row's last_sync after the pre-sync commit; the commit that concludes the
// merge leaves that write out, as a merge Dolt commits itself would, and the
// post-merge is_blocked commit takes it. The peer's tip already holds the row
// with its old committed last_sync, as after an earlier sync: while the tip's
// federation_peers differs from ours, DOLT_MERGE refuses to run over the
// rewritten row, which predates this sync path.
func TestFilteredSyncRegisteredPeer(t *testing.T) {
	const oldSync = "2001-02-03 04:05:06"
	for _, tc := range []struct {
		exclude  []string
		filtered bool
	}{
		{[]string{"epic"}, false},
		{[]string{"chore", "wisp"}, true},
	} {
		t.Run(fmt.Sprint(tc.exclude), func(t *testing.T) {
			ctx, cancel := testContext(t)
			defer cancel()
			f := newFilteredSyncFixture(t, ctx)
			configureFederationExcludeTypes(t, tc.exclude...)
			if err := f.store.AddFederationPeer(ctx, &storage.FederationPeer{Name: f.peer, RemoteURL: f.remoteURL}); err != nil {
				t.Fatalf("AddFederationPeer: %v", err)
			}
			f.exec(t, ctx, "UPDATE federation_peers SET last_sync = ?, created_at = ?, updated_at = ? WHERE name = ?", oldSync, oldSync, oldSync, f.peer)
			// Both sides edit the issue the peer holds, so the merge settles.
			f.exec(t, ctx, "UPDATE issues SET title = 'ours', updated_at = '2026-01-02 03:04:05' WHERE id = ?", f.peerChore)
			f.commit(t, ctx, "register the peer and retitle")
			var head string
			if err := f.store.db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&head); err != nil {
				t.Fatalf("read HEAD: %v", err)
			}
			f.peerCommits(t, ctx, "INSERT INTO federation_peers SELECT * FROM federation_peers AS OF '"+head+"' WHERE name = ?", f.peer)
			f.peerCommits(t, ctx, "UPDATE issues SET description = 'theirs', updated_at = '2026-01-02 03:04:06' WHERE id = ?", f.peerChore)

			res, err := f.store.Sync(ctx, f.peer, "")
			if err != nil || res.PushError != nil || !res.Pushed || res.PushFiltered != tc.filtered || len(res.Conflicts) != 0 {
				t.Fatalf("Sync = (pushed %v, filtered %v, conflicts %v, push error %v, err %v), want a settled sync that pushed (filtered %v)",
					res.Pushed, res.PushFiltered, res.Conflicts, res.PushError, err, tc.filtered)
			}
			src := f.store.branch
			tip := f.peerTip(t, ctx)
			for _, ref := range []string{src, tip} {
				if n := f.countAt(t, ctx, ref, "SELECT COUNT(*) FROM issues %s WHERE id = ? AND title = 'ours' AND description = 'theirs'", f.peerChore); n != 1 {
					t.Errorf("%s lacks the field-level merge of the issue both sides edited", ref)
				}
			}
			if f.issueCount(t, ctx, tip, f.task) != 1 {
				t.Error("peer tip lacks the shared task; the publication did not land")
			}
			if tc.filtered && f.issueCount(t, ctx, tip, f.newChore) != 0 {
				t.Error("peer tip holds the withheld chore")
			}
			if f.issueCount(t, ctx, src, f.newChore) != 1 || f.childRows(t, ctx, src, f.newChore) != 3 || f.issueCount(t, ctx, src, f.wisp) != 1 {
				t.Error("sync removed local data")
			}
			var mergeCommit, mergedSync string
			if err := f.store.db.QueryRowContext(ctx, "SELECT commit_hash FROM dolt_log WHERE message LIKE 'auto-resolve merge conflicts%' LIMIT 1").Scan(&mergeCommit); err != nil {
				t.Fatalf("find the commit that concluded the merge: %v", err)
			}
			if err := f.store.db.QueryRowContext(ctx, "SELECT DATE_FORMAT(last_sync, '%Y-%m-%d %H:%i:%s') FROM federation_peers AS OF '"+mergeCommit+"' WHERE name = ?", f.peer).Scan(&mergedSync); err != nil {
				t.Fatalf("read last_sync at the merge commit: %v", err)
			}
			if mergedSync != oldSync {
				t.Errorf("the merge commit took the last_sync %s the fetch wrote before the merge; want %s", mergedSync, oldSync)
			}
		})
	}
}

func (f *filteredSyncFixture) exec(t *testing.T, ctx context.Context, query string, args ...any) {
	t.Helper()
	if _, err := f.store.db.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// assertMergeCommitted fails when the working set holds a change outside the
// dolt_ignore'd tables and the metadata row Sync writes after it pushes: what
// the merge brought in must be committed, not left behind uncommitted.
func (f *filteredSyncFixture) assertMergeCommitted(t *testing.T, ctx context.Context) {
	t.Helper()
	rows, err := f.store.db.QueryContext(ctx, `
		SELECT DISTINCT s.table_name FROM dolt_status s
		WHERE s.table_name <> 'metadata' AND NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1 AND s.table_name LIKE di.pattern
		)`)
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	defer rows.Close()
	var dirty []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan dolt_status: %v", err)
		}
		dirty = append(dirty, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	if len(dirty) != 0 {
		t.Errorf("uncommitted changes left in %v", dirty)
	}
}

// A conflict the auto-resolver leaves for the operator (both sides retitled
// one issue with the same updated_at, so last-write-wins has no answer) stops
// a sync without a strategy before it pushes; --strategy resolves it with the
// named side and concludes the merge, including ours, which leaves nothing to
// commit.
func TestFilteredSyncStrategyResolvesDeclinedConflicts(t *testing.T) {
	const stamp = "2026-01-02 03:04:05"
	for _, tc := range []struct{ strategy, want, wantTip string }{
		{"", "ours", "theirs"},
		{"theirs", "theirs", "theirs"},
		{"ours", "ours", "ours"},
	} {
		t.Run("strategy="+tc.strategy, func(t *testing.T) {
			ctx, cancel := testContext(t)
			defer cancel()
			f := newFilteredSyncFixture(t, ctx)
			configureFederationExcludeTypes(t, "chore", "wisp")
			shared := "test-shared"
			f.create(t, ctx, &types.Issue{ID: shared, Title: "shared", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2})
			f.commit(t, ctx, "shared task")
			f.sync(t, ctx)
			if _, err := f.store.db.ExecContext(ctx, "UPDATE issues SET title = 'ours', updated_at = ? WHERE id = ?", stamp, shared); err != nil {
				t.Fatalf("retitle locally: %v", err)
			}
			f.commit(t, ctx, "retitle locally")
			f.peerCommits(t, ctx, "UPDATE issues SET title = 'theirs', updated_at = ? WHERE id = ?", stamp, shared)

			res, err := f.store.Sync(ctx, f.peer, tc.strategy)
			if tc.strategy == "" {
				if err == nil || res.Pushed || len(res.Conflicts) != 1 || res.Conflicts[0].Field != "issues" {
					t.Fatalf("Sync = (pushed %v, conflicts %v, err %v), want the issues conflict reported and nothing pushed", res.Pushed, res.Conflicts, err)
				}
			} else if err != nil || !res.ConflictsResolved || res.PushError != nil {
				t.Fatalf("Sync = (resolved %v, push error %v, err %v), want the conflict resolved and pushed", res.ConflictsResolved, res.PushError, err)
			}
			if got := f.titleAt(t, ctx, f.store.branch, shared); got != tc.want {
				t.Errorf("local title = %q, want %q", got, tc.want)
			}
			if got := f.titleAt(t, ctx, f.peerTip(t, ctx), shared); got != tc.wantTip {
				t.Errorf("peer tip title = %q, want %q", got, tc.wantTip)
			}
			f.assertNotMerging(t, ctx)
		})
	}
}

// faultStages installs a stage hook that records each stage reached and runs
// the current fault, which the caller swaps per case.
type faultStages struct {
	seen  []string
	fault func(ctx context.Context, stage string, conn versioncontrolops.DBConn, req versioncontrolops.FilteredPushRequest) error
}

func installFaultStages(t *testing.T) *faultStages {
	t.Helper()
	fs := &faultStages{}
	t.Cleanup(versioncontrolops.SetFilteredPushStageHookForTest(func(ctx context.Context, stage string, conn versioncontrolops.DBConn, req versioncontrolops.FilteredPushRequest) error {
		fs.seen = append(fs.seen, stage)
		if fs.fault == nil {
			return nil
		}
		return fs.fault(ctx, stage, conn, req)
	}))
	return fs
}

// writeOnSource returns a stage fault that, at stage, lands an issue on the
// source as another writer would, committing it unless commit is false, and
// then restores the checkout the publication had.
func writeOnSource(stage, id string, commit bool) func(context.Context, string, versioncontrolops.DBConn, versioncontrolops.FilteredPushRequest) error {
	return func(ctx context.Context, at string, conn versioncontrolops.DBConn, req versioncontrolops.FilteredPushRequest) error {
		if at != stage {
			return nil
		}
		var active string
		if err := conn.QueryRowContext(ctx, "SELECT active_branch()").Scan(&active); err != nil {
			return err
		}
		stmts := []string{
			"CALL DOLT_CHECKOUT('" + req.Source + "')",
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, issue_type, status, priority) VALUES ('" + id + "', 'other writer', '', '', '', '', 'task', 'open', 2)",
		}
		if commit {
			stmts = append(stmts, "CALL DOLT_COMMIT('-am', 'other writer')")
		}
		stmts = append(stmts, "CALL DOLT_CHECKOUT('"+active+"')")
		for _, stmt := range stmts {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	}
}

func failAt(want string, err error) func(context.Context, string, versioncontrolops.DBConn, versioncontrolops.FilteredPushRequest) error {
	return func(_ context.Context, stage string, _ versioncontrolops.DBConn, _ versioncontrolops.FilteredPushRequest) error {
		if stage == want {
			return err
		}
		return nil
	}
}

// tipHash fetches the peer's branch and returns its tip. Like peerCommits it
// fetches on its own connection, so it never records the peer's last_sync.
func (f *filteredSyncFixture) tipHash(t *testing.T, ctx context.Context) string {
	t.Helper()
	conn, err := f.store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Close()
	if err := versioncontrolops.Fetch(ctx, conn, f.peer, ""); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	hash, err := versioncontrolops.RemoteBranchTip(ctx, conn, f.peer, f.store.branch)
	if err != nil {
		t.Fatalf("RemoteBranchTip: %v", err)
	}
	return hash
}

// assertLocalIntact checks that the local excluded data survived and that no
// temporary branch is left behind.
func (f *filteredSyncFixture) assertLocalIntact(t *testing.T, ctx context.Context, label string) {
	t.Helper()
	src := f.store.branch
	if f.issueCount(t, ctx, src, f.newChore) != 1 || f.childRows(t, ctx, src, f.newChore) != 3 || f.issueCount(t, ctx, src, f.wisp) != 1 {
		t.Fatalf("%s: local excluded data lost: chore=%d rows=%d wisp=%d", label,
			f.issueCount(t, ctx, src, f.newChore), f.childRows(t, ctx, src, f.newChore), f.issueCount(t, ctx, src, f.wisp))
	}
	var n int
	if err := f.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_branches WHERE name LIKE '\\_\\_federation\\_push\\_%'").Scan(&n); err != nil {
		t.Fatalf("%s: count temporary branches: %v", label, err)
	}
	if n != 0 {
		t.Fatalf("%s: %d temporary branches left behind", label, n)
	}
}

// A filtering or keep failure before publication pushes nothing, reports the
// failure, keeps local excluded data and removes the temporary branches. A
// transport failure after the keep merge leaves contents unchanged, and a
// cleanup failure after a publication is reported without hiding the push.
func TestFilteredSyncFailureKeepsLocalData(t *testing.T) {
	ctx, cancel := testContext(t)
	defer cancel()
	f := newFilteredSyncFixture(t, ctx)
	configureFederationExcludeTypes(t, "chore", "wisp")
	fs := installFaultStages(t)
	tip := f.tipHash(t, ctx)

	failedSync := func(label string, syncCtx context.Context, want error) {
		t.Helper()
		fs.seen = nil
		res, err := f.store.Sync(syncCtx, f.peer, "")
		if err != nil {
			t.Fatalf("%s: Sync: %v", label, err)
		}
		if res.PushError == nil || res.Pushed {
			t.Fatalf("%s: pushed=%v push error=%v, want a reported failure", label, res.Pushed, res.PushError)
		}
		if want != nil && !errors.Is(res.PushError, want) {
			t.Fatalf("%s: push error %v, want %v", label, res.PushError, want)
		}
		if got := f.tipHash(t, ctx); got != tip {
			t.Fatalf("%s: peer tip moved from %s to %s", label, tip, got)
		}
		f.assertLocalIntact(t, ctx, label)
	}

	// A restrictive foreign key makes the DELETE itself fail.
	if _, err := f.store.db.ExecContext(ctx, `CREATE TABLE fault_guard (issue_id VARCHAR(255) NOT NULL,
		FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE RESTRICT)`); err != nil {
		t.Fatalf("create fault_guard: %v", err)
	}
	if _, err := f.store.db.ExecContext(ctx, "INSERT INTO fault_guard VALUES (?)", f.newChore); err != nil {
		t.Fatalf("insert fault_guard: %v", err)
	}
	f.commit(t, ctx, "restrict chore delete")
	failedSync("delete", ctx, nil)
	if slices.Contains(fs.seen, "push") {
		t.Fatal("delete: reached the push stage")
	}
	if _, err := f.store.db.ExecContext(ctx, "DROP TABLE fault_guard"); err != nil {
		t.Fatalf("drop fault_guard: %v", err)
	}
	f.commit(t, ctx, "drop fault guard")

	errInjected := errors.New("injected fault")
	for _, stage := range []string{"commit", "keep", "revert", "merge"} {
		fs.fault = failAt(stage, errInjected)
		failedSync(stage, ctx, errInjected)
		if !slices.Contains(fs.seen, stage) || slices.Contains(fs.seen, "push") {
			t.Fatalf("%s: stages %v, want the fault stage and no push", stage, fs.seen)
		}
	}

	// Cancellation between the filter commit and the keep merge.
	cancelCtx, cancelSync := context.WithCancel(ctx)
	fs.fault = func(_ context.Context, stage string, _ versioncontrolops.DBConn, _ versioncontrolops.FilteredPushRequest) error {
		if stage == "keep" {
			cancelSync()
		}
		return nil
	}
	failedSync("cancel", cancelCtx, context.Canceled)
	cancelSync()

	// Another writer advances the source between staging and keep, or leaves
	// it dirty, or advances it after the check, just before the keep merge:
	// nothing is published and the other writer's change is kept, never reset.
	for i, c := range []struct {
		label, stage string
		commit       bool
	}{
		{"advance", "keep", true},
		{"dirty", "keep", false},
		{"advance before merge", "merge", true},
	} {
		other := fmt.Sprintf("test-other%d", i)
		fs.fault = writeOnSource(c.stage, other, c.commit)
		failedSync(c.label, ctx, versioncontrolops.ErrFilteredPushSourceChanged)
		// Commit an uncommitted write first: AS OF reads the branch commit.
		if !c.commit {
			f.commit(t, ctx, "other writer")
		}
		if f.issueCount(t, ctx, f.store.branch, other) != 1 {
			t.Fatalf("%s: the other writer's issue was lost", c.label)
		}
	}

	// A transport failure after the keep merge leaves contents unchanged.
	errTransport := errors.New("injected transport failure")
	fs.fault = failAt("push", errTransport)
	failedSync("transport", ctx, errTransport)

	// A cleanup failure after the push is reported and the push still counts.
	errCleanup := errors.New("injected cleanup failure")
	fs.fault = failAt("cleanup", errCleanup)
	res, err := f.store.Sync(ctx, f.peer, "")
	if err != nil {
		t.Fatalf("cleanup: Sync: %v", err)
	}
	if !res.Pushed || !res.PushFiltered || !errors.Is(res.PushError, errCleanup) {
		t.Fatalf("cleanup: pushed=%v filtered=%v push error=%v, want a reported cleanup failure after the filtered push", res.Pushed, res.PushFiltered, res.PushError)
	}
	if got := f.tipHash(t, ctx); got == tip {
		t.Fatal("cleanup: peer tip did not move")
	}
	fs.fault = nil
	f.assertLocalIntact(t, ctx, "cleanup")
}
