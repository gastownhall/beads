//go:build integration

package dolt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// cliPullDeclineFixture is a server-mode store with its own CLI-accessible
// database and a git+file:// "origin", plus a peer clone of that remote that
// has pushed issue cliDeclineIssueID while the store created the same id
// locally: a real add/add issues conflict the resolver declines (GH#7839).
type cliPullDeclineFixture struct {
	store   *DoltStore
	baseDir string
	preHead string
}

const (
	cliDeclineIssueID    = "dc-same"
	cliDeclineLocalTitle = "local title"
	cliDeclinePeerTitle  = "peer title"
)

func setupCLIPullDeclineFixture(ctx context.Context, t *testing.T) *cliPullDeclineFixture {
	t.Helper()
	testutil.RequireDoltBinary(t)
	skipIfNoGit(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	baseDir := t.TempDir()
	remoteDir := filepath.Join(baseDir, "remote.git")
	runCmd(t, baseDir, "git", "init", "--bare", "-b", "main", remoteDir)
	seedDir := filepath.Join(baseDir, "seed")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runCmd(t, seedDir, "git", "init", "-b", "main")
	runCmd(t, seedDir, "git", "-c", "user.name=test", "-c", "user.email=test@example.com",
		"commit", "--allow-empty", "-m", "init")
	runCmd(t, seedDir, "git", "push", remoteDir, "main")
	remoteURL := "git+file://" + remoteDir

	doltDir := filepath.Join(baseDir, "server")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serverPort, _ := startLocalDoltServer(t, doltDir)
	store, err := New(ctx, &Config{
		Path:            doltDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      serverPort,
		ServerUser:      "root",
		AutoStart:       false,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        uniqueTestDBName(t),
		CreateIfMissing: true,
		Remote:          "origin",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// This store's own server serves CLIDir; the port TestMain exports names
	// the suite's server instead.
	clearServerPortEnv(t)
	if store.CLIDir() == "" || !store.hasCLIDatabase() {
		t.Fatalf("fixture store must have its own CLI-accessible database; CLIDir=%q", store.CLIDir())
	}
	store.branch = "main"

	if err := store.SetConfig(ctx, "issue_prefix", "dc"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := store.AddRemote(ctx, "origin", remoteURL); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	if _, err := store.CommitAll(ctx, "genesis"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if err := store.Push(ctx); err != nil {
		t.Fatalf("Push genesis: %v", err)
	}

	peerDir := filepath.Join(baseDir, "peer")
	doltClone(t, remoteURL, peerDir)
	sourceInsertIssue(t, peerDir, cliDeclineIssueID, cliDeclinePeerTitle)
	sourceCommitAndPush(t, peerDir, "peer adds "+cliDeclineIssueID)

	createCommitted(ctx, t, store, cliDeclineIssueID, cliDeclineLocalTitle)
	preHead, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	// The peer pushed from its own process; this store's sql-server last read
	// the remote during its own push and caches that view briefly.
	waitOutGitRemoteReadCache()
	return &cliPullDeclineFixture{store: store, baseDir: baseDir, preHead: preHead}
}

func createCommitted(ctx context.Context, t *testing.T, store *DoltStore, id, title string) {
	t.Helper()
	issue := &types.Issue{ID: id, Title: title, IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue %s: %v", id, err)
	}
	if err := store.Commit(ctx, "add "+id); err != nil {
		t.Fatalf("Commit %s: %v", id, err)
	}
}

// assertNoMergeLeft fails unless the working set holds no open merge and no
// conflict rows.
func assertNoMergeLeft(ctx context.Context, t *testing.T, store *DoltStore) {
	t.Helper()
	var merging bool
	if err := store.db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging); err != nil {
		t.Fatalf("read dolt_merge_status: %v", err)
	}
	if merging {
		t.Fatal("declined CLI pull left the merge open (dolt_merge_status.is_merging=1); want it aborted")
	}
	var conflicts int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&conflicts); err != nil {
		t.Fatalf("count dolt_conflicts: %v", err)
	}
	if conflicts != 0 {
		t.Fatalf("%d conflicted table(s) remain after the declined CLI pull", conflicts)
	}
}

// TestGitProtocolPullDeclinedConflictAbortsMerge pins GH#7839 end to end on
// the git-protocol CLI route: a pull whose add/add issues conflict the
// resolver declines must abort the merge the `dolt pull` subprocess opened
// and report the conflict as the SQL route's typed error (AC-1).
func TestGitProtocolPullDeclinedConflictAbortsMerge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	fx := setupCLIPullDeclineFixture(ctx, t)
	store := fx.store

	var pullErr error
	logged := captureRouteLog(t, func() { pullErr = store.Pull(ctx) })
	if !strings.Contains(logged, "dolt pull route: CLI") {
		t.Fatalf("Pull did not take the CLI route; log:\n%s", logged)
	}

	// AC-1: the declined conflict comes back as the SQL route's typed error,
	// naming issues and wrapping the subprocess's own failure, and nothing of
	// the merge is left behind.
	var mce *versioncontrolops.MergeConflictsError
	if !errors.As(pullErr, &mce) {
		t.Fatalf("Pull error = %v, want *MergeConflictsError", pullErr)
	}
	if tables := declinedTables(mce.Conflicts); !slices.Contains(tables, "issues") {
		t.Fatalf("MergeConflictsError names %v, want issues", tables)
	}
	if mce.MergeErr == nil || errors.Unwrap(pullErr) != mce.MergeErr {
		t.Fatalf("MergeConflictsError does not wrap the CLI pull's error (MergeErr=%v)", mce.MergeErr)
	}
	assertNoMergeLeft(ctx, t, store)
	if head, err := store.GetCurrentCommit(ctx); err != nil || head != fx.preHead {
		t.Fatalf("HEAD after the aborted pull = %q (err %v), want the pre-pull %q", head, err, fx.preHead)
	}
	if got, err := store.GetIssue(ctx, cliDeclineIssueID); err != nil || got.Title != cliDeclineLocalTitle {
		t.Fatalf("local %s after the aborted pull = %+v (err %v), want title %q", cliDeclineIssueID, got, err, cliDeclineLocalTitle)
	}
}

// TestGitProtocolPullDeclinedConflictRecovers is AC-2 of GH#7839: after a
// declined git-protocol CLI pull the store keeps accepting writes and a retry
// reports the same conflict instead of failing on a wedged working set, until
// an explicit strategy merge reconciles the histories.
func TestGitProtocolPullDeclinedConflictRecovers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	fx := setupCLIPullDeclineFixture(ctx, t)
	store := fx.store

	var pullErr error
	logged := captureRouteLog(t, func() { pullErr = store.Pull(ctx) })
	if !strings.Contains(logged, "dolt pull route: CLI") {
		t.Fatalf("Pull did not take the CLI route; log:\n%s", logged)
	}
	if pullErr == nil {
		t.Fatal("Pull = nil, want the declined conflict reported")
	}

	// Writes work again, and a retry reports the same semantic conflict
	// rather than tripping over a wedged merge in commitBeforePull.
	var mce *versioncontrolops.MergeConflictsError
	createCommitted(ctx, t, store, "dc-unrelated", "unrelated work after the aborted pull")
	waitOutGitRemoteReadCache()
	retryErr := store.Pull(ctx)
	if !errors.As(retryErr, &mce) {
		t.Fatalf("retried Pull error = %v, want *MergeConflictsError", retryErr)
	}
	if strings.Contains(retryErr.Error(), "commit pending changes before pull") {
		t.Fatalf("retried Pull failed in commitBeforePull: %v", retryErr)
	}
	assertNoMergeLeft(ctx, t, store)

	// The documented reconciliation: an explicit strategy merge of the
	// fetched remote-tracking ref, after which a pull has nothing to refuse.
	if _, err := store.MergeWithStrategy(ctx, "origin/main", "theirs"); err != nil {
		t.Fatalf("bd vc merge origin/main --strategy theirs: %v", err)
	}
	waitOutGitRemoteReadCache()
	if err := store.Pull(ctx); err != nil {
		t.Fatalf("Pull after strategy reconciliation: %v", err)
	}
	assertNoMergeLeft(ctx, t, store)
	if got, err := store.GetIssue(ctx, cliDeclineIssueID); err != nil || got.Title != cliDeclinePeerTitle {
		t.Fatalf("%s after --strategy theirs = %+v (err %v), want title %q", cliDeclineIssueID, got, err, cliDeclinePeerTitle)
	}
	if _, err := store.GetIssue(ctx, "dc-unrelated"); err != nil {
		t.Fatalf("dc-unrelated lost by the reconciliation: %v", err)
	}
}

// TestGitProtocolPeerPullDeclinedConflictAbortsMerge is the federation side of
// GH#7839 (AC-3): PullFrom over a git-protocol peer returns the declined
// conflicts as data with a nil error, as the SQL route does, and leaves no
// merge or conflict rows behind.
func TestGitProtocolPeerPullDeclinedConflictAbortsMerge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	fx := setupCLIPullDeclineFixture(ctx, t)
	store := fx.store

	if useCLI, err := store.prepareCLIRouteForPeerGitProtocol(ctx, "origin"); err != nil || !useCLI {
		t.Fatalf("peer origin must take the git-protocol CLI route (useCLI=%v, err=%v)", useCLI, err)
	}
	conflicts, err := store.PullFrom(ctx, "origin")
	if err != nil {
		t.Fatalf("PullFrom error = %v, want the conflicts as data with a nil error", err)
	}
	if tables := declinedTables(conflicts); !slices.Contains(tables, "issues") {
		t.Fatalf("PullFrom conflicts = %v, want issues", tables)
	}
	assertNoMergeLeft(ctx, t, store)
	if head, err := store.GetCurrentCommit(ctx); err != nil || head != fx.preHead {
		t.Fatalf("HEAD after the aborted peer pull = %q (err %v), want the pre-pull %q", head, err, fx.preHead)
	}
}
