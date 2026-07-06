package dolt

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// configDirty reports whether the config table has uncommitted changes in the
// working set — the exact condition that makes DOLT_MERGE refuse to start.
func configDirty(t *testing.T, ctx context.Context, db *sql.DB) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'config'").Scan(&n); err != nil {
		t.Fatalf("query dolt_status: %v", err)
	}
	return n > 0
}

// TestCommitBeforePullIncludesConfig is the regression test for the pull config
// wedge: persistent memories live in config as kv.memory.* rows, plain Commit()
// excludes config (GH#2455), so they sit permanently uncommitted and the
// pre-pull "clean the working set" step leaves config dirty — DOLT_MERGE then
// refuses to start ("cannot merge with uncommitted changes"). commitBeforePull
// must stage config explicitly and leave the working set clean.
func TestCommitBeforePullIncludesConfig(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	// Simulate `bd remember`: a kv.memory.* row in the synced config table.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES ('kv.memory.test-wedge', 'v1')"); err != nil {
		t.Fatalf("insert config memory row: %v", err)
	}

	// Plain Commit() leaves config dirty — the wedge precondition. (With only
	// config dirty it commits nothing and returns nil.)
	if err := store.Commit(ctx, "commit excluding config"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !configDirty(t, ctx, db) {
		t.Fatalf("Commit() unexpectedly committed config; the wedge precondition no longer reproduces (did GH#2455's config exclusion change?)")
	}

	// commitBeforePull must stage config and leave the working set clean so the
	// subsequent merge can start.
	if err := store.commitBeforePull(ctx, "auto-commit before pull"); err != nil {
		t.Fatalf("commitBeforePull: %v", err)
	}
	if configDirty(t, ctx, db) {
		t.Fatalf("commitBeforePull left config dirty; DOLT_MERGE would still refuse to start")
	}
}

// TestFederationSyncCommitsConfigBeforeFetch is the `bd federation sync`
// analogue of the pull config wedge: Sync auto-commits the working set before
// its merge, and that pre-merge commit must include config. Plain Commit
// (GH#2455) excludes config, so a dirty kv.memory.* row used to survive Sync's
// pre-merge commit and wedge DOLT_MERGE — the same failure this PR fixes for the
// pull paths. The pre-merge commit must leave config clean even though Sync then
// fails at fetch against a nonexistent remote.
func TestFederationSyncCommitsConfigBeforeFetch(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	// Register a peer whose remote does not exist: Sync runs its pre-merge
	// auto-commit, then fails at fetch — far enough to prove config was staged.
	peer := &storage.FederationPeer{
		Name:        "peer-config-wedge",
		RemoteURL:   "file:///tmp/beads-no-such-federation-peer",
		Sovereignty: "T2",
	}
	if err := store.AddFederationPeer(ctx, peer); err != nil {
		t.Fatalf("add federation peer: %v", err)
	}

	// Simulate `bd remember`: a kv.memory.* row dirties the synced config table.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES ('kv.memory.sync-wedge', 'v1')"); err != nil {
		t.Fatalf("insert config memory row: %v", err)
	}
	if !configDirty(t, ctx, db) {
		t.Fatalf("inserted memory row did not dirty config; cannot reproduce the wedge precondition")
	}

	if _, err := store.Sync(ctx, peer.Name, ""); err == nil {
		t.Fatal("expected sync to fail for nonexistent file remote")
	}
	if configDirty(t, ctx, db) {
		t.Fatal("Sync left config dirty before merge; bd federation sync would wedge DOLT_MERGE (GH#2474)")
	}
}

// TestPullAutoResolveMemoryConfigConflicts verifies that a merge conflict
// limited to kv.memory.* config rows is auto-resolved with "theirs" — the same
// machine-convergent policy used for metadata. Without this, making config a
// synced table (so memories round-trip) would turn same-memory edits across
// clones into an operator-visible pull wedge.
func TestPullAutoResolveMemoryConfigConflicts(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	// Stage config explicitly: -Am was observed not to stage config reliably
	// under the server-mode stored-procedure path, so the test must not depend
	// on it to set up the conflict.
	commitConfig := func(msg string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('config')"); err != nil {
			t.Fatalf("dolt add config: %v", err)
		}
		if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?)", msg); err != nil {
			t.Fatalf("dolt commit: %v", err)
		}
	}

	value := resolveConfigConflict(t, ctx, store, "kv.memory.shared", "ours", "theirs", commitConfig)
	if value != "theirs" {
		t.Errorf("resolved memory value = %q, want \"theirs\" (--theirs convergent)", value)
	}
}

