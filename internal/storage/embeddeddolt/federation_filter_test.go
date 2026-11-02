//go:build cgo

package embeddeddolt_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// withFederationExcludeTypes points the process-wide federation config at
// exclude for the duration of the test. The embedded store reads
// federation.exclude_types through the same global config as the server
// store.
func withFederationExcludeTypes(t *testing.T, exclude []string) {
	t.Helper()
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Setenv("BEADS_DIR", t.TempDir())
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("federation.exclude_types", exclude)
}

// newFederationRemote adds a file:// remote named peer to te's store and
// initializes it with an unfiltered push, the bootstrap a real deployment
// gets from `bd dolt push`. It returns the remote URL.
func newFederationRemote(t *testing.T, ctx context.Context, te *testEnv) string {
	t.Helper()
	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("create remote dir: %v", err)
	}
	remoteURL := "file://" + remoteDir
	te.exec(t, ctx, "CALL DOLT_REMOTE('add', 'peer', ?)", remoteURL)
	if err := te.store.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("initial push: %v", err)
	}
	return remoteURL
}

// createTaskAndChore creates and commits a task and a chore named
// <prefix>-task and <prefix>-chore.
func createTaskAndChore(t *testing.T, ctx context.Context, te *testEnv, prefix string) (task, chore *types.Issue) {
	t.Helper()
	task = &types.Issue{
		ID:        prefix + "-task",
		Title:     "Shareable task",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	chore = &types.Issue{
		ID:        prefix + "-chore",
		Title:     "Private chore",
		IssueType: types.TypeChore,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	for _, issue := range []*types.Issue{task, chore} {
		if err := te.store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	if err := te.store.Commit(ctx, "create test issues"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return task, chore
}

// TestEmbeddedSyncAppliesExcludeTypes is the embedded counterpart of the
// server store's filtered push: with federation.exclude_types configured,
// Sync must publish a peer tip that lacks the excluded rows while the local
// store keeps them, and must not leave its temporary staging branch behind.
// At the base commit embedded Sync pushed unconditionally, so the peer tip
// carried the excluded rows (GH#7890).
func TestEmbeddedSyncAppliesExcludeTypes(t *testing.T) {
	te := newTestEnv(t, "fedex")
	ctx := t.Context()
	task, chore := createTaskAndChore(t, ctx, te, "fedex")
	remoteURL := newFederationRemote(t, ctx, te)

	// Now exclude the chore type and sync: the filtered push must replace
	// the peer's main with a tip that has no chore row.
	withFederationExcludeTypes(t, []string{"chore"})

	result, err := te.store.Sync(ctx, "peer", "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PushError != nil {
		t.Fatalf("sync push error: %v", result.PushError)
	}
	if !result.Pushed {
		t.Fatal("sync did not push")
	}

	// What the peer actually received: clone the remote and read its tip.
	withRawConn(t, cloneFederationRemote(t, ctx, remoteURL, te.database), te.database, func(conn *sql.Conn) {
		assertCloneIssueCount(t, ctx, conn, task.ID, 1)
		assertCloneIssueCount(t, ctx, conn, chore.ID, 0)
	})

	// Local data survives the filtered push.
	te.assertRowExists(t, ctx, "issues", task.ID)
	te.assertRowExists(t, ctx, "issues", chore.ID)

	assertNoFederationTempBranches(t, ctx, te)
}

func assertCloneIssueCount(t *testing.T, ctx context.Context, conn *sql.Conn, id string, want int) {
	t.Helper()
	var got int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("count %s in clone: %v", id, err)
	}
	if got != want {
		t.Errorf("clone row %s = %d, want %d", id, got, want)
	}
}

// assertNoFederationTempBranches fails when any filtered-push temporary
// branch survives the operation.
func assertNoFederationTempBranches(t *testing.T, ctx context.Context, te *testEnv) {
	t.Helper()
	var branches string
	te.queryScalar(t, ctx,
		"SELECT COALESCE(GROUP_CONCAT(name ORDER BY name SEPARATOR ','), '') FROM dolt_branches",
		nil, &branches)
	for _, prefix := range []string{versioncontrolops.FilteredPushStagingPrefix} {
		if strings.Contains(branches, prefix) {
			t.Errorf("temporary branch with prefix %q left behind; dolt_branches = %q", prefix, branches)
		}
	}
}

// cloneFederationRemote clones remoteURL (a file:// Dolt remote) into a fresh
// temporary directory and returns the clone's data directory, ready for a
// withRawConn on database.
func cloneFederationRemote(t *testing.T, ctx context.Context, remoteURL, database string) string {
	t.Helper()
	cloneData := filepath.Join(t.TempDir(), "clone", "embeddeddolt")
	if err := os.MkdirAll(cloneData, 0o700); err != nil {
		t.Fatalf("create clone dir: %v", err)
	}
	withRawConn(t, cloneData, "", func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_CLONE(?, ?)", remoteURL, database)
	})
	return cloneData
}

// TestEmbeddedFilteredPushEmptyExclusionsDelegates pins the delegation
// contract: with exclusions opted out (empty exclude_types), Sync pushes
// exactly as PushTo does — no staging branch, and every local row reaches
// the peer.
func TestEmbeddedFilteredPushEmptyExclusionsDelegates(t *testing.T) {
	te := newTestEnv(t, "fedopt")
	ctx := t.Context()
	task, chore := createTaskAndChore(t, ctx, te, "fedopt")
	remoteURL := newFederationRemote(t, ctx, te)

	// Empty exclude_types is the documented opt-out (federation.exclude_types: []).
	withFederationExcludeTypes(t, []string{})

	if _, err := te.store.Sync(ctx, "peer", ""); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	withRawConn(t, cloneFederationRemote(t, ctx, remoteURL, te.database), te.database, func(conn *sql.Conn) {
		assertCloneIssueCount(t, ctx, conn, task.ID, 1)
		assertCloneIssueCount(t, ctx, conn, chore.ID, 1)
	})
	assertNoFederationTempBranches(t, ctx, te)
}

// TestFilteredFederationSyncExcludesWispsAndWarnsOnce: a sync that filters
// nothing does not warn; the first sync whose push drops excluded issues
// warns that history still holds them, and later filtered syncs in the same
// process stay quiet. The excluded row is a durable-plane ephemeral issue,
// which "wisp" matches.
func TestFilteredFederationSyncExcludesWispsAndWarnsOnce(t *testing.T) {
	ctx := t.Context()
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	te := newTestEnv(t, "filter")
	peer := &storage.FederationPeer{
		Name: "filter-peer", RemoteURL: "file://" + filepath.Join(t.TempDir(), "remote"),
	}
	if err := te.store.AddFederationPeer(ctx, peer); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}
	kept := &types.Issue{
		ID: "filter-kept", Title: "kept", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask,
	}
	if err := te.store.CreateIssue(ctx, kept, "tester"); err != nil {
		t.Fatalf("CreateIssue(%s): %v", kept.ID, err)
	}
	if err := te.store.PushTo(ctx, peer.Name); err != nil {
		t.Fatalf("initial PushTo: %v", err)
	}

	withFederationExcludeTypes(t, []string{"wisp"})
	previousWriter := versioncontrolops.FederationWarnWriter
	var warnings bytes.Buffer
	versioncontrolops.FederationWarnWriter = &warnings
	versioncontrolops.ResetExcludedHistoryWarningForTesting()
	t.Cleanup(func() {
		versioncontrolops.FederationWarnWriter = previousWriter
		versioncontrolops.ResetExcludedHistoryWarningForTesting()
	})

	sync := func(label string) {
		t.Helper()
		result, err := te.store.Sync(ctx, peer.Name, "")
		if err != nil {
			t.Fatalf("%s: Sync: %v", label, err)
		}
		if !result.Pushed || result.PushError != nil {
			t.Fatalf("%s: Sync push result: pushed=%t push error=%v", label, result.Pushed, result.PushError)
		}
	}

	sync("sync with nothing to exclude")
	if got := warnings.String(); got != "" {
		t.Fatalf("warning after a sync that filtered nothing = %q, want none", got)
	}

	// A durable-plane ephemeral row: current CreateIssue routes wisps to
	// ignored tables, which never leave the workspace.
	const excludedID = "filter-excluded"
	te.exec(t, ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral)
		VALUES (?, 'excluded', '', '', '', '', 'open', 2, 'task', 1)`, excludedID)

	sync("first filtered sync")
	if err := te.store.Fetch(ctx, peer.Name); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var excludedCount, keptCount int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues AS OF 'remotes/filter-peer/main' WHERE id = ?",
		[]any{excludedID}, &excludedCount)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues AS OF 'remotes/filter-peer/main' WHERE id = ?",
		[]any{kept.ID}, &keptCount)
	if excludedCount != 0 || keptCount != 1 {
		t.Fatalf("pushed tip counts: excluded=%d kept=%d, want 0 and 1", excludedCount, keptCount)
	}
	te.assertRowExists(t, ctx, "issues", excludedID)

	sync("second filtered sync")
	wantWarning := "Warning: federation.exclude_types removed 1 issue(s) from this push, but earlier commits in the pushed history still contain them; a peer that reads history can recover them. See https://beads.gascity.com/multi-agent/federation\n"
	if got := warnings.String(); got != wantWarning {
		t.Fatalf("warning = %q, want %q", got, wantWarning)
	}
}
