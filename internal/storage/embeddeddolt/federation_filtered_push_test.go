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

// peerStmt is one statement onPeer runs on the peer's tip.
type peerStmt struct {
	q    string
	args []any
}

// onPeer commits stmts on top of the peer's current tip and pushes the commit
// there, as another workspace syncing with the peer would.
func onPeer(t *testing.T, store *EmbeddedDoltStore, message string, stmts ...peerStmt) {
	t.Helper()
	ctx := t.Context()
	tip := peerTip(t, store)
	steps := append([]peerStmt{
		{"CALL DOLT_BRANCH('peer-work', ?)", []any{tip}},
		{"CALL DOLT_CHECKOUT('peer-work')", nil},
	}, stmts...)
	steps = append(steps,
		peerStmt{"CALL DOLT_COMMIT('-Am', ?)", []any{message}},
		peerStmt{"CALL DOLT_PUSH(?, 'peer-work:main')", []any{filteredPushPeer}},
		peerStmt{"CALL DOLT_CHECKOUT('main')", nil},
		peerStmt{"CALL DOLT_BRANCH('-D', 'peer-work')", nil},
	)
	if err := store.withPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
		for _, stmt := range steps {
			if _, err := db.ExecContext(ctx, stmt.q, stmt.args...); err != nil {
				return fmt.Errorf("%s: %w", stmt.q, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("%s: %v", message, err)
	}
}

// commitOnPeer commits an edit to fp-task on top of the peer's current tip
// and pushes it there, as another workspace syncing with the peer would.
func commitOnPeer(t *testing.T, store *EmbeddedDoltStore, title string) {
	t.Helper()
	onPeer(t, store, "peer: edit published task",
		peerStmt{"UPDATE issues SET title = ? WHERE id = 'fp-task'", []any{title}})
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

// recordWaiterProvenance records a provenance event on fp-waiter: a child row
// that, like the waiter's dependency, cascades from the issue through a
// foreign key.
func recordWaiterProvenance(t *testing.T, store *EmbeddedDoltStore) {
	t.Helper()
	ref := "https://example.com/pr/1"
	if _, _, err := store.RecordProvenanceEvent(t.Context(), types.ProvenanceEvent{
		IssueID: "fp-waiter", Kind: types.ProvClaim, Ref: &ref, Source: "test",
	}); err != nil {
		t.Fatalf("record provenance on fp-waiter: %v", err)
	}
}

const waiterProvenance = "SELECT COUNT(*) FROM provenance_events %s WHERE issue_id = 'fp-waiter'"

// assertSyncSettled fails when the sync left merge conflicts or constraint
// violations in the working set.
func assertSyncSettled(t *testing.T, store *EmbeddedDoltStore) {
	t.Helper()
	if n := countAt(t, store, "", "SELECT COUNT(*) FROM dolt_conflicts %s"); n != 0 {
		t.Errorf("working set holds %d conflicted table(s) after the sync, want 0", n)
	}
	if n := countAt(t, store, "", "SELECT COUNT(*) FROM dolt_constraint_violations %s"); n != 0 {
		t.Errorf("working set holds %d table(s) with constraint violations after the sync, want 0", n)
	}
}

// AC-6: a peer that deletes, on the filtered tip, an issue the compensation
// touched does not wedge later syncs. Deleting the waiter the filter
// unblocked conflicts with the revert's is_blocked on the issue row; deleting
// an issue an excluded one depends on leaves the revert's dependency row
// without its target. Both now merge, and the delete reaches this store.
func TestSyncFilteredPushMergesPeerDeletes(t *testing.T) {
	t.Run("the waiter the filter unblocked", func(t *testing.T) {
		store := newFilteredPushStore(t)
		seedFilteredPushGraph(t, store)
		syncFilteredPeer(t, store, "first sync")
		recordWaiterProvenance(t, store)
		if got := countAt(t, store, "", waiterProvenance); got != 1 {
			t.Fatalf("fp-waiter provenance events = %d before the sync, want 1", got)
		}

		onPeer(t, store, "peer: delete the waiter",
			peerStmt{"DELETE FROM issues WHERE id = 'fp-waiter'", nil})
		syncFilteredPeer(t, store, "sync after the peer deleted the waiter")

		want := localGraph
		want.waiter, want.dep, want.waiterBlocked = 0, 0, 0
		if got := readFilteredGraph(t, store, ""); got != want {
			t.Errorf("local graph = %+v, want %+v", got, want)
		}
		var resolution string
		if err := store.withConn(t.Context(), false, func(tx *sql.Tx) error {
			return tx.QueryRowContext(t.Context(),
				"SELECT commit_hash FROM dolt_log WHERE message LIKE 'auto-resolve merge conflicts%' LIMIT 1").Scan(&resolution)
		}); err != nil {
			t.Fatalf("find the commit concluding the merge: %v", err)
		}
		if got := readFilteredGraph(t, store, resolution); got != want {
			t.Errorf("graph at the commit concluding the merge = %+v, want %+v (the delete's cascade must be committed with it)", got, want)
		}
		if got := countAt(t, store, resolution, waiterProvenance); got != 0 {
			t.Errorf("fp-waiter provenance events at the commit concluding the merge = %d, want 0 (the delete's cascade must be committed with it)", got)
		}
		if got := countAt(t, store, "", waiterProvenance); got != 0 {
			t.Errorf("local fp-waiter provenance events = %d, want 0", got)
		}
		if got := readFilteredGraph(t, store, peerTip(t, store)); got != (filteredGraphCounts{task: 1}) {
			t.Errorf("peer tip graph = %+v, want only fp-task", got)
		}
		syncFilteredPeer(t, store, "final sync")
		if got := readFilteredGraph(t, store, ""); got != want {
			t.Errorf("after the final sync, local graph = %+v, want %+v", got, want)
		}
		assertSyncSettled(t, store)
		assertOnlyMainBranch(t, store)
	})

	t.Run("an issue an excluded one depends on", func(t *testing.T) {
		store := newFilteredPushStore(t)
		seedFilteredPushGraph(t, store)
		ctx := t.Context()
		base := &types.Issue{ID: "fp-base", Title: "chore prerequisite", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 1}
		if err := store.CreateIssue(ctx, base, "test"); err != nil {
			t.Fatalf("create fp-base: %v", err)
		}
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: "fp-chore", DependsOnID: "fp-base", Type: types.DepBlocks}, "test"); err != nil {
			t.Fatalf("block chore on fp-base: %v", err)
		}
		syncFilteredPeer(t, store, "first sync")

		onPeer(t, store, "peer: delete the chore's prerequisite",
			peerStmt{"DELETE FROM issues WHERE id = 'fp-base'", nil})
		syncFilteredPeer(t, store, "sync after the peer deleted the prerequisite")

		if got := readFilteredGraph(t, store, ""); got != localGraph {
			t.Errorf("local graph = %+v, want %+v", got, localGraph)
		}
		for query, want := range map[string]int{
			"SELECT COUNT(*) FROM issues %s WHERE id = 'fp-base'":                                                  0,
			"SELECT COUNT(*) FROM dependencies %s WHERE issue_id = 'fp-chore' AND depends_on_issue_id = 'fp-base'": 0,
			"SELECT COUNT(*) FROM issues %s WHERE id = 'fp-chore' AND is_blocked = 1":                              0,
		} {
			if got := countAt(t, store, "", query); got != want {
				t.Errorf("%s = %d, want %d", query, got, want)
			}
		}
		syncFilteredPeer(t, store, "final sync")
		assertSyncSettled(t, store)
		assertOnlyMainBranch(t, store)
	})
}

// A peer's own issue of a type this store excludes is the peer's, not this
// store's to withhold: the filter keeps the excluded issues the peer already
// holds, so publishing does not delete them from the peer's tip.
func TestSyncFilteredPushKeepsExcludedIssuesThePeerHolds(t *testing.T) {
	store := newFilteredPushStore(t)
	seedFilteredPushGraph(t, store)
	syncFilteredPeer(t, store, "first sync")

	onPeer(t, store, "peer: create its own chore", peerStmt{`INSERT INTO issues
		(id, title, description, design, acceptance_criteria, notes,
		 issue_type, status, priority, created_at, updated_at)
		VALUES ('fp-peer-chore', 'peer chore', '', '', '', '', 'chore', 'open', 1, NOW(), NOW())`, nil})
	syncFilteredPeer(t, store, "sync after the peer published its chore")
	syncFilteredPeer(t, store, "sync again")

	const peerChore = "SELECT COUNT(*) FROM issues %s WHERE id = 'fp-peer-chore'"
	tip := peerTip(t, store)
	if got := countAt(t, store, tip, peerChore); got != 1 {
		t.Errorf("peer tip holds fp-peer-chore %d time(s), want 1 (the filter deleted the peer's own issue)", got)
	}
	if got := countAt(t, store, "", peerChore); got != 1 {
		t.Errorf("local fp-peer-chore rows = %d, want 1", got)
	}
	if got := readFilteredGraph(t, store, tip); got != publishedGraph {
		t.Errorf("peer tip graph = %+v, want %+v (this store's own chore stays withheld)", got, publishedGraph)
	}
	if got := readFilteredGraph(t, store, ""); got != localGraph {
		t.Errorf("local graph = %+v, want %+v", got, localGraph)
	}
}

// addFetchedRemote adds name, a file:// remote holding the store's committed
// history, and fetches it, so its tracking ref is a copy the filter can read.
func addFetchedRemote(t *testing.T, store *EmbeddedDoltStore, name string) string {
	t.Helper()
	ctx := t.Context()
	if err := store.AddRemote(ctx, name, "file://"+filepath.Join(t.TempDir(), name+"-remote")); err != nil {
		t.Fatalf("AddRemote %s: %v", name, err)
	}
	if _, err := store.CommitPending(ctx, "test"); err != nil {
		t.Fatalf("commit before seeding %s: %v", name, err)
	}
	if err := store.PushTo(ctx, name); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	if err := store.Fetch(ctx, name); err != nil {
		t.Fatalf("fetch %s: %v", name, err)
	}
	return "remotes/" + name + "/main"
}

// countAfterMerge merges ref into a temporary branch made from base, as the
// workspace whose tip is base does on its next sync, and runs query, a
// SELECT COUNT(*), on the result.
func countAfterMerge(t *testing.T, store *EmbeddedDoltStore, base, ref, query string) int {
	t.Helper()
	ctx := t.Context()
	var n int
	if err := store.withPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
		for _, stmt := range []peerStmt{
			{"CALL DOLT_BRANCH('merge-probe', ?)", []any{base}},
			{"CALL DOLT_CHECKOUT('merge-probe')", nil},
			{"CALL DOLT_MERGE(?)", []any{ref}},
		} {
			if _, err := db.ExecContext(ctx, stmt.q, stmt.args...); err != nil {
				return fmt.Errorf("%s: %w", stmt.q, err)
			}
		}
		if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			return fmt.Errorf("%s: %w", query, err)
		}
		for _, q := range []string{"CALL DOLT_CHECKOUT('main')", "CALL DOLT_BRANCH('-D', 'merge-probe')"} {
			if _, err := db.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("%s: %w", q, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("merge %s into %s: %v", ref, base, err)
	}
	return n
}

// In a mesh, a peer's issue of a type this store excludes reaches this store
// from that peer. Withholding it from a second peer that lacks it would
// record its deletion in the tip that peer gets, and the owner applies the
// deletion when it merges that tip. The filter keeps every excluded issue a
// peer's fetched copy holds. origin's copy does not count: bd dolt push
// sends origin everything, this store's own chore included.
func TestSyncFilteredPushKeepsExcludedIssuesAnyPeerHolds(t *testing.T) {
	store := newFilteredPushStore(t)
	ctx := t.Context()
	townB := addFetchedRemote(t, store, "town-b")
	seedFilteredPushGraph(t, store)
	origin := addFetchedRemote(t, store, "origin")
	if got := readFilteredGraph(t, store, origin); got != localGraph {
		t.Fatalf("origin graph = %+v, want %+v (origin must hold this store's chore)", got, localGraph)
	}
	syncFilteredPeer(t, store, "first sync")

	onPeer(t, store, "peer: create its own chore", peerStmt{`INSERT INTO issues
		(id, title, description, design, acceptance_criteria, notes,
		 issue_type, status, priority, created_at, updated_at)
		VALUES ('fp-peer-chore', 'peer chore', '', '', '', '', 'chore', 'open', 1, NOW(), NOW())`, nil})
	syncFilteredPeer(t, store, "sync after the peer published its chore")
	result, err := store.Sync(ctx, "town-b", "")
	if err != nil || result.PushError != nil || !result.Pushed {
		t.Fatalf("Sync with town-b: err = %v, result = %+v; want a successful push", err, result)
	}
	if err := store.Fetch(ctx, "town-b"); err != nil {
		t.Fatalf("fetch town-b: %v", err)
	}

	const peerChore = "SELECT COUNT(*) FROM issues %s WHERE id = 'fp-peer-chore'"
	if got := countAt(t, store, townB, peerChore); got != 1 {
		t.Errorf("town-b tip holds fp-peer-chore %d time(s), want 1 (withholding it records its deletion)", got)
	}
	if got := readFilteredGraph(t, store, townB); got != publishedGraph {
		t.Errorf("town-b tip graph = %+v, want %+v (this store's own chore stays withheld; origin's copy keeps nothing)", got, publishedGraph)
	}
	if got := countAfterMerge(t, store, peerTip(t, store), townB, fmt.Sprintf(peerChore, "")); got != 1 {
		t.Errorf("the peer holds fp-peer-chore %d time(s) after merging town-b's tip, want 1", got)
	}
	if got := readFilteredGraph(t, store, ""); got != localGraph {
		t.Errorf("local graph = %+v, want %+v", got, localGraph)
	}
	assertOnlyMainBranch(t, store)
}

// A conflict the settle path leaves for the operator stops the sync with the
// conflicting tables and the merge aborted; --strategy then resolves it. Sync
// used to merge under autocommit, where a real conflict rolled back before
// its strategy could run.
func TestSyncStrategyResolvesConflictsTheSettlePathDeclines(t *testing.T) {
	for _, tc := range []struct {
		strategy string
		// taskRows is how many fp-task rows the strategy leaves, locally and
		// on the peer's tip: theirs takes the peer's delete, ours keeps the
		// local edit and publishes it.
		taskRows int
	}{{"theirs", 0}, {"ours", 1}} {
		t.Run(tc.strategy, func(t *testing.T) {
			store := newFilteredPushStore(t)
			seedFilteredPushGraph(t, store)
			syncFilteredPeer(t, store, "first sync")

			onPeer(t, store, "peer: delete the task", peerStmt{"DELETE FROM issues WHERE id = 'fp-task'", nil})
			execPeerSQL(t, store, "UPDATE issues SET title = 'edited locally' WHERE id = 'fp-task'")
			const editedTask = "SELECT COUNT(*) FROM issues %s WHERE id = 'fp-task' AND title = 'edited locally'"

			result, err := store.Sync(t.Context(), filteredPushPeer, "")
			if err == nil || len(result.Conflicts) == 0 || result.ConflictsResolved || result.Merged {
				t.Fatalf("Sync without a strategy: err = %v, result = %+v; want the conflicts reported and nothing merged", err, result)
			}
			if !strings.Contains(err.Error(), "merge conflicts in issues require resolution") {
				t.Errorf("Sync error = %q, want it to name the conflicting table", err)
			}
			if got := countAt(t, store, "", editedTask); got != 1 {
				t.Errorf("local fp-task with the local edit = %d row(s) after the refused merge, want 1", got)
			}
			assertSyncSettled(t, store)

			result, err = store.Sync(t.Context(), filteredPushPeer, tc.strategy)
			if err != nil || !result.ConflictsResolved || !result.Merged || !result.Pushed {
				t.Fatalf("Sync --strategy %s: err = %v, result = %+v; want the conflicts resolved and pushed", tc.strategy, err, result)
			}
			if got := countAt(t, store, "", editedTask); got != tc.taskRows {
				t.Errorf("local fp-task rows = %d after --strategy %s, want %d", got, tc.strategy, tc.taskRows)
			}
			if got := countAt(t, store, peerTip(t, store), editedTask); got != tc.taskRows {
				t.Errorf("peer tip fp-task rows = %d after --strategy %s, want %d", got, tc.strategy, tc.taskRows)
			}
			if got := countAt(t, store, "", "SELECT COUNT(*) FROM dolt_merge_status %s WHERE is_merging"); got != 0 {
				t.Errorf("--strategy %s left the merge open", tc.strategy)
			}
			assertSyncSettled(t, store)
			assertOnlyMainBranch(t, store)
		})
	}
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
				assertWritableAfterFilteredPushFailure(t, store)
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

// assertWritableAfterFilteredPushFailure checks that the store still accepts
// ordinary writes after a failed filtered push: the staging steps run on the
// store's shared session, so leaving it on a deleted temporary branch would
// break every later write.
func assertWritableAfterFilteredPushFailure(t *testing.T, store *EmbeddedDoltStore) {
	t.Helper()
	ctx := t.Context()
	after := &types.Issue{ID: "fp-after", Title: "written after the failure", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 1}
	if err := store.CreateIssue(ctx, after, "test"); err != nil {
		t.Fatalf("create issue after failed filtered push: %v", err)
	}
	if err := store.Commit(ctx, "write after failed filtered push"); err != nil {
		t.Fatalf("commit after failed filtered push: %v", err)
	}
	if n := countAt(t, store, "HEAD", "SELECT COUNT(*) FROM issues %s WHERE id = ?", after.ID); n != 1 {
		t.Errorf("post-failure write: committed fp-after rows = %d, want 1", n)
	}
}

// stubFilteredPushTransport replaces the embedded push transport for the test
// with fn and returns a pointer to the number of times it ran.
func stubFilteredPushTransport(t *testing.T, fn func(ctx context.Context, db versioncontrolops.DBConn) error) *int {
	t.Helper()
	calls := 0
	prevPush := vcPush
	vcPush = func(ctx context.Context, db versioncontrolops.DBConn, _, _, _ string) error {
		calls++
		return fn(ctx, db)
	}
	t.Cleanup(func() { vcPush = prevPush })
	return &calls
}

// AC-7: a failure while filtering never falls back to publishing: the
// transport is not reached, and the local data, branches and session survive.
func TestEmbeddedFilteredPushDeleteFailureFailsClosed(t *testing.T) {
	store := newFilteredPushStore(t)
	seedFilteredPushGraph(t, store)
	injectFilteredPushStep(t, versioncontrolops.FilteredPushStepDelete, nil)
	transports := stubFilteredPushTransport(t, func(context.Context, versioncontrolops.DBConn) error {
		return errors.New("transport must not run after a filtering failure")
	})

	err := store.filteredPushToPeer(t.Context(), filteredPushPeer, []string{"chore", "wisp"})
	if !errors.Is(err, errInjectedFilteredPush) {
		t.Fatalf("filteredPushToPeer error = %v, want the filtering failure", err)
	}
	if *transports != 0 {
		t.Errorf("transport ran %d time(s) after the filtering failure, want 0", *transports)
	}
	if got := readFilteredGraph(t, store, ""); got != localGraph {
		t.Errorf("local graph = %+v, want %+v", got, localGraph)
	}
	if got := readFilteredGraph(t, store, peerTip(t, store)); got != (filteredGraphCounts{}) {
		t.Errorf("peer tip graph = %+v, want the seed tip (nothing pushed)", got)
	}
	assertOnlyMainBranch(t, store)
	assertWritableAfterFilteredPushFailure(t, store)
}

// AC-7: the real transport failing after the local compensation reports the
// transport's error and keeps the excluded rows. The compensation is already
// on the local branch when the transport runs, so the next sync cannot
// fast-forward over the excluded rows.
func TestEmbeddedFilteredPushTransportFailurePreservesLocalData(t *testing.T) {
	store := newFilteredPushStore(t)
	seedFilteredPushGraph(t, store)
	transportErr := errors.New("Error 1227: access denied to remote")
	var revertsAtTransport, filterCommitsAtTransport int
	transports := stubFilteredPushTransport(t, func(ctx context.Context, db versioncontrolops.DBConn) error {
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_log WHERE message LIKE 'Revert %'").Scan(&revertsAtTransport); err != nil {
			return fmt.Errorf("count reverts at transport: %w", err)
		}
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_log WHERE message = ?", versioncontrolops.FilteredPushCommitMsg).Scan(&filterCommitsAtTransport); err != nil {
			return fmt.Errorf("count filter commits at transport: %w", err)
		}
		return transportErr
	})

	err := store.filteredPushToPeer(t.Context(), filteredPushPeer, []string{"chore", "wisp"})
	if !errors.Is(err, transportErr) {
		t.Fatalf("filteredPushToPeer error = %v, want the transport failure", err)
	}
	if *transports != 1 {
		t.Fatalf("transport ran %d time(s), want 1", *transports)
	}
	if revertsAtTransport == 0 || filterCommitsAtTransport == 0 {
		t.Errorf("at transport time the local branch had %d filter commit(s) and %d revert(s); want the compensation merged before the transport",
			filterCommitsAtTransport, revertsAtTransport)
	}
	if got := readFilteredGraph(t, store, ""); got != localGraph {
		t.Errorf("local graph = %+v, want %+v", got, localGraph)
	}
	assertOnlyMainBranch(t, store)
	assertWritableAfterFilteredPushFailure(t, store)
}
