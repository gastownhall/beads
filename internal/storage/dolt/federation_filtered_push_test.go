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

// serverPeerStmt is one statement onServerPeer runs on the peer's tip.
type serverPeerStmt struct {
	q    string
	args []any
}

// onServerPeer commits stmts on top of the peer's current tip and pushes the
// commit there, as another workspace syncing with the peer would.
func onServerPeer(t *testing.T, ctx context.Context, store *DoltStore, message string, stmts ...serverPeerStmt) {
	t.Helper()
	tip := serverPeerTip(t, ctx, store)
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin peer-edit connection: %v", err)
	}
	defer conn.Close()
	steps := append([]serverPeerStmt{
		{"CALL DOLT_BRANCH('peer-work', ?)", []any{tip}},
		{"CALL DOLT_CHECKOUT('peer-work')", nil},
	}, stmts...)
	steps = append(steps,
		serverPeerStmt{"CALL DOLT_COMMIT('-Am', ?)", []any{message}},
		serverPeerStmt{"CALL DOLT_PUSH(?, ?)", []any{serverFilteredPushPeer, "peer-work:" + store.branch}},
		serverPeerStmt{"CALL DOLT_CHECKOUT(?)", []any{store.branch}},
		serverPeerStmt{"CALL DOLT_BRANCH('-D', 'peer-work')", nil},
	)
	for _, stmt := range steps {
		if _, err := conn.ExecContext(ctx, stmt.q, stmt.args...); err != nil {
			t.Fatalf("%s: %s: %v", message, stmt.q, err)
		}
	}
}