// TestPullAutoResolveSkipsNonMemoryConfigConflicts verifies the prefix boundary:
// a config key under kv. but NOT kv.memory. (e.g. a user kv setting, or
// issue_prefix) is a real semantic conflict and must be left for the operator,
// so the whole config table is declined.
func TestPullAutoResolveSkipsNonMemoryConfigConflicts(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	commitConfig := func(msg string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('config')"); err != nil {
			t.Fatalf("dolt add config: %v", err)
		}
		if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?)", msg); err != nil {
			t.Fatalf("dolt commit: %v", err)
		}
	}

	resolved := tryResolveConfigConflict(t, ctx, store, "kv.custom.setting", "ours", "theirs", commitConfig)
	if resolved {
		t.Fatalf("non-memory config conflict was auto-resolved; only kv.memory.* keys are safe")
	}
}

// resolveConfigConflict sets up a same-key config conflict on a divergent
// branch, merges it, runs the auto-resolver, and (asserting it resolved)
// commits and returns the resolved value. commitFn stages+commits config.
func resolveConfigConflict(t *testing.T, ctx context.Context, store *DoltStore, key, ours, theirs string, commitFn func(string)) string {
	t.Helper()
	resolved, db := runConfigConflictMerge(t, ctx, store, key, ours, theirs, commitFn, true)
	var value string
	if err := db.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value); err != nil {
		t.Fatalf("read resolved config value: %v", err)
	}
	if !resolved {
		t.Fatalf("config conflict on %q was not auto-resolved", key)
	}
	return value
}

// tryResolveConfigConflict is resolveConfigConflict's negative-path sibling: it
// returns whether the resolver accepted the conflict without requiring it to.
func tryResolveConfigConflict(t *testing.T, ctx context.Context, store *DoltStore, key, ours, theirs string, commitFn func(string)) bool {
	t.Helper()
	resolved, _ := runConfigConflictMerge(t, ctx, store, key, ours, theirs, commitFn, false)
	return resolved
}

// runConfigConflictMerge builds a same-key config conflict (ours on the current
// branch, theirs on a divergent "remote" branch), merges remote into current
// with conflict-tolerant session flags, and runs tryAutoResolveMergeConflicts.
// When commitOnResolve and the resolver succeeds, the merge tx is committed so
// callers can read the settled value; otherwise the tx is rolled back.
func runConfigConflictMerge(t *testing.T, ctx context.Context, store *DoltStore, key, ours, theirs string, commitFn func(string), commitOnResolve bool) (bool, *sql.DB) {
	t.Helper()
	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("active_branch: %v", err)
	}

	// Our value on the current branch.
	if _, err := db.ExecContext(ctx, "INSERT INTO config (`key`, value) VALUES (?, ?)", key, ours); err != nil {
		t.Fatalf("insert local config: %v", err)
	}
	commitFn("local config")

	// Their value on a branch forked from the common ancestor (HEAD~1).
	remoteBranch := currentBranch + "_cfgremote"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD~1')", remoteBranch); err != nil {
		t.Fatalf("create remote branch: %v", err)
	}
	defer func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", remoteBranch)
	}()
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", remoteBranch); err != nil {
		t.Fatalf("checkout remote branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO config (`key`, value) VALUES (?, ?)", key, theirs); err != nil {
		t.Fatalf("insert remote config: %v", err)
	}
	commitFn("remote config")
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current branch: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("set dolt_allow_commit_conflicts: %v", err)
	}
	_, mergeErr := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", remoteBranch)

	// Prove the merge actually produced a config conflict before the resolver
	// runs. tryAutoResolveMergeConflicts returns (false, nil) both when it
	// DECLINES a conflict and when there is NO conflict at all, so without this
	// the negative test (TestPullAutoResolveSkipsNonMemoryConfigConflicts) would
	// pass vacuously if the conflict ever stopped forming.
	var configConflicts int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_conflicts WHERE `table` = 'config'").Scan(&configConflicts); err != nil {
		_ = tx.Rollback()
		t.Fatalf("query config conflicts: %v (mergeErr: %v)", err, mergeErr)
	}
	if configConflicts == 0 {
		_ = tx.Rollback()
		t.Fatalf("merge produced no config conflict on %q; the resolve/decline path is not exercised (mergeErr: %v)", key, mergeErr)
	}

	resolved, resolveErr := store.tryAutoResolveMergeConflicts(ctx, tx)
	if resolveErr != nil {
		_ = tx.Rollback()
		t.Fatalf("tryAutoResolveMergeConflicts error: %v (mergeErr: %v)", resolveErr, mergeErr)
	}
	if !resolved {
		_ = tx.Rollback()
		return false, db
	}
	if !commitOnResolve {
		_ = tx.Rollback()
		return true, db
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit after auto-resolve: %v", err)
	}
	return true, db
}

