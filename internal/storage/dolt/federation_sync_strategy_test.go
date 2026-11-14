package dolt

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// These tests cover bd-sesknw (GH#5564): `bd federation sync --strategy`
// merged the peer through the bare-autocommit DoltStore.Merge, so Dolt
// rejected any real conflict (Error 1105) before the strategy could apply —
// the shape #4992 fixed for `bd vc merge --strategy`. Sync now routes an
// explicit strategy through MergeWithStrategy.
//
// The peer lives inside the test Dolt server, exactly as in
// TestPullFromSettlesFKCascadeViolations: the store pushes to a server-local
// file:// remote, a server-side DOLT_CLONE turns that into a sibling peer
// database, and the peer pushes its divergent commit back before Sync runs.

// fedSyncPeer is a server store plus a federated peer clone of its history.
type fedSyncPeer struct {
	store *DoltStore
	db    *sql.DB
	peer  *sql.DB
}

// newFedSyncPeer seeds the store with seed (then commits it), registers the
// peer remote "peer", pushes, and clones the peer database from that remote.
func newFedSyncPeer(t *testing.T, ctx context.Context, seed ...string) *fedSyncPeer {
	t.Helper()
	store, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup)
	db := store.db

	fedExec(t, ctx, db, "seed", append(seed, "CALL DOLT_COMMIT('-Am', 'seed')")...)

	peerDB := uniqueTestDBName(t)
	remoteURL := "file:///tmp/" + peerDB + "_remote"
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{Name: "peer", RemoteURL: remoteURL}); err != nil {
		t.Fatalf("add federation peer: %v", err)
	}
	if err := store.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("push to peer remote: %v", err)
	}
	// Commit a stale peer last_sync so the stamp Sync's own Fetch writes always
	// dirties federation_peers before the merge (otherwise only when the clock
	// crosses a second): Dolt refuses to merge over that dirty table.
	fedExec(t, ctx, db, "local",
		"UPDATE federation_peers SET last_sync = '2000-01-01 00:00:00' WHERE name = 'peer'",
		"CALL DOLT_COMMIT('-Am', 'pin stale peer last_sync')",
	)
	if _, err := db.ExecContext(ctx, "CALL DOLT_CLONE(?, ?)", remoteURL, peerDB); err != nil {
		t.Fatalf("clone peer database: %v", err)
	}

	peerConn, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: peerDB,
	}.String())
	if err != nil {
		t.Fatalf("open peer connection: %v", err)
	}
	t.Cleanup(func() { _ = peerConn.Close() })
	peerConn.SetMaxOpenConns(1)
	return &fedSyncPeer{store: store, db: db, peer: peerConn}
}

func fedExec(t *testing.T, ctx context.Context, db *sql.DB, who string, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s %q: %v", who, q, err)
		}
	}
}

func fedScalar(t *testing.T, ctx context.Context, db *sql.DB, dest any, query string, args ...any) {
	t.Helper()
	if err := db.QueryRowContext(ctx, query, args...).Scan(dest); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
}

func fedHead(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var h string
	fedScalar(t, ctx, db, &h, "SELECT DOLT_HASHOF('HEAD')")
	return h
}

// retitleConflict commits a peer retitle ("theirs", pushed) and a local
// retitle ("ours") of the same issue. Both pin the same updated_at so the
// LWW auto-resolver declines the conflict and only a strategy resolves it.
// It returns the local and peer pre-merge heads.
func (f *fedSyncPeer) retitleConflict(t *testing.T, ctx context.Context, issueID string) (localHead, peerHead string) {
	t.Helper()
	fedExec(t, ctx, f.peer, "peer",
		"UPDATE issues SET title = 'theirs', updated_at = '2026-01-01 00:00:00' WHERE id = '"+issueID+"'",
		"CALL DOLT_COMMIT('-Am', 'peer retitles')",
		"CALL DOLT_PUSH('origin', 'main')",
	)
	peerHead = fedHead(t, ctx, f.peer)
	fedExec(t, ctx, f.db, "local",
		"UPDATE issues SET title = 'ours', updated_at = '2026-01-01 00:00:00' WHERE id = '"+issueID+"'",
		"CALL DOLT_COMMIT('-Am', 'local retitles')",
	)
	return fedHead(t, ctx, f.db), peerHead
}

func seedIssue(id string) string {
	return "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) " +
		"VALUES ('" + id + "', 'base', '', '', '', '', 'open', 2, 'task')"
}

func fedTitle(t *testing.T, ctx context.Context, db *sql.DB, issueID string) string {
	t.Helper()
	var title string
	fedScalar(t, ctx, db, &title, "SELECT title FROM issues AS OF 'HEAD' WHERE id = ?", issueID)
	return title
}

// assertMergeSettled checks no unresolved conflict, constraint violation or
// open merge survives on the store.
func assertMergeSettled(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var n int
	fedScalar(t, ctx, db, &n, "SELECT COUNT(*) FROM dolt_conflicts")
	if n != 0 {
		t.Errorf("%d conflicted table(s) remain", n)
	}
	fedScalar(t, ctx, db, &n, "SELECT COALESCE(SUM(num_violations), 0) FROM dolt_constraint_violations")
	if n != 0 {
		t.Errorf("%d constraint violation(s) remain", n)
	}
	var merging bool
	fedScalar(t, ctx, db, &merging, "SELECT is_merging FROM dolt_merge_status")
	if merging {
		t.Errorf("merge is still open (dolt_merge_status.is_merging)")
	}
	// Sync's advisory timestamps — metadata last_sync_<peer> and the
	// federation_peers.last_sync stamp a successful push writes — stay
	// uncommitted by design; any other dirty table is merge output the
	// conclusion failed to commit.
	rows, err := db.QueryContext(ctx,
		"SELECT table_name FROM dolt_status WHERE table_name NOT IN ('metadata', 'federation_peers')")
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan dolt_status: %v", err)
		}
		t.Errorf("table %s left uncommitted after Sync", table)
	}
}