// commitOnServerPeer commits an edit to fp-task on top of the peer's current
// tip and pushes it there, as another workspace syncing with the peer would.
func commitOnServerPeer(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	onServerPeer(t, ctx, store, "peer: edit published task",
		serverPeerStmt{"UPDATE issues SET title = 'edited on peer' WHERE id = 'fp-task'", nil})
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

// serverCount runs query, a SELECT COUNT(*) with a %s placeholder where the
// AS OF clause goes, at ref ("" = the working set).
func serverCount(t *testing.T, ctx context.Context, store *DoltStore, ref, query string) int {
	t.Helper()
	asOf := ""
	if ref != "" {
		asOf = fmt.Sprintf("AS OF '%s'", ref)
	}
	var n int
	if err := store.db.QueryRowContext(ctx, fmt.Sprintf(query, asOf)).Scan(&n); err != nil {
		t.Fatalf("count %q at %q: %v", query, ref, err)
	}
	return n
}

// recordServerWaiterProvenance records a provenance event on fp-waiter: a
// child row that, like the waiter's dependency, cascades from the issue
// through a foreign key.
func recordServerWaiterProvenance(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	ref := "https://example.com/pr/1"
	if _, _, err := store.RecordProvenanceEvent(ctx, types.ProvenanceEvent{
		IssueID: "fp-waiter", Kind: types.ProvClaim, Ref: &ref, Source: "test",
	}); err != nil {
		t.Fatalf("record provenance on fp-waiter: %v", err)
	}
}

const serverWaiterProvenance = "SELECT COUNT(*) FROM provenance_events %s WHERE issue_id = 'fp-waiter'"

// assertServerSyncSettled fails when the sync left merge conflicts or
// constraint violations in the working set.
func assertServerSyncSettled(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	if n := serverCount(t, ctx, store, "", "SELECT COUNT(*) FROM dolt_conflicts %s"); n != 0 {
		t.Errorf("working set holds %d conflicted table(s) after the sync, want 0", n)
	}
	if n := serverCount(t, ctx, store, "", "SELECT COUNT(*) FROM dolt_constraint_violations %s"); n != 0 {
		t.Errorf("working set holds %d table(s) with constraint violations after the sync, want 0", n)
	}
}

// AC-6: a peer that deletes, on the filtered tip, an issue the compensation
// touched does not wedge later syncs. Deleting the waiter the filter
// unblocked conflicts with the revert's is_blocked on the issue row; deleting
// an issue an excluded one depends on leaves the revert's dependency row
// without its target. Both now merge, and the delete reaches this store.
func TestServerSyncFilteredPushMergesPeerDeletes(t *testing.T) {
	t.Run("the waiter the filter unblocked", func(t *testing.T) {
		store, ctx := newServerFilteredPushStore(t)
		seedServerFilteredPushGraph(t, ctx, store)
		syncServerFilteredPeer(t, ctx, store, "first sync")
		recordServerWaiterProvenance(t, ctx, store)
		if got := serverCount(t, ctx, store, "", serverWaiterProvenance); got != 1 {
			t.Fatalf("fp-waiter provenance events = %d before the sync, want 1", got)
		}

		onServerPeer(t, ctx, store, "peer: delete the waiter",
			serverPeerStmt{"DELETE FROM issues WHERE id = 'fp-waiter'", nil})
		syncServerFilteredPeer(t, ctx, store, "sync after the peer deleted the waiter")

		want := serverLocalGraph
		want.waiter, want.dep, want.waiterBlocked = 0, 0, 0
		if got := readServerGraph(t, ctx, store, ""); got != want {
			t.Errorf("local graph = %+v, want %+v", got, want)
		}
		var resolution string
		if err := store.db.QueryRowContext(ctx,
			"SELECT commit_hash FROM dolt_log WHERE message LIKE 'auto-resolve merge conflicts%' LIMIT 1").Scan(&resolution); err != nil {
			t.Fatalf("find the commit concluding the merge: %v", err)
		}
		if got := readServerGraph(t, ctx, store, resolution); got != want {
			t.Errorf("graph at the commit concluding the merge = %+v, want %+v (the delete's cascade must be committed with it)", got, want)
		}
		if got := serverCount(t, ctx, store, resolution, serverWaiterProvenance); got != 0 {
			t.Errorf("fp-waiter provenance events at the commit concluding the merge = %d, want 0 (the delete's cascade must be committed with it)", got)
		}
		if got := serverCount(t, ctx, store, "", serverWaiterProvenance); got != 0 {
			t.Errorf("local fp-waiter provenance events = %d, want 0", got)
		}
		if got := readServerGraph(t, ctx, store, serverPeerTip(t, ctx, store)); got != (serverGraphCounts{task: 1}) {
			t.Errorf("peer tip graph = %+v, want only fp-task", got)
		}
		syncServerFilteredPeer(t, ctx, store, "final sync")
		if got := readServerGraph(t, ctx, store, ""); got != want {
			t.Errorf("after the final sync, local graph = %+v, want %+v", got, want)
		}
		assertServerSyncSettled(t, ctx, store)
		assertServerOnlySourceBranch(t, ctx, store)
	})

	t.Run("an issue an excluded one depends on", func(t *testing.T) {
		store, ctx := newServerFilteredPushStore(t)
		seedServerFilteredPushGraph(t, ctx, store)
		base := &types.Issue{ID: "fp-base", Title: "chore prerequisite", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 1}
		if err := store.CreateIssue(ctx, base, "test"); err != nil {
			t.Fatalf("create fp-base: %v", err)
		}
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: "fp-chore", DependsOnID: "fp-base", Type: types.DepBlocks}, "test"); err != nil {
			t.Fatalf("block chore on fp-base: %v", err)
		}
		syncServerFilteredPeer(t, ctx, store, "first sync")

		onServerPeer(t, ctx, store, "peer: delete the chore's prerequisite",
			serverPeerStmt{"DELETE FROM issues WHERE id = 'fp-base'", nil})
		syncServerFilteredPeer(t, ctx, store, "sync after the peer deleted the prerequisite")

		if got := readServerGraph(t, ctx, store, ""); got != serverLocalGraph {
			t.Errorf("local graph = %+v, want %+v", got, serverLocalGraph)
		}
		for query, want := range map[string]int{
			"SELECT COUNT(*) FROM issues %s WHERE id = 'fp-base'":                                                  0,
			"SELECT COUNT(*) FROM dependencies %s WHERE issue_id = 'fp-chore' AND depends_on_issue_id = 'fp-base'": 0,
			"SELECT COUNT(*) FROM issues %s WHERE id = 'fp-chore' AND is_blocked = 1":                              0,
		} {
			if got := serverCount(t, ctx, store, "", query); got != want {
				t.Errorf("%s = %d, want %d", query, got, want)
			}
		}
		syncServerFilteredPeer(t, ctx, store, "final sync")
		assertServerSyncSettled(t, ctx, store)
		assertServerOnlySourceBranch(t, ctx, store)
	})
}