// TestCommitBeforePullRefusesUnsafeConfig is the GH#2455 guard on the pre-pull
// auto-commit: it must include this clone's own user kv.* data so it stops
// wedging DOLT_MERGE (GH#2474), but it must NOT sweep an unrelated dirty INTERNAL
// config key such as issue_prefix into an automatic commit — that is the
// stale-config corruption GH#2455 fixed. With a non-kv. config key dirty,
// commitBeforePull must refuse, name the key, leave config uncommitted, and not
// advance HEAD.
func TestCommitBeforePullRefusesUnsafeConfig(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	headBefore, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("get HEAD before: %v", err)
	}

	// A non-memory config key dirties config (a stale issue_prefix edit is the
	// exact GH#2455 hazard). REPLACE works whether or not the row already exists.
	if _, err := db.ExecContext(ctx,
		"REPLACE INTO config (`key`, value) VALUES ('issue_prefix', 'unsafe-prefix')"); err != nil {
		t.Fatalf("dirty non-memory config row: %v", err)
	}
	if !configDirty(t, ctx, db) {
		t.Fatalf("non-memory config row did not dirty config; cannot reproduce the hazard")
	}

	err = store.commitBeforePull(ctx, "auto-commit before pull")
	if err == nil {
		t.Fatalf("commitBeforePull auto-committed an unsafe dirty config key; GH#2455 stale-config sweep is reintroduced")
	}
	if !strings.Contains(err.Error(), "issue_prefix") {
		t.Errorf("refusal should name the unsafe key issue_prefix; got: %v", err)
	}
	if !configDirty(t, ctx, db) {
		t.Errorf("commitBeforePull must leave the unsafe config row uncommitted for the operator")
	}
	headAfter, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("get HEAD after: %v", err)
	}
	if headAfter != headBefore {
		t.Errorf("commitBeforePull advanced HEAD (%s -> %s) despite refusing unsafe config", headBefore, headAfter)
	}
}

// TestCommitMergeResolutionIncludesConfig proves the bd federation sync
// --strategy resolution commit includes config where plain Commit drops it.
// A config-only resolution is routine now that kv.memory.* memories sync through
// config; if it is not committed the merge stays unconcluded and re-wedges the
// next sync (GH#2474). It uses a non-memory key to show CommitMergeResolution
// commits whatever the operator resolved, unlike the kv.*-screened commitBeforePull.
func TestCommitMergeResolutionIncludesConfig(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	// A resolved config row left in the working set after --strategy.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES ('kv.custom.resolved', 'theirs')"); err != nil {
		t.Fatalf("insert resolved config row: %v", err)
	}

	// Plain Commit excludes config (GH#2455), so it would silently drop the
	// resolution — the bug. (With only config dirty it commits nothing.)
	if err := store.Commit(ctx, "plain commit excludes config"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !configDirty(t, ctx, db) {
		t.Fatalf("plain Commit committed config; cannot demonstrate the resolution-commit gap")
	}

	// CommitMergeResolution must commit it, concluding the merge.
	if err := store.CommitMergeResolution(ctx, "Resolve conflicts using theirs strategy"); err != nil {
		t.Fatalf("CommitMergeResolution: %v", err)
	}
	if configDirty(t, ctx, db) {
		t.Fatalf("CommitMergeResolution left config dirty; a config-only sync resolution would not be committed (GH#2474 re-wedge)")
	}
}

