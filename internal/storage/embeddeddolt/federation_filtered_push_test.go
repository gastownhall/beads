//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// GH#7890: embedded Sync honors federation.exclude_types the way the server
// store does, and keeps the excluded rows local.

const filteredPushPeer = "peer"

// newFilteredPushStore returns a store whose remote filteredPushPeer is a
// file:// database already holding the store's initial history, and sets
// federation.exclude_types to [chore, wisp] for the test.
func newFilteredPushStore(t *testing.T) *EmbeddedDoltStore {
	t.Helper()
	ctx := t.Context()
	store := newPeerAuthTestStore(t)
	if err := store.SetConfig(ctx, "issue_prefix", "fp"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	remote := "file://" + filepath.Join(t.TempDir(), "peer-remote")
	if err := store.AddRemote(ctx, filteredPushPeer, remote); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	if err := store.PushTo(ctx, filteredPushPeer); err != nil {
		t.Fatalf("seed peer: %v", err)
	}

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Setenv("BEADS_DIR", t.TempDir())
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("federation.exclude_types", []string{"chore", "wisp"})
	return store
}

// seedFilteredPushGraph creates a published task, a chore with a label and a
// comment, a retained task blocked only by the chore, and an ephemeral row in
// the committed issues table.
func seedFilteredPushGraph(t *testing.T, store *EmbeddedDoltStore) {
	t.Helper()
	ctx := t.Context()
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
	execPeerSQL(t, store, `INSERT INTO issues
		(id, title, description, design, acceptance_criteria, notes,
		 issue_type, status, priority, ephemeral, created_at, updated_at)
		VALUES ('fp-eph', 'ephemeral row', '', '', '', '', 'task', 'open', 1, 1, NOW(), NOW())`)
}

// countAt counts rows of query's table at ref ("" = the working set); query
// is a SELECT COUNT(*) with a %s placeholder where the AS OF clause goes.
func countAt(t *testing.T, store *EmbeddedDoltStore, ref, query string, args ...any) int {
	t.Helper()
	ctx := t.Context()
	asOf := ""
	if ref != "" {
		asOf = fmt.Sprintf("AS OF '%s'", ref)
	}
	var n int
	if err := store.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, fmt.Sprintf(query, asOf), args...).Scan(&n)
	}); err != nil {
		t.Fatalf("count %q at %q: %v", query, ref, err)
	}
	return n
}

// filteredGraphCounts reads, at ref, how much of the seeded private graph is
// present and whether the waiter is blocked.
type filteredGraphCounts struct {
	task, chore, eph, waiter, label, comment, dep, waiterBlocked int
}

func readFilteredGraph(t *testing.T, store *EmbeddedDoltStore, ref string) filteredGraphCounts {
	t.Helper()
	return filteredGraphCounts{
		task:          countAt(t, store, ref, "SELECT COUNT(*) FROM issues %s WHERE id = ?", "fp-task"),
		chore:         countAt(t, store, ref, "SELECT COUNT(*) FROM issues %s WHERE id = ?", "fp-chore"),
		eph:           countAt(t, store, ref, "SELECT COUNT(*) FROM issues %s WHERE id = ?", "fp-eph"),
		waiter:        countAt(t, store, ref, "SELECT COUNT(*) FROM issues %s WHERE id = ?", "fp-waiter"),
		label:         countAt(t, store, ref, "SELECT COUNT(*) FROM labels %s WHERE issue_id = ?", "fp-chore"),
		comment:       countAt(t, store, ref, "SELECT COUNT(*) FROM comments %s WHERE issue_id = ?", "fp-chore"),
		dep:           countAt(t, store, ref, "SELECT COUNT(*) FROM dependencies %s WHERE issue_id = ? AND depends_on_issue_id = ?", "fp-waiter", "fp-chore"),
		waiterBlocked: countAt(t, store, ref, "SELECT COUNT(*) FROM issues %s WHERE id = ? AND is_blocked = 1", "fp-waiter"),
	}
}

// localGraph is the full seeded graph: nothing excluded, waiter blocked.
var localGraph = filteredGraphCounts{task: 1, chore: 1, eph: 1, waiter: 1, label: 1, comment: 1, dep: 1, waiterBlocked: 1}

