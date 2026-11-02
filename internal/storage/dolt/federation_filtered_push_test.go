package dolt

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// GH#7890: a filtered push must not make the next sync delete the excluded
// issues locally when the filtered tip comes back from the peer.

const serverFilteredPushPeer = "peer"

// newServerFilteredPushStore returns a store whose remote serverFilteredPushPeer
// is a file:// database holding the store's initial history, and sets
// federation.exclude_types to [chore, wisp] for the test.
func newServerFilteredPushStore(t *testing.T) (*DoltStore, context.Context) {
	t.Helper()
	store, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)
	if err := store.SetConfig(ctx, "issue_prefix", "fp"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	// Sync refuses to auto-commit internal config keys before a pull.
	if err := store.CommitWithConfig(ctx, "test: filtered push fixture config"); err != nil {
		t.Fatalf("commit fixture config: %v", err)
	}

	remote := "file://" + filepath.Join(t.TempDir(), "peer-remote")
	if err := store.AddRemote(ctx, serverFilteredPushPeer, remote); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	if err := store.PushTo(ctx, serverFilteredPushPeer); err != nil {
		t.Fatalf("seed peer: %v", err)
	}

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Setenv("BEADS_DIR", t.TempDir())
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("federation.exclude_types", []string{"chore", "wisp"})
	return store, ctx
}

