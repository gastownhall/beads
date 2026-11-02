//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// withFederationExcludeTypes points the process-wide federation config at
// exclude for the duration of the test, restoring the prior value after. The
// embedded store reads federation.exclude_types through the same global config
// as the server store, and viper is process-wide, so the test binary's own
// configuration is borrowed and returned (tests in this package run
// sequentially; none of the others read federation config).
func withFederationExcludeTypes(t *testing.T, exclude []string) {
	t.Helper()
	t.Chdir(t.TempDir())
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	prior := config.GetFederationConfig().ExcludeTypes
	config.Set("federation.exclude_types", exclude)
	t.Cleanup(func() {
		config.Set("federation.exclude_types", prior)
	})
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

	task := &types.Issue{
		ID:        "fedex-task",
		Title:     "Shareable task",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	chore := &types.Issue{
		ID:        "fedex-chore",
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

	// A file:// remote, initialized by an unfiltered push before exclusions
	// are configured (the same bootstrap a real deployment gets from
	// `bd dolt push`).
	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("create remote dir: %v", err)
	}
	remoteURL := "file://" + remoteDir
	te.exec(t, ctx, "CALL DOLT_REMOTE('add', 'peer', ?)", remoteURL)
	if err := te.store.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("initial push: %v", err)
	}

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
	cloneBeads := filepath.Join(t.TempDir(), "clone")
	cloneData := filepath.Join(cloneBeads, "embeddeddolt")
	if err := os.MkdirAll(cloneData, 0o700); err != nil {
		t.Fatalf("create clone dir: %v", err)
	}
	withRawConn(t, cloneData, "", func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_CLONE(?, ?)", remoteURL, te.database)
	})
	withRawConn(t, cloneData, te.database, func(conn *sql.Conn) {
		assertCloneIssueCount(t, ctx, conn, task.ID, 1)
		assertCloneIssueCount(t, ctx, conn, chore.ID, 0)
	})

	// Local data survives the filtered push.
	te.assertRowExists(t, ctx, "issues", task.ID)
	te.assertRowExists(t, ctx, "issues", chore.ID)

	// The staging branch is gone, whatever the push outcome was.
	var branches string
	te.queryScalar(t, ctx,
		"SELECT COALESCE(GROUP_CONCAT(name ORDER BY name SEPARATOR ','), '') FROM dolt_branches",
		nil, &branches)
	if strings.Contains(branches, versioncontrolops.FederationStagingBranchPrefix) {
		t.Errorf("staging branch left behind after sync; dolt_branches = %q", branches)
	}
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

// TestEmbeddedFilteredPushEmptyExclusionsDelegates pins the delegation
// contract: with exclusions opted out (empty exclude_types), Sync pushes
// exactly as PushTo does — no staging branch, and every local row reaches
// the peer.
func TestEmbeddedFilteredPushEmptyExclusionsDelegates(t *testing.T) {
	te := newTestEnv(t, "fedopt")
	ctx := t.Context()

	task := &types.Issue{
		ID:        "fedopt-task",
		Title:     "Unfiltered task",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	chore := &types.Issue{
		ID:        "fedopt-chore",
		Title:     "Excluded by other configs, not this one",
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

	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("create remote dir: %v", err)
	}
	remoteURL := "file://" + remoteDir
	te.exec(t, ctx, "CALL DOLT_REMOTE('add', 'peer', ?)", remoteURL)
	if err := te.store.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("initial push: %v", err)
	}

	// Empty exclude_types is the documented opt-out (federation.exclude_types: []).
	withFederationExcludeTypes(t, []string{})

	if _, err := te.store.Sync(ctx, "peer", ""); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	cloneBeads := filepath.Join(t.TempDir(), "clone")
	cloneData := filepath.Join(cloneBeads, "embeddeddolt")
	if err := os.MkdirAll(cloneData, 0o700); err != nil {
		t.Fatalf("create clone dir: %v", err)
	}
	withRawConn(t, cloneData, "", func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_CLONE(?, ?)", remoteURL, te.database)
	})
	withRawConn(t, cloneData, te.database, func(conn *sql.Conn) {
		assertCloneIssueCount(t, ctx, conn, task.ID, 1)
		assertCloneIssueCount(t, ctx, conn, chore.ID, 1)
	})

	var branches string
	te.queryScalar(t, ctx,
		"SELECT COALESCE(GROUP_CONCAT(name ORDER BY name SEPARATOR ','), '') FROM dolt_branches",
		nil, &branches)
	if strings.Contains(branches, versioncontrolops.FederationStagingBranchPrefix) {
		t.Errorf("staging branch created without exclusions; dolt_branches = %q", branches)
	}
}