package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// These tests cover GH#5564 (bd-sesknw) for server-mode federation sync:
// `bd federation sync --strategy ours|theirs` used to merge through the bare
// DoltStore.Merge, whose CALL DOLT_MERGE runs on the shared pool inside an
// implicit-autocommit transaction — the exact shape #4992 fixed for
// `bd vc merge --strategy`. A real conflict is rejected (Error 1105) before
// the strategy can apply, so Sync's resolve/commit tail was dead code on
// genuine conflicts. Sync now dispatches an explicit strategy to
// MergeWithStrategy, the pinned-session merge/resolve/repair/commit sequence
// vc merge uses.

// syncPeerFixture is the two-clone federation shape these tests use: a local
// store with one committed seed issue, a server-local file:// remote, and a
// sibling peer database cloned from it (TestPullFromSettlesFKCascadeViolations'
// shape). The peer lives inside the test Dolt server because a container's
// host-side file remotes are invisible to it; the peer connection is driven
// directly by the test. Both clones sit on the seed commit — the test
// diverges each side afterwards.
type syncPeerFixture struct {
	store    *DoltStore
	db       *sql.DB // the local store's pool
	peerConn *sql.DB // single-connection pool on the sibling peer database
}

// setupSyncPeerClone builds the syncPeerFixture for issueID.
func setupSyncPeerClone(t *testing.T, issueID string) *syncPeerFixture {
	t.Helper()
	store, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)
	db := store.db

	exec := func(stage string, q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}
	exec("seed issue",
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES (?, 'base', '', '', '', '', 'open', 2, 'task')", issueID)
	exec("commit seed", "CALL DOLT_COMMIT('-Am', 'seed issue')")

	peerDB := uniqueTestDBName(t)
	remoteURL := "file:///tmp/" + peerDB + "_remote"
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{Name: "peer", RemoteURL: remoteURL}); err != nil {
		t.Fatalf("add federation peer: %v", err)
	}
	if err := store.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("push to peer remote: %v", err)
	}
	exec("clone peer database", "CALL DOLT_CLONE(?, ?)", remoteURL, peerDB)

	peerConn, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: peerDB,
	}.String())
	if err != nil {
		t.Fatalf("open peer connection: %v", err)
	}
	t.Cleanup(func() { _ = peerConn.Close() })
	peerConn.SetMaxOpenConns(1)

	return &syncPeerFixture{store: store, db: db, peerConn: peerConn}
}

// peerExec runs one statement on the peer clone under its own context budget.
func (f *syncPeerFixture) peerExec(t *testing.T, stage, q string) {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	if _, err := f.peerConn.ExecContext(ctx, q); err != nil {
		t.Fatalf("peer %s: %v", stage, err)
	}
}

// peerScalar scans a single value off the peer clone under its own context
// budget.
func peerScalar[T any](t *testing.T, f *syncPeerFixture, q string, dest *T) {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	if err := f.peerConn.QueryRowContext(ctx, q).Scan(dest); err != nil {
		t.Fatalf("peer query %q: %v", q, err)
	}
}

// divergeRetitles commits a conflicting retitle on both clones: 'theirs' on
// the peer (pushed), 'ours' locally. updated_at is pinned to the same value on
// both sides so the LWW auto-resolver would decline the conflict — only the
// explicit strategy can resolve it (TestPullFromReportsOperatorConflicts'
// conflict shape).
func (f *syncPeerFixture) divergeRetitles(t *testing.T, issueID string) {
	t.Helper()
	f.peerExec(t, "peer retitle",
		fmt.Sprintf("UPDATE issues SET title = 'theirs', updated_at = '2026-01-01 00:00:00' WHERE id = '%s'", issueID))
	f.peerExec(t, "peer commit", "CALL DOLT_COMMIT('-Am', 'peer retitles')")
	f.peerExec(t, "peer push", "CALL DOLT_PUSH('origin', 'main')")

	ctx, cancel := testContext(t)
	defer cancel()
	if _, err := f.db.ExecContext(ctx,
		"UPDATE issues SET title = 'ours', updated_at = '2026-01-01 00:00:00' WHERE id = ?", issueID); err != nil {
		t.Fatalf("retitle locally: %v", err)
	}
	if _, err := f.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local retitles')"); err != nil {
		t.Fatalf("commit local retitle: %v", err)
	}
}

// titleOf reads an issue's title from the local working set.
func titleOf(t *testing.T, db *sql.DB, ctx context.Context, issueID string) string {
	t.Helper()
	var title string
	if err := db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", issueID).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	return title
}

// localHashOf evaluates HASHOF(expr) on the local store.
func localHashOf(t *testing.T, db *sql.DB, ctx context.Context, expr string) string {
	t.Helper()
	var h string
	if err := db.QueryRowContext(ctx, "SELECT HASHOF(?)", expr).Scan(&h); err != nil {
		t.Fatalf("HASHOF(%s): %v", expr, err)
	}
	return h
}