// publishedGraph is the seeded graph with the chore, the ephemeral row and
// everything hanging off the chore removed, and the waiter unblocked.
var publishedGraph = filteredGraphCounts{task: 1, waiter: 1}

// peerTip fetches filteredPushPeer and returns its tracking ref.
func peerTip(t *testing.T, store *EmbeddedDoltStore) string {
	t.Helper()
	if err := store.Fetch(t.Context(), filteredPushPeer); err != nil {
		t.Fatalf("fetch peer: %v", err)
	}
	return "remotes/" + filteredPushPeer + "/main"
}

func assertOnlyMainBranch(t *testing.T, store *EmbeddedDoltStore) {
	t.Helper()
	branches, err := store.ListBranches(t.Context())
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) != 1 || branches[0] != "main" {
		t.Fatalf("branches = %v, want only main (temporary push branches must be removed)", branches)
	}
}

// AC-5: a successful embedded Sync publishes a tip without the excluded rows
// and keeps them, and everything attached to them, in the local database.
func TestSyncFilteredPushPublishesWithoutExcludedTypes(t *testing.T) {
	store := newFilteredPushStore(t)
	seedFilteredPushGraph(t, store)

	result, err := store.Sync(t.Context(), filteredPushPeer, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PushError != nil || !result.Pushed {
		t.Fatalf("Sync pushed = %v, push error = %v; want a successful push", result.Pushed, result.PushError)
	}

	if got := readFilteredGraph(t, store, peerTip(t, store)); got != publishedGraph {
		t.Errorf("peer tip graph = %+v, want %+v", got, publishedGraph)
	}
	if got := readFilteredGraph(t, store, ""); got != localGraph {
		t.Errorf("local graph = %+v, want %+v", got, localGraph)
	}
	assertOnlyMainBranch(t, store)
}

// commitOnPeer commits an edit to fp-task on top of the peer's current tip
// and pushes it there, as another workspace syncing with the peer would.
func commitOnPeer(t *testing.T, store *EmbeddedDoltStore, title string) {
	t.Helper()
	ctx := t.Context()
	tip := peerTip(t, store)
	if err := store.withPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
		for _, stmt := range []struct {
			q    string
			args []any
		}{
			{"CALL DOLT_BRANCH('peer-work', ?)", []any{tip}},
			{"CALL DOLT_CHECKOUT('peer-work')", nil},
			{"UPDATE issues SET title = ? WHERE id = 'fp-task'", []any{title}},
			{"CALL DOLT_COMMIT('-Am', 'peer: edit published task')", nil},
			{"CALL DOLT_PUSH(?, 'peer-work:main')", []any{filteredPushPeer}},
			{"CALL DOLT_CHECKOUT('main')", nil},
			{"CALL DOLT_BRANCH('-D', 'peer-work')", nil},
		} {
			if _, err := db.ExecContext(ctx, stmt.q, stmt.args...); err != nil {
				return fmt.Errorf("%s: %w", stmt.q, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("commit on peer: %v", err)
	}
}

func syncFilteredPeer(t *testing.T, store *EmbeddedDoltStore, label string) {
	t.Helper()
	result, err := store.Sync(t.Context(), filteredPushPeer, "")
	if err != nil {
		t.Fatalf("%s: Sync: %v", label, err)
	}
	if result.PushError != nil || !result.Pushed {
		t.Fatalf("%s: Sync pushed = %v, push error = %v; want a successful push", label, result.Pushed, result.PushError)
	}
}

func taskTitleAt(t *testing.T, store *EmbeddedDoltStore, ref string) int {
	t.Helper()
	return countAt(t, store, ref, "SELECT COUNT(*) FROM issues %s WHERE id = 'fp-task' AND title = 'edited on peer'")
}

// AC-6: syncing again after a filtered push, with the peer unchanged or with
// peer commits on the filtered tip, keeps the excluded issues and their
// labels, comments and dependencies local and merges the peer's changes.
func TestSyncFilteredPushRetainsExcludedRowsAcrossSyncs(t *testing.T) {
	store := newFilteredPushStore(t)
	seedFilteredPushGraph(t, store)

	syncFilteredPeer(t, store, "first sync")
	syncFilteredPeer(t, store, "sync with unchanged peer")
	if got := readFilteredGraph(t, store, ""); got != localGraph {
		t.Fatalf("after re-sync with unchanged peer, local graph = %+v, want %+v", got, localGraph)
	}

	commitOnPeer(t, store, "edited on peer")
	syncFilteredPeer(t, store, "sync with peer commit on the filtered tip")
	if got := readFilteredGraph(t, store, ""); got != localGraph {
		t.Errorf("after merging a peer commit, local graph = %+v, want %+v", got, localGraph)
	}
	if taskTitleAt(t, store, "") != 1 {
		t.Errorf("local fp-task lacks the peer's edit")
	}
	tip := peerTip(t, store)
	if got := readFilteredGraph(t, store, tip); got != publishedGraph {
		t.Errorf("peer tip graph = %+v, want %+v", got, publishedGraph)
	}
	if taskTitleAt(t, store, tip) != 1 {
		t.Errorf("peer tip lost its own edit")
	}

	syncFilteredPeer(t, store, "final sync")
	if got := readFilteredGraph(t, store, ""); got != localGraph {
		t.Errorf("after the final sync, local graph = %+v, want %+v", got, localGraph)
	}
	assertOnlyMainBranch(t, store)
}

var errInjectedFilteredPush = errors.New("injected filtered push failure")

// injectFilteredPushStep makes the filtered push fail at step: with an error,
// or, when cancel is set, by canceling the operation's context first.
func injectFilteredPushStep(t *testing.T, step string, cancel context.CancelFunc) {
	t.Helper()
	filteredPushBeforeStep = func(got string) error {
		if got != step {
			return nil
		}
		if cancel != nil {
			cancel()
			return nil
		}
		return errInjectedFilteredPush
	}
	t.Cleanup(func() { filteredPushBeforeStep = nil })
}

// AC-7: a filtered push that fails or is canceled at any step reports the
// failure, keeps every local row, sends nothing to the peer and removes both
// temporary branches.
func TestSyncFilteredPushFailureKeepsLocalDataAndCleansUp(t *testing.T) {
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
				store := newFilteredPushStore(t)
				seedFilteredPushGraph(t, store)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if canceled {
					injectFilteredPushStep(t, step, cancel)
				} else {
					injectFilteredPushStep(t, step, nil)
				}

				result, err := store.Sync(ctx, filteredPushPeer, "")
				if err != nil {
					t.Fatalf("Sync: %v", err)
				}
				switch {
				case result.Pushed || result.PushError == nil:
					t.Fatalf("Sync pushed = %v, push error = %v; want the push to fail", result.Pushed, result.PushError)
				case !canceled && !errors.Is(result.PushError, errInjectedFilteredPush):
					t.Errorf("push error = %v, want the injected failure", result.PushError)
				case strings.Contains(result.PushError.Error(), "cleanup"):
					t.Errorf("push error = %v, want a clean cleanup", result.PushError)
				}

				if got := readFilteredGraph(t, store, ""); got != localGraph {
					t.Errorf("local graph = %+v, want %+v", got, localGraph)
				}
				if got := readFilteredGraph(t, store, peerTip(t, store)); got != (filteredGraphCounts{}) {
					t.Errorf("peer tip graph = %+v, want the seed tip (nothing pushed)", got)
				}
				assertOnlyMainBranch(t, store)
			})
		}
	}
}

// AC-7: a cleanup failure is reported, and cleanup still removes what it can.
func TestSyncFilteredPushReportsCleanupFailure(t *testing.T) {
	store := newFilteredPushStore(t)
	seedFilteredPushGraph(t, store)
	injectFilteredPushStep(t, versioncontrolops.FilteredPushStepCleanup, nil)

	result, err := store.Sync(t.Context(), filteredPushPeer, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PushError == nil || !errors.Is(result.PushError, errInjectedFilteredPush) ||
		!strings.Contains(result.PushError.Error(), "cleanup") {
		t.Fatalf("push error = %v, want the injected cleanup failure reported", result.PushError)
	}
	if got := readFilteredGraph(t, store, ""); got != localGraph {
		t.Errorf("local graph = %+v, want %+v", got, localGraph)
	}
	assertOnlyMainBranch(t, store)
}
