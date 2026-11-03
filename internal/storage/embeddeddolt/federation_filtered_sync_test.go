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