// assertSyncConcluded fails when a live conflict or an open merge survives a
// sync: a concluded strategy merge leaves neither.
func assertSyncConcluded(t *testing.T, db *sql.DB, ctx context.Context) {
	t.Helper()
	var conflicts int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&conflicts); err != nil {
		t.Fatalf("count dolt_conflicts: %v", err)
	}
	if conflicts != 0 {
		t.Errorf("%d conflicted table(s) remain after sync", conflicts)
	}
	var merging int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_merge_status WHERE is_merging").Scan(&merging); err != nil {
		t.Fatalf("read dolt_merge_status: %v", err)
	}
	if merging != 0 {
		t.Error("a merge is still open after sync; the next sync would wedge")
	}
}

// TestFederationSyncStrategyResolvesRealConflict reproduces bd-sesknw
// (GH#5564): `bd federation sync --strategy theirs` must resolve a real
// conflict with a peer, the way `bd vc merge --strategy` does since #4992.
// Sync still merges through the bare-autocommit DoltStore.Merge, so Dolt
// rejects the conflicted merge (Error 1105) before the strategy is ever
// applied. Peer setup mirrors TestPullFromReportsOperatorConflicts; both
// sides pin updated_at so the LWW auto-resolver declines the conflict and
// only the strategy can resolve it.
func TestFederationSyncStrategyResolvesRealConflict(t *testing.T) {
	const issueID = "fsspeer-x"
	f := setupSyncPeerClone(t, issueID)
	f.divergeRetitles(t, issueID)

	ctx, cancel := testContext(t)
	defer cancel()

	result, err := f.store.Sync(ctx, "peer", "theirs")
	if err != nil {
		t.Fatalf("Sync(--strategy theirs) should resolve the conflict, got: %v", err)
	}
	if !result.Merged || !result.ConflictsResolved || !result.Pushed {
		t.Errorf("Sync result = merged:%v conflictsResolved:%v pushed:%v; want all true",
			result.Merged, result.ConflictsResolved, result.Pushed)
	}
	foundIssues := false
	for _, c := range result.Conflicts {
		if c.Field == "issues" {
			foundIssues = true
		}
	}
	if !foundIssues {
		t.Errorf("reported conflicts %+v do not name the issues table", result.Conflicts)
	}

	if got := titleOf(t, f.db, ctx, issueID); got != "theirs" {
		t.Errorf("title = %q after Sync --strategy theirs, want %q", got, "theirs")
	}
	assertSyncConcluded(t, f.db, ctx)

	// The peer remote must hold the local merge result the sync pushed.
	f.peerExec(t, "peer fetch", "CALL DOLT_FETCH('origin')")
	var remoteTitle string
	peerScalar(t, f, "SELECT title FROM issues AS OF 'remotes/origin/main' WHERE id = '"+issueID+"'", &remoteTitle)
	if remoteTitle != "theirs" {
		t.Errorf("peer remote title = %q after sync push, want %q", remoteTitle, "theirs")
	}
}

// TestFederationSyncStrategyOursKeepsLocalAndConcludesMerge is the
// --strategy ours counterpart (GH#5564 AC-2): the resolution keeps the local
// value and stages nothing new, yet the merge must still conclude with both
// pre-merge heads in its ancestry — the merge-closing commit concludeMerge
// runs while the merge is open (the GH#7811 --ours shape). Ancestry is
// asserted through dolt_log because the post-merge is_blocked recompute may
// add a commit on top of the merge commit.
func TestFederationSyncStrategyOursKeepsLocalAndConcludesMerge(t *testing.T) {
	const issueID = "fsspeer-ours"
	f := setupSyncPeerClone(t, issueID)
	f.divergeRetitles(t, issueID)

	ctx, cancel := testContext(t)
	defer cancel()
	ourHead := localHashOf(t, f.db, ctx, "HEAD")
	var theirHead string
	peerScalar(t, f, "SELECT HASHOF('HEAD')", &theirHead)

	result, err := f.store.Sync(ctx, "peer", "ours")
	if err != nil {
		t.Fatalf("Sync(--strategy ours) should resolve the conflict, got: %v", err)
	}
	if !result.Merged || !result.ConflictsResolved {
		t.Errorf("Sync result = merged:%v conflictsResolved:%v; want both true",
			result.Merged, result.ConflictsResolved)
	}
	if !result.Pushed {
		t.Errorf("Pushed = false after a resolved --ours sync (push error: %v)", result.PushError)
	}

	if got := titleOf(t, f.db, ctx, issueID); got != "ours" {
		t.Errorf("title = %q after Sync --strategy ours, want %q", got, "ours")
	}
	assertSyncConcluded(t, f.db, ctx)

	head := localHashOf(t, f.db, ctx, "HEAD")
	var ancestors int
	// ourHead/theirHead/head come from HASHOF, so they are valid Dolt commit
	// literals; dolt_log AS OF requires literals.
	//nolint:gosec // G201: hashes read from HASHOF in this test.
	if err := f.db.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT COUNT(*) FROM dolt_log AS OF '%s' WHERE commit_hash IN ('%s', '%s')",
		head, ourHead, theirHead)).Scan(&ancestors); err != nil {
		t.Fatalf("count merge ancestry: %v", err)
	}
	if ancestors != 2 {
		t.Errorf("HEAD's history contains %d of the 2 pre-merge heads; the merge was not concluded as a two-parent commit", ancestors)
	}
}