// assertPeerHasLocalHead checks the peer's remote now carries the local HEAD.
func (f *fedSyncPeer) assertPeerHasLocalHead(t *testing.T, ctx context.Context) {
	t.Helper()
	head := fedHead(t, ctx, f.db)
	fedExec(t, ctx, f.peer, "peer", "CALL DOLT_FETCH('origin')")
	var n int
	fedScalar(t, ctx, f.peer, &n, "SELECT COUNT(*) FROM dolt_log AS OF 'origin/main' WHERE commit_hash = ?", head)
	if n != 1 {
		t.Errorf("peer remote does not contain the local merge result %s", head)
	}
}

func hasConflictOn(conflicts []storage.Conflict, table string) bool {
	for _, c := range conflicts {
		if c.Field == table {
			return true
		}
	}
	return false
}

// TestFederationSyncStrategyResolvesRealConflict is the triage repro for
// bd-sesknw (AC-1): `bd federation sync --strategy theirs` resolves a real
// issues-table conflict with the peer, commits the peer's title, concludes
// the merge and pushes the result back.
func TestFederationSyncStrategyResolvesRealConflict(t *testing.T) {
	ctx, cancel := testContext(t)
	defer cancel()
	f := newFedSyncPeer(t, ctx, seedIssue("fsspeer-x"))
	f.retitleConflict(t, ctx, "fsspeer-x")

	result, err := f.store.Sync(ctx, "peer", "theirs")
	if err != nil {
		t.Fatalf("Sync(--strategy theirs) should resolve the conflict, got: %v", err)
	}
	if !result.Merged || !result.ConflictsResolved || !result.Pushed {
		t.Errorf("result Merged=%v ConflictsResolved=%v Pushed=%v (PushError=%v), want all true",
			result.Merged, result.ConflictsResolved, result.Pushed, result.PushError)
	}
	if !hasConflictOn(result.Conflicts, "issues") {
		t.Errorf("reported conflicts %+v do not name the issues table", result.Conflicts)
	}
	if title := fedTitle(t, ctx, f.db, "fsspeer-x"); title != "theirs" {
		t.Errorf("committed title = %q after Sync --strategy theirs, want %q", title, "theirs")
	}
	assertMergeSettled(t, ctx, f.db)
	f.assertPeerHasLocalHead(t, ctx)
}

// TestFederationSyncStrategyOursConcludesMerge covers AC-2: --strategy ours
// keeps the local title and still concludes a real two-parent merge — even
// though resolving every conflict back to ours stages no new issue content —
// so the peer's head is in local history and the next sync is not wedged.
func TestFederationSyncStrategyOursConcludesMerge(t *testing.T) {
	ctx, cancel := testContext(t)
	defer cancel()
	f := newFedSyncPeer(t, ctx, seedIssue("fssours-x"))
	localHead, peerHead := f.retitleConflict(t, ctx, "fssours-x")

	result, err := f.store.Sync(ctx, "peer", "ours")
	if err != nil {
		t.Fatalf("Sync(--strategy ours) should resolve the conflict, got: %v", err)
	}
	if !result.Merged || !result.ConflictsResolved {
		t.Errorf("result Merged=%v ConflictsResolved=%v, want both true", result.Merged, result.ConflictsResolved)
	}
	if !hasConflictOn(result.Conflicts, "issues") {
		t.Errorf("reported conflicts %+v do not name the issues table", result.Conflicts)
	}
	if title := fedTitle(t, ctx, f.db, "fssours-x"); title != "ours" {
		t.Errorf("committed title = %q after Sync --strategy ours, want %q", title, "ours")
	}
	for name, h := range map[string]string{"local": localHead, "peer": peerHead} {
		var n int
		fedScalar(t, ctx, f.db, &n, "SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?", h)
		if n != 1 {
			t.Errorf("%s pre-merge head %s is not an ancestor of HEAD: the merge was not concluded", name, h)
		}
	}
	assertMergeSettled(t, ctx, f.db)
}

// TestFederationSyncNoStrategyConflictFails covers AC-5: without a strategy a
// genuine conflict fails the sync with the --strategy hint, merges and pushes
// nothing, and leaves no conflict or open merge behind.
func TestFederationSyncNoStrategyConflictFails(t *testing.T) {
	ctx, cancel := testContext(t)
	defer cancel()
	f := newFedSyncPeer(t, ctx, seedIssue("fssnone-x"))
	_, peerHead := f.retitleConflict(t, ctx, "fssnone-x")

	result, err := f.store.Sync(ctx, "peer", "")
	if err == nil {
		t.Fatal("Sync without a strategy succeeded over a real conflict")
	}
	if !strings.Contains(err.Error(), "--strategy") {
		t.Errorf("error = %v, want it to name the --strategy escape hatch", err)
	}
	if result == nil || result.Merged || result.Pushed {
		t.Errorf("result = %+v, want Merged and Pushed false", result)
	}
	if title := fedTitle(t, ctx, f.db, "fssnone-x"); title != "ours" {
		t.Errorf("committed title = %q, want the local %q", title, "ours")
	}
	var merged int
	fedScalar(t, ctx, f.db, &merged, "SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?", peerHead)
	if merged != 0 {
		t.Errorf("peer head %s was merged into HEAD by a failed sync", peerHead)
	}
	assertMergeSettled(t, ctx, f.db)
}