// A peer's own issue of a type this store excludes is the peer's, not this
// store's to withhold: the filter keeps the excluded issues the peer already
// holds, so publishing does not delete them from the peer's tip.
func TestServerSyncFilteredPushKeepsExcludedIssuesThePeerHolds(t *testing.T) {
	store, ctx := newServerFilteredPushStore(t)
	seedServerFilteredPushGraph(t, ctx, store)
	syncServerFilteredPeer(t, ctx, store, "first sync")

	onServerPeer(t, ctx, store, "peer: create its own chore", serverPeerStmt{`INSERT INTO issues
		(id, title, description, design, acceptance_criteria, notes,
		 issue_type, status, priority, created_at, updated_at)
		VALUES ('fp-peer-chore', 'peer chore', '', '', '', '', 'chore', 'open', 1, NOW(), NOW())`, nil})
	syncServerFilteredPeer(t, ctx, store, "sync after the peer published its chore")
	syncServerFilteredPeer(t, ctx, store, "sync again")

	const peerChore = "SELECT COUNT(*) FROM issues %s WHERE id = 'fp-peer-chore'"
	tip := serverPeerTip(t, ctx, store)
	if got := serverCount(t, ctx, store, tip, peerChore); got != 1 {
		t.Errorf("peer tip holds fp-peer-chore %d time(s), want 1 (the filter deleted the peer's own issue)", got)
	}
	if got := serverCount(t, ctx, store, "", peerChore); got != 1 {
		t.Errorf("local fp-peer-chore rows = %d, want 1", got)
	}
	if got := readServerGraph(t, ctx, store, tip); got != serverPublishedGraph {
		t.Errorf("peer tip graph = %+v, want %+v (this store's own chore stays withheld)", got, serverPublishedGraph)
	}
	if got := readServerGraph(t, ctx, store, ""); got != serverLocalGraph {
		t.Errorf("local graph = %+v, want %+v", got, serverLocalGraph)
	}
}

// addFetchedServerRemote adds name, a file:// remote holding the store's
// committed history, and fetches it, so its tracking ref is a copy the filter
// can read.
func addFetchedServerRemote(t *testing.T, ctx context.Context, store *DoltStore, name string) string {
	t.Helper()
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
	return "remotes/" + name + "/" + store.branch
}

// serverCountAfterMerge merges ref into a temporary branch made from base, as
// the workspace whose tip is base does on its next sync, and runs query, a
// SELECT COUNT(*), on the result.
func serverCountAfterMerge(t *testing.T, ctx context.Context, store *DoltStore, base, ref, query string) int {
	t.Helper()
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin merge-probe connection: %v", err)
	}
	defer conn.Close()
	for _, stmt := range []serverPeerStmt{
		{"CALL DOLT_BRANCH('merge-probe', ?)", []any{base}},
		{"CALL DOLT_CHECKOUT('merge-probe')", nil},
		{"CALL DOLT_MERGE(?)", []any{ref}},
	} {
		if _, err := conn.ExecContext(ctx, stmt.q, stmt.args...); err != nil {
			t.Fatalf("merge %s into %s: %s: %v", ref, base, stmt.q, err)
		}
	}
	var n int
	if err := conn.QueryRowContext(ctx, query).Scan(&n); err != nil {
		t.Fatalf("merge %s into %s: %s: %v", ref, base, query, err)
	}
	for _, stmt := range []serverPeerStmt{
		{"CALL DOLT_CHECKOUT(?)", []any{store.branch}},
		{"CALL DOLT_BRANCH('-D', 'merge-probe')", nil},
	} {
		if _, err := conn.ExecContext(ctx, stmt.q, stmt.args...); err != nil {
			t.Fatalf("merge %s into %s: %s: %v", ref, base, stmt.q, err)
		}
	}
	return n
}

// In a mesh, a peer's issue of a type this store excludes reaches this store
// from that peer. Withholding it from a second peer that lacks it would
// record its deletion in the tip that peer gets, and the owner applies the
// deletion when it merges that tip. The filter keeps every excluded issue a
// peer's fetched copy holds. origin's copy does not count: bd dolt push
// sends origin everything, this store's own chore included.
func TestServerSyncFilteredPushKeepsExcludedIssuesAnyPeerHolds(t *testing.T) {
	store, ctx := newServerFilteredPushStore(t)
	townB := addFetchedServerRemote(t, ctx, store, "town-b")
	seedServerFilteredPushGraph(t, ctx, store)
	origin := addFetchedServerRemote(t, ctx, store, "origin")
	if got := readServerGraph(t, ctx, store, origin); got != serverLocalGraph {
		t.Fatalf("origin graph = %+v, want %+v (origin must hold this store's chore)", got, serverLocalGraph)
	}
	syncServerFilteredPeer(t, ctx, store, "first sync")

	onServerPeer(t, ctx, store, "peer: create its own chore", serverPeerStmt{`INSERT INTO issues
		(id, title, description, design, acceptance_criteria, notes,
		 issue_type, status, priority, created_at, updated_at)
		VALUES ('fp-peer-chore', 'peer chore', '', '', '', '', 'chore', 'open', 1, NOW(), NOW())`, nil})
	syncServerFilteredPeer(t, ctx, store, "sync after the peer published its chore")
	result, err := store.Sync(ctx, "town-b", "")
	if err != nil || result.PushError != nil || !result.Pushed {
		t.Fatalf("Sync with town-b: err = %v, result = %+v; want a successful push", err, result)
	}
	if err := store.Fetch(ctx, "town-b"); err != nil {
		t.Fatalf("fetch town-b: %v", err)
	}

	const peerChore = "SELECT COUNT(*) FROM issues %s WHERE id = 'fp-peer-chore'"
	if got := serverCount(t, ctx, store, townB, peerChore); got != 1 {
		t.Errorf("town-b tip holds fp-peer-chore %d time(s), want 1 (withholding it records its deletion)", got)
	}
	if got := readServerGraph(t, ctx, store, townB); got != serverPublishedGraph {
		t.Errorf("town-b tip graph = %+v, want %+v (this store's own chore stays withheld; origin's copy keeps nothing)", got, serverPublishedGraph)
	}
	if got := serverCountAfterMerge(t, ctx, store, serverPeerTip(t, ctx, store), townB, fmt.Sprintf(peerChore, "")); got != 1 {
		t.Errorf("the peer holds fp-peer-chore %d time(s) after merging town-b's tip, want 1", got)
	}
	if got := readServerGraph(t, ctx, store, ""); got != serverLocalGraph {
		t.Errorf("local graph = %+v, want %+v", got, serverLocalGraph)
	}
	assertServerOnlySourceBranch(t, ctx, store)
}