// seedServerFilteredPushGraph creates a published task, a chore with a label
// and a comment, a retained task blocked only by the chore, and an ephemeral
// row in the committed issues table.
func seedServerFilteredPushGraph(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	for _, issue := range []*types.Issue{
		{ID: "fp-task", Title: "published task", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 1},
		{ID: "fp-chore", Title: "private chore", IssueType: types.TypeChore, Status: types.StatusOpen, Priority: 1},
		{ID: "fp-waiter", Title: "retained waiter", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 1},
	} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	if err := store.AddLabel(ctx, "fp-chore", "private", "test"); err != nil {
		t.Fatalf("label chore: %v", err)
	}
	if _, err := store.AddIssueComment(ctx, "fp-chore", "test", "private note"); err != nil {
		t.Fatalf("comment chore: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: "fp-waiter", DependsOnID: "fp-chore", Type: types.DepBlocks}, "test"); err != nil {
		t.Fatalf("block waiter on chore: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO issues
		(id, title, description, design, acceptance_criteria, notes,
		 issue_type, status, priority, ephemeral, created_at, updated_at)
		VALUES ('fp-eph', 'ephemeral row', '', '', '', '', 'task', 'open', 1, 1, NOW(), NOW())`); err != nil {
		t.Fatalf("insert ephemeral row: %v", err)
	}
}

// serverGraphCounts reads, at ref ("" = the working set), how much of the
// seeded private graph is present and whether the waiter is blocked.
type serverGraphCounts struct {
	task, chore, eph, waiter, label, comment, dep, waiterBlocked, peerEdit int
}

var (
	serverLocalGraph     = serverGraphCounts{task: 1, chore: 1, eph: 1, waiter: 1, label: 1, comment: 1, dep: 1, waiterBlocked: 1}
	serverPublishedGraph = serverGraphCounts{task: 1, waiter: 1}
)

func readServerGraph(t *testing.T, ctx context.Context, store *DoltStore, ref string) serverGraphCounts {
	t.Helper()
	asOf := ""
	if ref != "" {
		asOf = fmt.Sprintf("AS OF '%s'", ref)
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := store.db.QueryRowContext(ctx, fmt.Sprintf(query, asOf), args...).Scan(&n); err != nil {
			t.Fatalf("count %q at %q: %v", query, ref, err)
		}
		return n
	}
	return serverGraphCounts{
		task:          count("SELECT COUNT(*) FROM issues %s WHERE id = ?", "fp-task"),
		chore:         count("SELECT COUNT(*) FROM issues %s WHERE id = ?", "fp-chore"),
		eph:           count("SELECT COUNT(*) FROM issues %s WHERE id = ?", "fp-eph"),
		waiter:        count("SELECT COUNT(*) FROM issues %s WHERE id = ?", "fp-waiter"),
		label:         count("SELECT COUNT(*) FROM labels %s WHERE issue_id = ?", "fp-chore"),
		comment:       count("SELECT COUNT(*) FROM comments %s WHERE issue_id = ?", "fp-chore"),
		dep:           count("SELECT COUNT(*) FROM dependencies %s WHERE issue_id = ? AND depends_on_issue_id = ?", "fp-waiter", "fp-chore"),
		waiterBlocked: count("SELECT COUNT(*) FROM issues %s WHERE id = ? AND is_blocked = 1", "fp-waiter"),
		peerEdit:      count("SELECT COUNT(*) FROM issues %s WHERE id = ? AND title = 'edited on peer'", "fp-task"),
	}
}

func serverPeerTip(t *testing.T, ctx context.Context, store *DoltStore) string {
	t.Helper()
	if err := store.Fetch(ctx, serverFilteredPushPeer); err != nil {
		t.Fatalf("fetch peer: %v", err)
	}
	return "remotes/" + serverFilteredPushPeer + "/" + store.branch
}

func syncServerFilteredPeer(t *testing.T, ctx context.Context, store *DoltStore, label string) {
	t.Helper()
	result, err := store.Sync(ctx, serverFilteredPushPeer, "")
	if err != nil {
		t.Fatalf("%s: Sync: %v", label, err)
	}
	if result.PushError != nil || !result.Pushed {
		t.Fatalf("%s: Sync pushed = %v, push error = %v; want a successful push", label, result.Pushed, result.PushError)
	}
}

// commitOnServerPeer commits an edit to fp-task on top of the peer's current
// tip and pushes it there, as another workspace syncing with the peer would.
func commitOnServerPeer(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	tip := serverPeerTip(t, ctx, store)
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin peer-edit connection: %v", err)
	}
	defer conn.Close()
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{"CALL DOLT_BRANCH('peer-work', ?)", []any{tip}},
		{"CALL DOLT_CHECKOUT('peer-work')", nil},
		{"UPDATE issues SET title = 'edited on peer' WHERE id = 'fp-task'", nil},
		{"CALL DOLT_COMMIT('-Am', 'peer: edit published task')", nil},
		{"CALL DOLT_PUSH(?, ?)", []any{serverFilteredPushPeer, "peer-work:" + store.branch}},
		{"CALL DOLT_CHECKOUT(?)", []any{store.branch}},
		{"CALL DOLT_BRANCH('-D', 'peer-work')", nil},
	} {
		if _, err := conn.ExecContext(ctx, stmt.q, stmt.args...); err != nil {
			t.Fatalf("commit on peer: %s: %v", stmt.q, err)
		}
	}
}

func assertServerOnlySourceBranch(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	branches, err := store.ListBranches(ctx)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) != 1 || branches[0] != store.branch {
		t.Fatalf("branches = %v, want only %s (temporary push branches must be removed)", branches, store.branch)
	}
}

// AC-6: syncing again after a filtered push, with the peer unchanged or with
// peer commits on the filtered tip, keeps the excluded issues and their
// labels, comments and dependencies local and merges the peer's changes.
func TestServerSyncFilteredPushRetainsExcludedRowsAcrossSyncs(t *testing.T) {
	store, ctx := newServerFilteredPushStore(t)
	seedServerFilteredPushGraph(t, ctx, store)

	syncServerFilteredPeer(t, ctx, store, "first sync")
	syncServerFilteredPeer(t, ctx, store, "sync with unchanged peer")
	if got := readServerGraph(t, ctx, store, ""); got != serverLocalGraph {
		t.Fatalf("after re-sync with unchanged peer, local graph = %+v, want %+v", got, serverLocalGraph)
	}

	commitOnServerPeer(t, ctx, store)
	syncServerFilteredPeer(t, ctx, store, "sync with peer commit on the filtered tip")
	want := serverLocalGraph
	want.peerEdit = 1
	if got := readServerGraph(t, ctx, store, ""); got != want {
		t.Errorf("after merging a peer commit, local graph = %+v, want %+v", got, want)
	}
	wantTip := serverPublishedGraph
	wantTip.peerEdit = 1
	if got := readServerGraph(t, ctx, store, serverPeerTip(t, ctx, store)); got != wantTip {
		t.Errorf("peer tip graph = %+v, want %+v", got, wantTip)
	}

	syncServerFilteredPeer(t, ctx, store, "final sync")
	if got := readServerGraph(t, ctx, store, ""); got != want {
		t.Errorf("after the final sync, local graph = %+v, want %+v", got, want)
	}
	assertServerOnlySourceBranch(t, ctx, store)
}

// TestFilteredPushSecondSyncKeepsExcludedRows is the simple base-failing
// server regression for AC-6: after a compensated filtered push, the merge of
// the peer's unchanged filtered tip (the second sync's pull step) must retain
// the excluded local rows. Before the keep-branch compensation the filtered
// commit F lived only on the deleted staging branch, the peer's filtered tip
// descended from the local tip, and this merge fast-forwarded the local branch
// to F, deleting every excluded issue.
func TestFilteredPushSecondSyncKeepsExcludedRows(t *testing.T) {
	store, ctx := newServerFilteredPushStore(t)
	for _, issue := range []*types.Issue{
		{ID: "fed-keep-task", Title: "Shareable task", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 1},
		{ID: "fed-keep-chore", Title: "Private chore", IssueType: types.TypeChore, Status: types.StatusOpen, Priority: 1},
	} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	if err := store.Commit(ctx, "create test issues"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit: %v", err)
	}

	// First filtered push: publishes the filtered tip and compensates locally.
	if err := store.filteredPushToPeer(ctx, serverFilteredPushPeer, []string{"chore"}); err != nil {
		t.Fatalf("filtered push: %v", err)
	}

	// The compensation recorded the filtered commit and its revert in the
	// local branch's history. Before it, F lived only on the staging branch,
	// which the cleanup deleted.
	assertBranchLogMessage(t, ctx, store, "federation: exclude private issue types")
	var reverts int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_log WHERE message LIKE 'Revert %'").Scan(&reverts); err != nil {
		t.Fatalf("count revert commits: %v", err)
	}
	if reverts == 0 {
		t.Error("local history lacks the keep-branch revert of the filtered commit")
	}

	// The excluded row survives the push itself.
	assertIssueRowExists(t, ctx, store, "fed-keep-task")
	assertIssueRowExists(t, ctx, store, "fed-keep-chore")

	// Second sync's merge: the peer's filtered tip fast-forwarded the local
	// branch to it before the compensation, deleting the excluded row. With F
	// already an ancestor of the local branch the merge is a no-op.
	serverPeerTip(t, ctx, store)
	if _, err := store.Merge(ctx, serverFilteredPushPeer+"/"+store.branch); err != nil {
		t.Fatalf("merge peer tip: %v", err)
	}
	assertIssueRowExists(t, ctx, store, "fed-keep-task")
	assertIssueRowExists(t, ctx, store, "fed-keep-chore")
	assertServerOnlySourceBranch(t, ctx, store)
}

// assertBranchLogMessage fails when the current branch's history holds no
// commit with exactly message.
func assertBranchLogMessage(t *testing.T, ctx context.Context, store *DoltStore, message string) {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_log WHERE message = ?", message).Scan(&n); err != nil {
		t.Fatalf("count log messages %q: %v", message, err)
	}
	if n == 0 {
		t.Errorf("branch history lacks commit %q", message)
	}
}

// assertIssueRowExists fails when the issues table has no row with id.
func assertIssueRowExists(t *testing.T, ctx context.Context, store *DoltStore, id string) {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE id = ?", id).Scan(&n); err != nil {
		t.Fatalf("count issue %s: %v", id, err)
	}
	if n != 1 {
		t.Errorf("issue %s row count = %d, want 1", id, n)
	}
}

var errInjectedServerFilteredPush = errors.New("injected filtered push failure")

// injectServerFilteredPushStep makes the filtered push fail at step: with an
// error, or, when cancel is set, by canceling the operation's context first.
func injectServerFilteredPushStep(t *testing.T, step string, cancel context.CancelFunc) {
	t.Helper()
	filteredPushBeforeStep = func(got string) error {
		if got != step {
			return nil
		}
		if cancel != nil {
			cancel()
			return nil
		}
		return errInjectedServerFilteredPush
	}
	t.Cleanup(func() { filteredPushBeforeStep = nil })
}

// AC-7: a filtered push that fails or is canceled at any step reports the
// failure, keeps every local row, sends nothing to the peer and removes both
// temporary branches.
func TestServerSyncFilteredPushFailureKeepsLocalDataAndCleansUp(t *testing.T) {
	for _, step := range versioncontrolops.FilteredPushSteps {
		if step == versioncontrolops.FilteredPushStepCleanup {
			continue
		}
		for _, canceled := range []bool{false, true} {
			name := step + "/failed"
			if canceled {
				name = step + "/canceled"
			}
			t.Run(name, func(t *testing.T) {
				store, ctx := newServerFilteredPushStore(t)
				seedServerFilteredPushGraph(t, ctx, store)
				opCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				if canceled {
					injectServerFilteredPushStep(t, step, cancel)
				} else {
					injectServerFilteredPushStep(t, step, nil)
				}

				result, err := store.Sync(opCtx, serverFilteredPushPeer, "")
				if err != nil {
					t.Fatalf("Sync: %v", err)
				}
				switch {
				case result.Pushed || result.PushError == nil:
					t.Fatalf("Sync pushed = %v, push error = %v; want the push to fail", result.Pushed, result.PushError)
				case !canceled && !errors.Is(result.PushError, errInjectedServerFilteredPush):
					t.Errorf("push error = %v, want the injected failure", result.PushError)
				case strings.Contains(result.PushError.Error(), "cleanup"):
					t.Errorf("push error = %v, want a clean cleanup", result.PushError)
				}

				if got := readServerGraph(t, ctx, store, ""); got != serverLocalGraph {
					t.Errorf("local graph = %+v, want %+v", got, serverLocalGraph)
				}
				if got := readServerGraph(t, ctx, store, serverPeerTip(t, ctx, store)); got != (serverGraphCounts{}) {
					t.Errorf("peer tip graph = %+v, want the seed tip (nothing pushed)", got)
				}
				assertServerOnlySourceBranch(t, ctx, store)
				assertServerWritableAfterFilteredPushFailure(t, ctx, store)
			})
		}
	}
}

// assertServerWritableAfterFilteredPushFailure checks that the store still
// accepts ordinary writes after a failed filtered push.
func assertServerWritableAfterFilteredPushFailure(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	after := &types.Issue{ID: "fp-after", Title: "written after the failure", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 1}
	if err := store.CreateIssue(ctx, after, "test"); err != nil {
		t.Fatalf("create issue after failed filtered push: %v", err)
	}
	if err := store.Commit(ctx, "write after failed filtered push"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit after failed filtered push: %v", err)
	}
	assertIssueRowExists(t, ctx, store, after.ID)
}

// AC-7: a cleanup failure is reported, and cleanup still removes what it can.
func TestServerSyncFilteredPushReportsCleanupFailure(t *testing.T) {
	store, ctx := newServerFilteredPushStore(t)
	seedServerFilteredPushGraph(t, ctx, store)
	injectServerFilteredPushStep(t, versioncontrolops.FilteredPushStepCleanup, nil)

	result, err := store.Sync(ctx, serverFilteredPushPeer, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PushError == nil || !errors.Is(result.PushError, errInjectedServerFilteredPush) ||
		!strings.Contains(result.PushError.Error(), "cleanup") {
		t.Fatalf("push error = %v, want the injected cleanup failure reported", result.PushError)
	}
	if got := readServerGraph(t, ctx, store, ""); got != serverLocalGraph {
		t.Errorf("local graph = %+v, want %+v", got, serverLocalGraph)
	}
	assertServerOnlySourceBranch(t, ctx, store)
}
