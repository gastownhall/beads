package dolt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/config"
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
	// A credential-free remote: a federation_peers row would get its
	// last_sync rewritten by Sync's own fetch, after the pre-pull commit,
	// which makes the merge refuse whenever the clock crosses a second.
	if err := store.AddRemote(ctx, f.peer, "file://"+remoteDir); err != nil {
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

func (f *filteredSyncFixture) tipHash(t *testing.T, ctx context.Context) string {
	t.Helper()
	f.peerTip(t, ctx)
	hash, err := versioncontrolops.RemoteBranchTip(ctx, f.store.db, f.peer, f.store.branch)
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
