//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
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

// countAt counts rows of query evaluated against ref with AS OF.
func countAt(t *testing.T, store *EmbeddedDoltStore, ref, query string, args ...any) int {
	t.Helper()
	var n int
	if err := store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), fmt.Sprintf(query, "AS OF '"+ref+"'"), args...).Scan(&n)
	}); err != nil {
		t.Fatalf("count at %s: %v", ref, err)
	}
	return n
}

// filteredSyncFixture is a store whose peer already holds an excluded-type
// issue, plus a new local excluded issue with a label, comment and dependency
// and a leaked ephemeral row in the committed issues table.
type filteredSyncFixture struct {
	store                           *EmbeddedDoltStore
	peer                            string
	task, peerChore, newChore, wisp string
}

func newFilteredSyncFixture(t *testing.T) *filteredSyncFixture {
	t.Helper()
	ctx := t.Context()
	store := newPeerAuthTestStore(t)
	f := &filteredSyncFixture{
		store:     store,
		peer:      "peer",
		task:      "fedauth-task",
		peerChore: "fedauth-peerchore",
		newChore:  "fedauth-newchore",
		wisp:      "fedauth-leak",
	}
	if err := store.SetConfig(ctx, "issue_prefix", "fedauth"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: f.peer, RemoteURL: "file://" + filepath.Join(t.TempDir(), "peer"),
	}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}

	// The peer already holds an excluded-type issue.
	mustCreate(t, store, &types.Issue{ID: f.peerChore, Title: "held by peer", IssueType: types.TypeChore, Status: types.StatusOpen, Priority: 2})
	if err := store.PushTo(ctx, f.peer); err != nil {
		t.Fatalf("seed peer: %v", err)
	}

	mustCreate(t, store, &types.Issue{ID: f.task, Title: "shared task", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2})
	mustCreate(t, store, &types.Issue{ID: f.newChore, Title: "local chore", IssueType: types.TypeChore, Status: types.StatusOpen, Priority: 2})
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
	if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO issues
			(id, title, description, design, acceptance_criteria, notes, issue_type, status, priority, ephemeral)
			VALUES (?, 'leaked wisp', '', '', '', '', 'task', 'open', 2, 1)`, f.wisp)
		return err
	}); err != nil {
		t.Fatalf("insert ephemeral row: %v", err)
	}
	if _, err := store.CommitPending(ctx, "test"); err != nil {
		t.Fatalf("CommitPending: %v", err)
	}
	return f
}

func mustCreate(t *testing.T, store *EmbeddedDoltStore, issue *types.Issue) {
	t.Helper()
	if err := store.CreateIssue(t.Context(), issue, "test"); err != nil {
		t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
	}
}

// peerTip refreshes and returns the remote-tracking ref of the peer's branch.
func (f *filteredSyncFixture) peerTip(t *testing.T) string {
	t.Helper()
	if err := f.store.Fetch(t.Context(), f.peer); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	return "remotes/" + f.peer + "/main"
}

func (f *filteredSyncFixture) issueCount(t *testing.T, ref, id string) int {
	return countAt(t, f.store, ref, "SELECT COUNT(*) FROM issues %s WHERE id = ?", id)
}

func (f *filteredSyncFixture) childRows(t *testing.T, ref, id string) int {
	return countAt(t, f.store, ref, "SELECT COUNT(*) FROM labels %s WHERE issue_id = ?", id) +
		countAt(t, f.store, ref, "SELECT COUNT(*) FROM comments %s WHERE issue_id = ?", id) +
		countAt(t, f.store, ref, "SELECT COUNT(*) FROM dependencies %s WHERE issue_id = ? OR depends_on_issue_id = ?", id, id)
}

// A filtered sync withholds the new local excluded issue and its rows from the
// peer while leaving the excluded issue the peer already holds in place.
func TestFilteredFederationSyncExcludesTypes(t *testing.T) {
	ctx := context.Background()
	f := newFilteredSyncFixture(t)
	configureFederationExcludeTypes(t, "chore", "wisp")

	res, err := f.store.Sync(ctx, f.peer, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.PushError != nil || !res.Pushed || !res.PushFiltered {
		t.Fatalf("Sync pushed=%v filtered=%v push error=%v, want a filtered publication", res.Pushed, res.PushFiltered, res.PushError)
	}

	tip := f.peerTip(t)
	if f.issueCount(t, tip, f.task) != 1 {
		t.Fatal("peer tip lacks the shared task; the publication did not land")
	}
	if n := f.issueCount(t, tip, f.newChore); n != 0 {
		t.Errorf("peer tip holds the new excluded issue (%d rows)", n)
	}
	if n := f.childRows(t, tip, f.newChore); n != 0 {
		t.Errorf("peer tip holds %d label/comment/dependency rows of the new excluded issue", n)
	}
	if n := f.issueCount(t, tip, f.wisp); n != 0 {
		t.Errorf("peer tip holds the leaked ephemeral row (%d rows)", n)
	}
	if n := f.issueCount(t, tip, f.peerChore); n != 1 {
		t.Errorf("peer tip lost the excluded issue it already held (%d rows)", n)
	}

	if f.issueCount(t, "main", f.newChore) != 1 || f.childRows(t, "main", f.newChore) != 3 || f.issueCount(t, "main", f.wisp) != 1 {
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

func (f *filteredSyncFixture) sync(t *testing.T) {
	t.Helper()
	res, err := f.store.Sync(t.Context(), f.peer, "")
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
func TestFilteredFederationSyncKeepsLocalExcludedIssues(t *testing.T) {
	ctx := t.Context()
	f := newFilteredSyncFixture(t)
	configureFederationExcludeTypes(t, "chore", "wisp")
	checked := checkFilterAncestryAtPush(t)

	f.sync(t)
	// Edit the excluded issue locally and add a shared one between syncs.
	if _, err := f.store.AddIssueComment(ctx, f.newChore, "test", "second comment"); err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}
	if err := f.store.UpdateIssue(ctx, f.newChore, map[string]interface{}{"title": "local chore, edited"}, "test"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	task2 := "fedauth-task2"
	mustCreate(t, f.store, &types.Issue{ID: task2, Title: "second shared task", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2})
	if _, err := f.store.CommitPending(ctx, "test"); err != nil {
		t.Fatalf("CommitPending: %v", err)
	}
	f.sync(t)
	f.sync(t)

	if *checked < 2 {
		t.Fatalf("checked %d filtered publications, want 2", *checked)
	}
	if f.issueCount(t, "main", f.newChore) != 1 || f.childRows(t, "main", f.newChore) != 4 || f.issueCount(t, "main", f.wisp) != 1 {
		t.Errorf("later sync removed local excluded data: chore=%d rows=%d wisp=%d",
			f.issueCount(t, "main", f.newChore), f.childRows(t, "main", f.newChore), f.issueCount(t, "main", f.wisp))
	}
	tip := f.peerTip(t)
	if f.issueCount(t, tip, task2) != 1 {
		t.Fatal("peer tip lacks the second shared task")
	}
	if f.issueCount(t, tip, f.newChore) != 0 || f.childRows(t, tip, f.newChore) != 0 || f.issueCount(t, tip, f.wisp) != 0 {
		t.Error("peer tip holds withheld data after the later sync")
	}
}

// peerCommits runs query on the peer's branch, as another clone of the peer
// would: on a branch cut from the fetched peer tip, committed and pushed to
// the peer's main.
func (f *filteredSyncFixture) peerCommits(t *testing.T, query string, args ...any) {
	t.Helper()
	ctx := t.Context()
	tip := f.tipHash(t)
	if err := f.store.withPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
		if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH('peerwork', ?)", tip); err != nil {
			return err
		}
		if err := versioncontrolops.CheckoutBranch(ctx, db, "peerwork"); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'peer change')"); err != nil {
			return err
		}
		if err := versioncontrolops.Push(ctx, db, f.peer, "peerwork:main", ""); err != nil {
			return err
		}
		if err := versioncontrolops.CheckoutBranch(ctx, db, "main"); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', 'peerwork')")
		return err
	}); err != nil {
		t.Fatalf("peer commits %q: %v", query, err)
	}
}

// assertNotMerging fails when a merge is left in progress on main.
func (f *filteredSyncFixture) assertNotMerging(t *testing.T) {
	t.Helper()
	var merging bool
	if err := f.store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT is_merging FROM dolt_merge_status").Scan(&merging)
	}); err != nil {
		t.Fatalf("read merge status: %v", err)
	}
	if merging {
		t.Error("a merge is left in progress")
	}
}

// titleAt reads the title of issue id at ref.
func (f *filteredSyncFixture) titleAt(t *testing.T, ref, id string) string {
	t.Helper()
	var title string
	if err := f.store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT title FROM issues AS OF '"+ref+"' WHERE id = ?", id).Scan(&title)
	}); err != nil {
		t.Fatalf("read title of %s at %s: %v", id, ref, err)
	}
	return title
}

// assertSyncTakesPeerDelete runs the sync after the peer deleted id and
// checks that it settles: id is gone locally and from the peer's tip, the
// withheld chore keeps choreRows label/comment/dependency rows, no merge is
// left in progress, and the sync after it works too.
func (f *filteredSyncFixture) assertSyncTakesPeerDelete(t *testing.T, id string, choreRows int) {
	t.Helper()
	res, err := f.store.Sync(t.Context(), f.peer, "")
	if err != nil {
		t.Fatalf("Sync after the peer deleted %s: %v", id, err)
	}
	if res.PushError != nil || len(res.Conflicts) != 0 {
		t.Fatalf("Sync push error %v, conflicts %v; want a settled sync", res.PushError, res.Conflicts)
	}
	if n := f.issueCount(t, "main", id); n != 0 {
		t.Errorf("%s survived locally (%d rows); want the peer's delete taken", id, n)
	}
	if n, rows := f.issueCount(t, "main", f.newChore), f.childRows(t, "main", f.newChore); n != 1 || rows != choreRows {
		t.Errorf("withheld chore: %d rows with %d label/comment/dependency rows, want 1 with %d", n, rows, choreRows)
	}
	f.assertNotMerging(t)
	tip := f.peerTip(t)
	if f.issueCount(t, tip, id) != 0 || f.issueCount(t, tip, f.newChore) != 0 {
		t.Error("peer tip holds the deleted issue or the withheld chore")
	}
	f.sync(t)
}

// A peer that deletes a published issue linked to a withheld one does not
// wedge later syncs. The kept filter revert makes the withheld chore's edges,
// and the is_blocked they set, local changes against the peer's tip, so the
// next merge meets a delete/modify conflict whose local side changed only
// is_blocked (the open task the chore blocks) or a foreign key violation on a
// restored edge (a closed task the chore relates to, which bd prune deletes).
func TestFilteredFederationSyncTakesPeerDeletesOfLinkedIssues(t *testing.T) {
	t.Run("open task the withheld chore blocks", func(t *testing.T) {
		f := newFilteredSyncFixture(t)
		configureFederationExcludeTypes(t, "chore", "wisp")
		f.sync(t)
		f.peerCommits(t, "DELETE FROM issues WHERE id = ?", f.task)
		// The label and comment; the task's edge went with the task.
		f.assertSyncTakesPeerDelete(t, f.task, 2)
	})
	t.Run("closed task the withheld chore relates to", func(t *testing.T) {
		ctx := t.Context()
		f := newFilteredSyncFixture(t)
		configureFederationExcludeTypes(t, "chore", "wisp")
		closed := "fedauth-closed"
		mustCreate(t, f.store, &types.Issue{ID: closed, Title: "finished task", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2})
		if err := f.store.CloseIssue(ctx, closed, "done", "test", ""); err != nil {
			t.Fatalf("CloseIssue: %v", err)
		}
		if err := f.store.AddDependency(ctx, &types.Dependency{IssueID: f.newChore, DependsOnID: closed, Type: types.DepRelated}, "test"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		f.commit(t)
		f.sync(t)
		f.peerCommits(t, "DELETE FROM issues WHERE id = ?", closed)
		// The label, the comment and the open task's blocking edge.
		f.assertSyncTakesPeerDelete(t, closed, 3)
	})
	// The delete is taken on our side, so its cascade removes our provenance
	// row there; the commit that concludes the merge must take that too.
	t.Run("open task with a local provenance row", func(t *testing.T) {
		f := newFilteredSyncFixture(t)
		configureFederationExcludeTypes(t, "chore", "wisp")
		f.sync(t)
		f.exec(t, `INSERT INTO provenance_events (id, issue_id, kind, ref, ref_kind, source)
			VALUES (UUID(), ?, 'commit', 'abc123', 'git_sha', 'test')`, f.task)
		f.commit(t)
		f.peerCommits(t, "DELETE FROM issues WHERE id = ?", f.task)
		f.assertSyncTakesPeerDelete(t, f.task, 2)
		if n := countAt(t, f.store, "main", "SELECT COUNT(*) FROM provenance_events %s WHERE issue_id = ?", f.task); n != 0 {
			t.Errorf("main keeps %d provenance row(s) of the deleted task", n)
		}
		f.assertMergeCommitted(t)
	})
}

// A merge that needs the settle path (here a field-level merge of one issue
// both sides edited) must commit everything else it brought in too: the
// peer's label on another issue and its memory row reach our branch and stay
// on the peer's tip after the push, whether or not an issue is withheld.
// Committing only the tables the settle pass touched left them in our working
// set, and the push then took them off the peer.
func TestFilteredFederationSyncSettledMergeKeepsPeerChanges(t *testing.T) {
	for _, exclude := range [][]string{{"epic"}, {"chore", "wisp"}} {
		t.Run(fmt.Sprint(exclude), func(t *testing.T) {
			f := newFilteredSyncFixture(t)
			configureFederationExcludeTypes(t, exclude...)
			shared, other := "fedauth-shared", "fedauth-other"
			mustCreate(t, f.store, &types.Issue{ID: shared, Title: "shared", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2})
			mustCreate(t, f.store, &types.Issue{ID: other, Title: "other", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2})
			f.commit(t)
			f.sync(t)

			// Both sides edit the shared issue and stamp updated_at, so the
			// row conflicts and the field-level merge settles it.
			f.exec(t, "UPDATE issues SET title = 'ours', updated_at = '2026-01-02 03:04:05' WHERE id = ?", shared)
			f.commit(t)
			f.peerCommits(t, "UPDATE issues SET description = 'theirs', updated_at = '2026-01-02 03:04:06' WHERE id = ?", shared)
			f.peerCommits(t, "INSERT INTO labels (issue_id, label) VALUES (?, 'peer-label')", other)
			f.peerCommits(t, "INSERT INTO config (`key`, value) VALUES ('kv.memory.peer-note', 'from the peer')")

			res, err := f.store.Sync(t.Context(), f.peer, "")
			if err != nil || res.PushError != nil || len(res.Conflicts) != 0 || !res.Pushed {
				t.Fatalf("Sync = (pushed %v, conflicts %v, push error %v, err %v), want a settled sync that pushed",
					res.Pushed, res.Conflicts, res.PushError, err)
			}
			var title, description string
			if err := f.store.withConn(t.Context(), false, func(tx *sql.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT title, description FROM issues WHERE id = ?", shared).Scan(&title, &description)
			}); err != nil {
				t.Fatalf("read the shared issue: %v", err)
			}
			if title != "ours" || description != "theirs" {
				t.Fatalf("shared issue = (%q, %q), want the field-level merge (ours, theirs)", title, description)
			}
			f.assertNotMerging(t)
			f.assertMergeCommitted(t)
			for _, ref := range []string{"main", f.peerTip(t)} {
				if n := countAt(t, f.store, ref, "SELECT COUNT(*) FROM labels %s WHERE issue_id = ? AND label = 'peer-label'", other); n != 1 {
					t.Errorf("%s holds %d of the peer's label, want 1", ref, n)
				}
				if n := countAt(t, f.store, ref, "SELECT COUNT(*) FROM config %s WHERE `key` = 'kv.memory.peer-note'"); n != 1 {
					t.Errorf("%s holds %d of the peer's memory row, want 1", ref, n)
				}
			}
		})
	}
}

// assertMergeCommitted fails when the working set holds a change outside the
// dolt_ignore'd tables and the metadata row Sync writes after it pushes: what
// the merge brought in must be committed, not left behind uncommitted.
func (f *filteredSyncFixture) assertMergeCommitted(t *testing.T) {
	t.Helper()
	var dirty []string
	if err := f.store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(t.Context(), `
			SELECT DISTINCT s.table_name FROM dolt_status s
			WHERE s.table_name <> 'metadata' AND NOT EXISTS (
				SELECT 1 FROM dolt_ignore di
				WHERE di.ignored = 1 AND s.table_name LIKE di.pattern
			)`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var table string
			if err := rows.Scan(&table); err != nil {
				return err
			}
			dirty = append(dirty, table)
		}
		return rows.Err()
	}); err != nil {
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
func TestFilteredFederationSyncStrategyResolvesDeclinedConflicts(t *testing.T) {
	const stamp = "2026-01-02 03:04:05"
	for _, tc := range []struct{ strategy, want, wantTip string }{
		{"", "ours", "theirs"},
		{"theirs", "theirs", "theirs"},
		{"ours", "ours", "ours"},
	} {
		t.Run("strategy="+tc.strategy, func(t *testing.T) {
			f := newFilteredSyncFixture(t)
			configureFederationExcludeTypes(t, "chore", "wisp")
			shared := "fedauth-shared"
			mustCreate(t, f.store, &types.Issue{ID: shared, Title: "shared", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2})
			f.commit(t)
			f.sync(t)
			f.exec(t, "UPDATE issues SET title = 'ours', updated_at = ? WHERE id = ?", stamp, shared)
			f.commit(t)
			f.peerCommits(t, "UPDATE issues SET title = 'theirs', updated_at = ? WHERE id = ?", stamp, shared)

			res, err := f.store.Sync(t.Context(), f.peer, tc.strategy)
			if tc.strategy == "" {
				if err == nil || res.Pushed || len(res.Conflicts) != 1 || res.Conflicts[0].Field != "issues" {
					t.Fatalf("Sync = (pushed %v, conflicts %v, err %v), want the issues conflict reported and nothing pushed", res.Pushed, res.Conflicts, err)
				}
			} else if err != nil || !res.ConflictsResolved || res.PushError != nil {
				t.Fatalf("Sync = (resolved %v, push error %v, err %v), want the conflict resolved and pushed", res.ConflictsResolved, res.PushError, err)
			}
			if got := f.titleAt(t, "main", shared); got != tc.want {
				t.Errorf("local title = %q, want %q", got, tc.want)
			}
			if got := f.titleAt(t, f.peerTip(t), shared); got != tc.wantTip {
				t.Errorf("peer tip title = %q, want %q", got, tc.wantTip)
			}
			f.assertNotMerging(t)
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

func (f *filteredSyncFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := f.store.withConn(t.Context(), true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), query, args...)
		return err
	}); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func (f *filteredSyncFixture) commit(t *testing.T) {
	t.Helper()
	if _, err := f.store.CommitPending(t.Context(), "test"); err != nil {
		t.Fatalf("CommitPending: %v", err)
	}
}

func (f *filteredSyncFixture) tipHash(t *testing.T) string {
	t.Helper()
	f.peerTip(t)
	var hash string
	if err := f.store.withDBConn(t.Context(), func(db versioncontrolops.DBConn) error {
		var err error
		hash, err = versioncontrolops.RemoteBranchTip(t.Context(), db, f.peer, "main")
		return err
	}); err != nil {
		t.Fatalf("RemoteBranchTip: %v", err)
	}
	return hash
}

// assertLocalIntact checks that the local excluded data survived, the source
// is checked out and no temporary branch is left behind.
func (f *filteredSyncFixture) assertLocalIntact(t *testing.T, label string) {
	t.Helper()
	if f.issueCount(t, "main", f.newChore) != 1 || f.childRows(t, "main", f.newChore) != 3 || f.issueCount(t, "main", f.wisp) != 1 {
		t.Fatalf("%s: local excluded data lost: chore=%d rows=%d wisp=%d", label,
			f.issueCount(t, "main", f.newChore), f.childRows(t, "main", f.newChore), f.issueCount(t, "main", f.wisp))
	}
	var branch string
	var n int
	if err := f.store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(t.Context(), "SELECT active_branch()").Scan(&branch); err != nil {
			return err
		}
		return tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_branches WHERE name LIKE '\\_\\_federation\\_push\\_%'").Scan(&n)
	}); err != nil {
		t.Fatalf("%s: read branches: %v", label, err)
	}
	if branch != "main" || n != 0 {
		t.Fatalf("%s: checkout %q with %d temporary branches left behind, want main and none", label, branch, n)
	}
}

// A filtering or keep failure before publication pushes nothing, reports the
// failure, keeps local excluded data, restores the checkout and removes the
// temporary branches. A transport failure after the keep merge leaves contents
// unchanged, and a cleanup failure after a publication is reported without
// hiding the push.
func TestFilteredFederationSyncFailureKeepsLocalData(t *testing.T) {
	ctx := t.Context()
	f := newFilteredSyncFixture(t)
	configureFederationExcludeTypes(t, "chore", "wisp")
	fs := installFaultStages(t)
	tip := f.tipHash(t)

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
		if got := f.tipHash(t); got != tip {
			t.Fatalf("%s: peer tip moved from %s to %s", label, tip, got)
		}
		f.assertLocalIntact(t, label)
	}

	// A restrictive foreign key makes the DELETE itself fail.
	f.exec(t, `CREATE TABLE fault_guard (issue_id VARCHAR(255) NOT NULL,
		FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE RESTRICT)`)
	f.exec(t, "INSERT INTO fault_guard VALUES (?)", f.newChore)
	f.commit(t)
	failedSync("delete", ctx, nil)
	if slices.Contains(fs.seen, "push") {
		t.Fatal("delete: reached the push stage")
	}
	f.exec(t, "DROP TABLE fault_guard")
	f.commit(t)

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
		other := fmt.Sprintf("fedauth-other%d", i)
		fs.fault = writeOnSource(c.stage, other, c.commit)
		failedSync(c.label, ctx, versioncontrolops.ErrFilteredPushSourceChanged)
		// Commit an uncommitted write first: AS OF reads the branch commit.
		if !c.commit {
			f.commit(t)
		}
		if f.issueCount(t, "main", other) != 1 {
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
	if got := f.tipHash(t); got == tip {
		t.Fatal("cleanup: peer tip did not move")
	}
	fs.fault = nil
	f.assertLocalIntact(t, "cleanup")
}
