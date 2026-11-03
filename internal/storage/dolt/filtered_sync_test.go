package dolt

import (
	"context"
	"fmt"
	"os"
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
	peer                            string
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
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{Name: f.peer, RemoteURL: "file://" + remoteDir}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
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
	if res.PushError != nil || !res.Pushed {
		t.Fatalf("Sync pushed=%v push error=%v, want a publication", res.Pushed, res.PushError)
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