// TestStrategyResolutionCommitsConfigConflict drives an actual config merge
// conflict through the resolution sequence bd federation sync --strategy runs —
// ResolveConflicts(theirs) then the resolution commit — and asserts config is
// committed. Before the fix the resolution committed through config-excluding
// Commit (GH#2455), so a config-only resolution returned success without
// committing, leaving config dirty and re-wedging the next sync (GH#2474).
//
// The conflicted working set is staged in a conflict-tolerant transaction (what
// a conflict-tolerant merge leaves behind) so the test can then exercise the
// real autocommit ResolveConflicts + CommitMergeResolution calls without the
// MaxOpenConns=1 deadlock that holding the tx open would cause.
func TestStrategyResolutionCommitsConfigConflict(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db
	key := "kv.memory.sync-strategy"

	commitConfig := func(msg string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('config')"); err != nil {
			t.Fatalf("dolt add config: %v", err)
		}
		if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?)", msg); err != nil {
			t.Fatalf("dolt commit: %v", err)
		}
	}

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("active_branch: %v", err)
	}

	// Our value on the current branch.
	if _, err := db.ExecContext(ctx, "INSERT INTO config (`key`, value) VALUES (?, ?)", key, "ours"); err != nil {
		t.Fatalf("insert local config: %v", err)
	}
	commitConfig("local config")

	// Their value on a branch forked from the common ancestor (HEAD~1).
	remoteBranch := currentBranch + "_syncstrategy"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD~1')", remoteBranch); err != nil {
		t.Fatalf("create remote branch: %v", err)
	}
	defer func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", remoteBranch)
	}()
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", remoteBranch); err != nil {
		t.Fatalf("checkout remote branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO config (`key`, value) VALUES (?, ?)", key, "theirs"); err != nil {
		t.Fatalf("insert remote config: %v", err)
	}
	commitConfig("remote config")
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current branch: %v", err)
	}

	// Merge in a conflict-tolerant tx and commit the tx so the conflicted working
	// set persists (autocommit rolls a conflicted merge back). This is the state
	// the resolution sequence inherits.
	func() {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin merge tx: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
			t.Fatalf("set dolt_allow_commit_conflicts: %v", err)
		}
		if _, err := tx.ExecContext(ctx, "SET @@dolt_force_transaction_commit = 1"); err != nil {
			t.Fatalf("set dolt_force_transaction_commit: %v", err)
		}
		if _, err := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", remoteBranch); err != nil {
			t.Fatalf("merge: %v", err)
		}
		var n int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_conflicts WHERE `table` = 'config'").Scan(&n); err != nil {
			t.Fatalf("query config conflicts: %v", err)
		}
		if n == 0 {
			t.Fatalf("merge produced no config conflict; cannot exercise the --strategy path")
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit conflicted working set: %v", err)
		}
	}()

	// The exact resolution sequence Sync runs for --strategy theirs.
	if err := store.ResolveConflicts(ctx, "config", "theirs"); err != nil {
		t.Fatalf("ResolveConflicts: %v", err)
	}
	if err := store.CommitMergeResolution(ctx, "Resolve conflicts using theirs strategy"); err != nil {
		t.Fatalf("CommitMergeResolution: %v", err)
	}

	if configDirty(t, ctx, db) {
		t.Fatalf("config left dirty after --strategy resolution; the merge is unconcluded and re-wedges the next sync (GH#2474)")
	}
	var value string
	if err := db.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value); err != nil {
		t.Fatalf("read resolved config value: %v", err)
	}
	if value != "theirs" {
		t.Errorf("resolved config value = %q, want \"theirs\" (--strategy theirs)", value)
	}
}

// TestCommitBeforePullCommitsGenericKV is the regression for the generic kv.*
// pre-pull gap (PR #4412 review attempt 4): `bd kv set foo bar` writes a kv.foo
// row into the synced config table without committing it (SetConfig does not
// commit), and plain Commit excludes config (GH#2455), so the row sits dirty and
// the next server-mode pull/sync would refuse to start. The pre-pull auto-commit
// must include the whole user kv.* namespace — not just kv.memory.* memories — so
// an ordinary `bd kv set` no longer wedges DOLT_MERGE. (The narrower auto-resolve
// policy stays kv.memory.*-only; see TestPullAutoResolveSkipsNonMemoryConfigConflicts.)
func TestCommitBeforePullCommitsGenericKV(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	headBefore, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("get HEAD before: %v", err)
	}

	// Simulate `bd kv set custom.setting v1`: a generic (non-memory) kv.* row.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES ('kv.custom.setting', 'v1')"); err != nil {
		t.Fatalf("insert generic kv config row: %v", err)
	}
	if !configDirty(t, ctx, db) {
		t.Fatalf("generic kv row did not dirty config; cannot reproduce the wedge")
	}

	// commitBeforePull must stage the kv.* row and leave config clean — the same
	// un-wedge it already does for memories — instead of refusing it.
	if err := store.commitBeforePull(ctx, "auto-commit before pull"); err != nil {
		t.Fatalf("commitBeforePull refused a generic kv.* row; `bd kv set` still wedges pull/sync: %v", err)
	}
	if configDirty(t, ctx, db) {
		t.Fatalf("commitBeforePull left a generic kv.* row dirty; DOLT_MERGE would still refuse to start")
	}
	headAfter, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("get HEAD after: %v", err)
	}
	if headAfter == headBefore {
		t.Fatalf("commitBeforePull did not advance HEAD; the generic kv.* row was not committed")
	}
}

