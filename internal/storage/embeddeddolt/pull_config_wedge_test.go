//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// configCommittedClean asserts the config table has no uncommitted changes —
// i.e. the preceding commit actually staged config. This is the embedded-engine
// check for the pull config wedge: persistent memories live in config as
// kv.memory.* rows, and a commit that fails to stage config leaves the working
// set permanently dirty so DOLT_MERGE refuses to start.
func configCommittedClean(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'config'").Scan(&n); err != nil {
		t.Fatalf("query dolt_status: %v", err)
	}
	if n > 0 {
		t.Fatalf("config still dirty after commit: the embedded commit path does not stage config, so pulls will wedge")
	}
}

// TestEmbeddedConfigCommitStagesConfig documents the embedded engine's commit
// behavior for config: the embedded store commits via DOLT_COMMIT('-Am') (see
// EmbeddedDoltStore.Commit), and the pre-pull auto-commit relies on that to
// stage memory rows. If this fails, the embedded pre-pull path needs the same
// explicit-config-staging fix the server-mode path got.
func TestEmbeddedConfigCommitStagesConfig(t *testing.T) {
	te := newTestEnv(t, "cfgstage")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	if _, err := conn.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES ('kv.memory.stage-check', 'v1')"); err != nil {
		t.Fatalf("insert config memory row: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'commit memory')"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	configCommittedClean(t, ctx, conn)
}

// TestEmbeddedMergeAndSettleMemoryConfigConflict is the embedded-engine
// counterpart of TestPullAutoResolveMemoryConfigConflicts: a merge conflict
// limited to kv.memory.* config rows is auto-resolved with "theirs", so two
// clones that edited the same memory converge instead of wedging the pull.
func TestEmbeddedMergeAndSettleMemoryConfigConflict(t *testing.T) {
	te := newTestEnv(t, "cfgsettle")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	if _, err := conn.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES ('kv.memory.shared', 'ours')"); err != nil {
		t.Fatalf("insert config on main: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local memory')"); err != nil {
		t.Fatalf("commit on main: %v", err)
	}
	// Guard the setup: if -Am did not stage config, no conflict would form and
	// the test would pass vacuously.
	configCommittedClean(t, ctx, conn)

	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH('cfgpeer', 'HEAD~1')"); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('cfgpeer')"); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES ('kv.memory.shared', 'theirs')"); err != nil {
		t.Fatalf("insert config on peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'peer memory')"); err != nil {
		t.Fatalf("commit on peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	if err := versioncontrolops.MergeAndSettle(ctx, conn, "cfgpeer"); err != nil {
		t.Fatalf("settling memory-config-conflicted merge: %v", err)
	}

	var value string
	if err := conn.QueryRowContext(ctx,
		"SELECT value FROM config WHERE `key` = 'kv.memory.shared'").Scan(&value); err != nil {
		t.Fatalf("read resolved config: %v", err)
	}
	if value != "theirs" {
		t.Errorf("resolved memory value = %q, want \"theirs\" (--theirs convergent)", value)
	}
}

// TestEmbeddedMergeAndSettleSkipsNonMemoryConfigConflict verifies the prefix
// boundary: a config key under kv. but NOT kv.memory. is a real semantic
// conflict, so the resolver declines the whole config table and the merge is
// surfaced to the operator as a MergeConflictsError naming config.
func TestEmbeddedMergeAndSettleSkipsNonMemoryConfigConflict(t *testing.T) {
	te := newTestEnv(t, "cfgskip")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	if _, err := conn.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES ('kv.custom.setting', 'ours')"); err != nil {
		t.Fatalf("insert config on main: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local config')"); err != nil {
		t.Fatalf("commit on main: %v", err)
	}
	configCommittedClean(t, ctx, conn)

	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH('cfgskippeer', 'HEAD~1')"); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('cfgskippeer')"); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES ('kv.custom.setting', 'theirs')"); err != nil {
		t.Fatalf("insert config on peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'peer config')"); err != nil {
		t.Fatalf("commit on peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	err := versioncontrolops.MergeAndSettle(ctx, conn, "cfgskippeer")
	var mce *versioncontrolops.MergeConflictsError
	if !errors.As(err, &mce) {
		t.Fatalf("want MergeConflictsError for non-memory config conflict, got: %v", err)
	}
	foundConfig := false
	for _, c := range mce.Conflicts {
		if c.Field == "config" {
			foundConfig = true
		}
	}
	if !foundConfig {
		t.Errorf("captured conflicts %+v do not name the config table", mce.Conflicts)
	}
	// The local value must survive the aborted merge.
	var value string
	if err := conn.QueryRowContext(ctx,
		"SELECT value FROM config WHERE `key` = 'kv.custom.setting'").Scan(&value); err != nil {
		t.Fatalf("read config after abort: %v", err)
	}
	if value != "ours" {
		t.Errorf("local config value = %q after aborted merge, want %q", value, "ours")
	}
}

// statusHasConfig reports whether the config table has staged or unstaged
// working-set changes, observed through the store's own connection — the same
// session Sync commits on. A separate OpenSQL handle is avoided on purpose:
// embedded uncommitted working-set writes are not reliably visible across
// handles until shutdown-flush, so the dirty precondition must be read here.
func statusHasConfig(t *testing.T, ctx context.Context, te *testEnv) bool {
	t.Helper()
	st, err := te.store.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, e := range st.Staged {
		if e.Table == "config" {
			return true
		}
	}
	for _, e := range st.Unstaged {
		if e.Table == "config" {
			return true
		}
	}
	return false
}

// TestEmbeddedSyncCommitsConfigBeforeMerge is the embedded-engine counterpart of
// server-mode TestFederationSyncCommitsConfigBeforeFetch: EmbeddedDoltStore.Sync
// must auto-commit pending changes before its merge, exactly as embedded
// Pull/PullRemote/PullFrom already do. Persistent memories live in config as
// kv.memory.* rows that `bd remember` leaves uncommitted; without the pre-merge
// CommitPending, a dirty config working set survives into DOLT_MERGE and wedges
// `bd federation sync` ("cannot merge with uncommitted changes"). The peer remote
// does not exist, so Sync fails at fetch — but only after the pre-merge commit has
// already staged config, which is exactly what this test pins.
func TestEmbeddedSyncCommitsConfigBeforeMerge(t *testing.T) {
	te := newTestEnv(t, "syncwedge")
	ctx := t.Context()

	// Register a peer whose file remote does not exist: Sync runs its pre-merge
	// auto-commit, then fails at fetch — far enough to prove config was staged.
	peer := &storage.FederationPeer{
		Name:        "peer-sync-wedge",
		RemoteURL:   "file:///tmp/beads-no-such-embedded-federation-peer",
		Sovereignty: "T2",
	}
	if err := te.store.AddFederationPeer(ctx, peer); err != nil {
		t.Fatalf("add federation peer: %v", err)
	}
	// Commit the peer registration so the only dirty table going into Sync is the
	// config row seeded below — the precise `bd remember` wedge precondition.
	if err := te.store.Commit(ctx, "register sync peer"); err != nil {
		t.Fatalf("commit peer registration: %v", err)
	}

	// Simulate `bd remember`: a kv.memory.* config write, left uncommitted.
	if err := te.store.SetConfig(ctx, "kv.memory.sync-wedge", "v1"); err != nil {
		t.Fatalf("SetConfig memory row: %v", err)
	}
	if !statusHasConfig(t, ctx, te) {
		t.Fatal("memory write did not dirty config; cannot reproduce the wedge precondition")
	}

	if _, err := te.store.Sync(ctx, peer.Name, ""); err == nil {
		t.Fatal("expected sync to fail for nonexistent file remote")
	}

	if statusHasConfig(t, ctx, te) {
		t.Fatal("embedded Sync left config dirty before merge; bd federation sync would wedge DOLT_MERGE (GH#2474)")
	}
}

// TestSyncWithStrategy_TheirsResolvesPeerConflict is the embedded-engine
// counterpart of server-mode TestFederationSyncStrategyResolvesRealConflict
// (GH#5564, bd-sesknw): EmbeddedDoltStore.Sync had the same dead --strategy
// branch — it merged through the bare Merge, whose CALL DOLT_MERGE runs
// under autocommit and is rejected on a real conflict before the strategy
// could apply. The peer is a real file:// remote cloned into a second
// embedded database (remote_git_ref_test.go's clone pattern), so the test
// exercises the fetch/merge/push loop Sync actually runs. Both sides pin
// updated_at so the LWW auto-resolver would decline the conflict and only
// the strategy can resolve it.
func TestSyncWithStrategy_TheirsResolvesPeerConflict(t *testing.T) {
	te := newTestEnv(t, "syncstrat")
	ctx := t.Context()

	// Seed one issue and commit it. All local writes go through te.exec's
	// short-lived handles: a second long-lived engine handle alongside the
	// store's own (openSettleConn) makes the store's next OpenSQL wait on the
	// engine lock forever, so each statement opens, runs, and closes.
	te.exec(t, ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('mssync-1', 'base', '', '', '', '', 'open', 2, 'task')")
	te.exec(t, ctx, "CALL DOLT_COMMIT('-Am', 'seed issue')")

	// Register the peer on a file remote and commit the registration so the
	// working set Sync merges on is clean.
	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := te.store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name:        "peer",
		RemoteURL:   "file://" + remoteDir,
		Sovereignty: "T2",
	}); err != nil {
		t.Fatalf("add federation peer: %v", err)
	}
	if err := te.store.Commit(ctx, "register sync peer"); err != nil {
		t.Fatalf("commit peer registration: %v", err)
	}
	if err := te.store.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("push to peer remote: %v", err)
	}

	// Clone the remote into a second embedded database, retitle there, and
	// push back (clone-created remotes carry branch tracking).
	cloneDir := t.TempDir()
	boot, bootCleanup, err := embeddeddolt.OpenSQL(ctx, cloneDir, "", "")
	if err != nil {
		t.Fatalf("OpenSQL for clone: %v", err)
	}
	if err := versioncontrolops.DoltClone(ctx, boot, "file://"+remoteDir, "peerdb", ""); err != nil {
		_ = bootCleanup()
		t.Fatalf("clone peer database: %v", err)
	}
	_ = bootCleanup()
	peerDB, peerCleanup, err := embeddeddolt.OpenSQL(ctx, cloneDir, "peerdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL on clone: %v", err)
	}
	defer func() { _ = peerCleanup() }()
	for _, q := range []string{
		"UPDATE issues SET title = 'theirs', updated_at = '2026-01-01 00:00:00' WHERE id = 'mssync-1'",
		"CALL DOLT_COMMIT('-Am', 'peer retitles')",
		"CALL DOLT_PUSH('origin', 'main')",
	} {
		if _, err := peerDB.ExecContext(ctx, q); err != nil {
			t.Fatalf("peer %q: %v", q, err)
		}
	}

	// Local: conflicting retitle, committed.
	te.exec(t, ctx,
		"UPDATE issues SET title = 'ours', updated_at = '2026-01-01 00:00:00' WHERE id = 'mssync-1'")
	te.exec(t, ctx, "CALL DOLT_COMMIT('-Am', 'local retitles')")

	result, err := te.store.Sync(ctx, "peer", "theirs")
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

	// The peer's title is committed and nothing is left wedged. The reads go
	// through fresh handles (te.queryScalar); the merge is committed, so the
	// state is visible across handles.
	var title string
	te.queryScalar(t, ctx, "SELECT title FROM issues WHERE id = 'mssync-1'", nil, &title)
	if title != "theirs" {
		t.Errorf("title = %q after Sync --strategy theirs, want %q", title, "theirs")
	}
	var liveConflicts int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_conflicts", nil, &liveConflicts)
	if liveConflicts != 0 {
		t.Errorf("%d conflicted table(s) remain after strategy resolution", liveConflicts)
	}
	var merging int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_merge_status WHERE is_merging", nil, &merging)
	if merging != 0 {
		t.Error("a merge is still open after sync; the next sync would wedge")
	}
}