// A conflict the settle path leaves for the operator stops the sync with the
// conflicting tables and the merge rolled back; --strategy then resolves it.
// Sync used to merge under autocommit, where a real conflict rolled back
// before its strategy could run.
func TestServerSyncStrategyResolvesConflictsTheSettlePathDeclines(t *testing.T) {
	for _, tc := range []struct {
		strategy string
		// taskRows is how many fp-task rows the strategy leaves, locally and
		// on the peer's tip: theirs takes the peer's delete, ours keeps the
		// local edit and publishes it.
		taskRows int
	}{{"theirs", 0}, {"ours", 1}} {
		t.Run(tc.strategy, func(t *testing.T) {
			store, ctx := newServerFilteredPushStore(t)
			seedServerFilteredPushGraph(t, ctx, store)
			syncServerFilteredPeer(t, ctx, store, "first sync")

			onServerPeer(t, ctx, store, "peer: delete the task", serverPeerStmt{"DELETE FROM issues WHERE id = 'fp-task'", nil})
			if _, err := store.db.ExecContext(ctx, "UPDATE issues SET title = 'edited locally' WHERE id = 'fp-task'"); err != nil {
				t.Fatalf("edit fp-task locally: %v", err)
			}
			const editedTask = "SELECT COUNT(*) FROM issues %s WHERE id = 'fp-task' AND title = 'edited locally'"

			result, err := store.Sync(ctx, serverFilteredPushPeer, "")
			if err == nil || len(result.Conflicts) == 0 || result.ConflictsResolved || result.Merged {
				t.Fatalf("Sync without a strategy: err = %v, result = %+v; want the conflicts reported and nothing merged", err, result)
			}
			if !strings.Contains(err.Error(), "merge conflicts in issues require resolution") {
				t.Errorf("Sync error = %q, want it to name the conflicting table", err)
			}
			if got := serverCount(t, ctx, store, "", editedTask); got != 1 {
				t.Errorf("local fp-task with the local edit = %d row(s) after the refused merge, want 1", got)
			}
			assertServerSyncSettled(t, ctx, store)

			result, err = store.Sync(ctx, serverFilteredPushPeer, tc.strategy)
			if err != nil || !result.ConflictsResolved || !result.Merged || !result.Pushed {
				t.Fatalf("Sync --strategy %s: err = %v, result = %+v; want the conflicts resolved and pushed", tc.strategy, err, result)
			}
			if got := serverCount(t, ctx, store, "", editedTask); got != tc.taskRows {
				t.Errorf("local fp-task rows = %d after --strategy %s, want %d", got, tc.strategy, tc.taskRows)
			}
			if got := serverCount(t, ctx, store, serverPeerTip(t, ctx, store), editedTask); got != tc.taskRows {
				t.Errorf("peer tip fp-task rows = %d after --strategy %s, want %d", got, tc.strategy, tc.taskRows)
			}
			if got := serverCount(t, ctx, store, "", "SELECT COUNT(*) FROM dolt_merge_status %s WHERE is_merging"); got != 0 {
				t.Errorf("--strategy %s left the merge open", tc.strategy)
			}
			assertServerSyncSettled(t, ctx, store)
			assertServerOnlySourceBranch(t, ctx, store)
		})
	}
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
