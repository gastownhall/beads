//go:build cgo

package embeddeddolt

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
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