// TestVCMergeStrategyCommitsConfigResolution is the server-mode regression for
// the `bd vc merge --strategy` config-resolution gap (PR #4412 review attempt 4):
// cmd/bd/vc.go concluded the resolution with config-excluding Commit (GH#2455), so
// a resolved INTERNAL config key — issue_prefix is the exact re-wedge the finding
// names, since unlike a kv.memory.* key it does not self-heal on the next pull —
// was dropped, leaving the merge unconcluded and refusing the next pull (GH#2474).
// It drives the same ResolveConflicts + CommitMergeResolution sequence cmd/bd/vc.go
// now runs and asserts config is committed and converged.
//
// The conflicted working set is staged in a conflict-tolerant transaction (what a
// conflict-tolerant merge leaves behind) so the test can then exercise the real
// autocommit ResolveConflicts + CommitMergeResolution calls without the
// MaxOpenConns=1 deadlock that holding the tx open would cause.
func TestVCMergeStrategyCommitsConfigResolution(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db
	// issue_prefix is an internal key (not kv.*): a resolved value left dirty
	// re-wedges the next pull via assertDirtyConfigUserKVOnly, which is precisely
	// why the resolution must be committed with config included.
	key := "issue_prefix"

	commitConfig := func(msg string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('config')"); err != nil {
			t.Fatalf("dolt add config: %v", err)
		}
		if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?)", msg); err != nil {
			t.Fatalf("dolt commit: %v", err)
		}
	}

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("active_branch: %v", err)
	}

	// Our value on the current branch. REPLACE because issue_prefix is seeded at
	// bootstrap, so the three-way merge base already carries the key.
	if _, err := db.ExecContext(ctx, "REPLACE INTO config (`key`, value) VALUES (?, ?)", key, "ours"); err != nil {
		t.Fatalf("insert local config: %v", err)
	}
	commitConfig("local config")

	// Their value on a branch forked from the common ancestor (HEAD~1).
	remoteBranch := currentBranch + "_vcmergestrategy"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD~1')", remoteBranch); err != nil {
		t.Fatalf("create remote branch: %v", err)
	}
	defer func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", remoteBranch)
	}()
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", remoteBranch); err != nil {
		t.Fatalf("checkout remote branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "REPLACE INTO config (`key`, value) VALUES (?, ?)", key, "theirs"); err != nil {
		t.Fatalf("insert remote config: %v", err)
	}
	commitConfig("remote config")
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current branch: %v", err)
	}

	// Merge in a conflict-tolerant tx and commit the tx so the conflicted working
	// set persists (autocommit rolls a conflicted merge back). This is the state
	// the resolution sequence inherits.
	func() {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin merge tx: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
			t.Fatalf("set dolt_allow_commit_conflicts: %v", err)
		}
		if _, err := tx.ExecContext(ctx, "SET @@dolt_force_transaction_commit = 1"); err != nil {
			t.Fatalf("set dolt_force_transaction_commit: %v", err)
		}
		if _, err := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", remoteBranch); err != nil {
			t.Fatalf("merge: %v", err)
		}
		var n int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_conflicts WHERE `table` = 'config'").Scan(&n); err != nil {
			t.Fatalf("query config conflicts: %v", err)
		}
		if n == 0 {
			t.Fatalf("merge produced no config conflict on %q; cannot exercise the --strategy path", key)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit conflicted working set: %v", err)
		}
	}()

	// The exact resolution sequence cmd/bd/vc.go runs for --strategy theirs.
	if err := store.ResolveConflicts(ctx, "config", "theirs"); err != nil {
		t.Fatalf("ResolveConflicts: %v", err)
	}
	if err := store.CommitMergeResolution(ctx, "Resolve merge conflicts using theirs strategy"); err != nil {
		t.Fatalf("CommitMergeResolution: %v", err)
	}

	if configDirty(t, ctx, db) {
		t.Fatalf("config left dirty after bd vc merge --strategy resolution of %q; the merge is unconcluded and re-wedges the next pull (GH#2474)", key)
	}
	var value string
	if err := db.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value); err != nil {
		t.Fatalf("read resolved config value: %v", err)
	}
	if value != "theirs" {
		t.Errorf("resolved config value = %q, want \"theirs\" (--strategy theirs)", value)
	}
}
