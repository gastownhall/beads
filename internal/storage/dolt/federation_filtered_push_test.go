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
			})
		}
	}
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
