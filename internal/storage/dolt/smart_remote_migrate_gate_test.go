package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestDoltNew_SmartRemoteMigrateGate_RealDolt exercises the state-aware smart
// gate (#4516) end-to-end against a real Dolt server with a genuine cached
// remote-tracking ref — the cross-clone scenario the sqlmock unit tests cannot
// reach, because the smart router's `schema_migrations AS OF 'remotes/origin/main'`
// read only resolves against an actually-pushed remote.
//
// One create/drop cycle walks three states by regressing the LOCAL working set
// away from a pushed-and-matching remote (consecutive create/drop cycles
// destabilize the test dolt server, so a single fixture covers all cases):
//
//	auto-migrate : remote == local, at/above the convergence floor, no skew -> allow
//	adopt        : remote ahead of local, no skew                          -> stop (adopt)
//	fork-skew    : a shared version's content hash diverges                 -> stop (fork-skew)
//
// The gate decision is read directly via schema.CheckRemoteMigrateGate; it does
// not run MigrateUp, so regressing only the schema_migrations cursor rows (the
// applied schema itself stays in place) is a faithful stand-in for a clone that
// genuinely lags the binary.
func TestDoltNew_SmartRemoteMigrateGate_RealDolt(t *testing.T) {
	skipIfNoDolt(t)
	t.Setenv(schema.SmartGateEnv, "1")
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)

	store, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
		MaxOpenConns:    1, // single session so working-set regressions are visible to the gate reads
	})
	if err != nil {
		t.Fatalf("New (create): %v", err)
	}
	db := store.db
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	latest := schema.LatestVersion()
	floor := schema.LastNonDeterministicMigration
	// The fixture needs two versions below latest that are still at/above the
	// convergence floor, so the auto-migrate precondition (current >= floor) holds.
	if latest-2 < floor {
		t.Skipf("latest=%d too close to floor=%d to build the fixture", latest, floor)
	}
	pAuto := latest - 1 // current after dropping the latest cursor row
	pShared := latest - 2

	mustExec := func(stage, q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}

	// Register the sync remote, then push the FULL schema so remotes/origin/main
	// is cached. (file:// inside the server's filesystem; Dolt creates it on push.)
	mustExec("add remote", "CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(tmpDir, "remote"))

	// --- auto-migrate: regress local to pAuto AND push so the remote matches. ---
	mustExec("regress to pAuto", "DELETE FROM schema_migrations WHERE version = ?", latest)
	mustExec("stage", "CALL DOLT_ADD('-A')")
	mustExec("commit", "CALL DOLT_COMMIT('--allow-empty', '-m', 'test: regress cursor to pAuto')")
	mustExec("push", "CALL DOLT_PUSH('origin', 'main')")

	if err := schema.CheckRemoteMigrateGate(ctx, db); err != nil {
		t.Fatalf("auto-migrate case: remote==local at floor with no skew should be allowed, got %v", err)
	}

	// --- adopt: drop one more cursor row locally WITHOUT pushing -> remote ahead. ---
	mustExec("regress local below remote", "DELETE FROM schema_migrations WHERE version = ?", pAuto)
	{
		err := schema.CheckRemoteMigrateGate(ctx, db)
		var gateErr *schema.RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("adopt case: expected gate error, got %v", err)
		}
		if gateErr.Decision != "adopt" {
			t.Errorf("adopt case: Decision = %q, want %q", gateErr.Decision, "adopt")
		}
	}

	// --- fork-skew: diverge a SHARED version's local content hash. ---
	mustExec("tamper shared hash",
		"UPDATE schema_migrations SET content_hash = 'smart-gate-divergent' WHERE version = ?", pShared)
	{
		err := schema.CheckRemoteMigrateGate(ctx, db)
		var gateErr *schema.RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("fork-skew case: expected gate error, got %v", err)
		}
		if gateErr.Decision != "fork-skew" {
			t.Errorf("fork-skew case: Decision = %q, want %q", gateErr.Decision, "fork-skew")
		}
		found := false
		for _, v := range gateErr.SkewVersions {
			if v == pShared {
				found = true
			}
		}
		if !found {
			t.Errorf("fork-skew case: SkewVersions = %v, want to contain %d", gateErr.SkewVersions, pShared)
		}
	}
}

// realFastForwardAdopter wires a *schema.FastForwardAdopter to the real
// versioncontrolops driver primitives, exactly like dolt/store.go's
// production injection (initSchema) — the cross-clone tests below exercise
// the SAME wiring, not a fake.
func realFastForwardAdopter() *schema.FastForwardAdopter {
	return &schema.FastForwardAdopter{
		IsStrictAncestor: func(ctx context.Context, db schema.DBConn, ref string) (bool, error) {
			return versioncontrolops.LocalIsStrictAncestorOf(ctx, db, ref)
		},
		WorkingSetClean: func(ctx context.Context, db schema.DBConn) (bool, error) {
			return versioncontrolops.WorkingSetClean(ctx, db)
		},
		FastForward: func(ctx context.Context, db schema.DBConn, ref string) error {
			return versioncontrolops.FastForwardAdopt(ctx, db, ref)
		},
	}
}

// TestDoltNew_SmartRemoteMigrateGate_AutoFastForward_RealDolt (mybd-ae1i
// piece 3) exercises the auto fast-forward against a real Dolt server with a
// genuine multi-commit ancestor relationship — the sqlmock unit tests in
// internal/storage/schema cannot reach this because a real DOLT_MERGE
// ('--ff-only', ...) needs an actual ancestor commit graph, not just
// canned bool results.
//
// The fixture models two real clones sharing a file:// remote, all inside
// the same test Dolt server (mirrors the peer pattern in
// TestPullFromSettlesFKCascadeViolations):
//
//   - "source" starts one migration BEHIND latest (commit C0) and publishes
//     C0 to origin.
//   - "laggingClone" is a genuine server-side DOLT_CLONE of origin at C0 —
//     its own local HEAD and its cached remotes/origin/main both point at C0.
//   - "source" (still the same connection) then migrates further (commit C1,
//     a child of C0) and pushes — origin/main now advances to C1, but
//     laggingClone's own branch HEAD is untouched (still C0).
//   - laggingClone runs CALL DOLT_FETCH('origin') to refresh only its cached
//     remotes/origin/main ref to C1 (no merge) — exactly the no-network-of-
//     its-own contract the smart gate relies on.
//
// laggingClone's local HEAD (C0) is now a genuine STRICT ancestor of its
// cached remotes/origin/main (C1) with a clean working set: the real
// preconditions for smartAdoptFastForward, not simulated ones.
func TestDoltNew_SmartRemoteMigrateGate_AutoFastForward_RealDolt(t *testing.T) {
	skipIfNoDolt(t)
	t.Setenv(schema.SmartGateEnv, "1")
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")

	ctx, cancel := testContext(t)
	defer cancel()

	latest := schema.LatestVersion()
	floor := schema.LastNonDeterministicMigration
	if latest-1 < floor {
		t.Skipf("latest=%d too close to floor=%d to build the fixture", latest, floor)
	}
	pBehind := latest - 1

	tmpDir := t.TempDir()
	sourceDB := uniqueTestDBName(t)

	source, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        sourceDB,
		CreateIfMissing: true,
		MaxOpenConns:    1, // single session so working-set regressions are visible to the gate reads
	})
	if err != nil {
		t.Fatalf("New (source): %v", err)
	}
	sdb := source.db
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = sdb.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", sourceDB))
		source.Close()
	}()

	mustExec := func(stage string, db *sql.DB, q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}

	remoteURL := "file://" + filepath.Join(tmpDir, "ff-remote")

	// Capture the latest row's real content hash before deleting it, so
	// re-adding it later reproduces the exact same commit content instead
	// of inventing a fake one.
	var latestHash string
	if err := sdb.QueryRowContext(ctx,
		"SELECT content_hash FROM schema_migrations WHERE version = ?", latest).Scan(&latestHash); err != nil {
		t.Fatalf("read latest content_hash: %v", err)
	}

	// --- source: regress to pBehind (commit C0) and publish it. ---
	mustExec("regress to pBehind", sdb, "DELETE FROM schema_migrations WHERE version = ?", latest)
	mustExec("stage C0", sdb, "CALL DOLT_ADD('-A')")
	mustExec("commit C0", sdb, "CALL DOLT_COMMIT('--allow-empty', '-m', 'test: regress to pBehind (C0)')")
	mustExec("add remote", sdb, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
	mustExec("push C0", sdb, "CALL DOLT_PUSH('origin', 'main')")

	// --- laggingClone: a genuine server-side clone of origin at C0. ---
	laggingDB := uniqueTestDBName(t)
	mustExec("clone lagging peer", sdb, "CALL DOLT_CLONE(?, ?)", remoteURL, laggingDB)
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = sdb.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", laggingDB))
	}()

	laggingConn, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: laggingDB,
	}.String())
	if err != nil {
		t.Fatalf("open laggingClone connection: %v", err)
	}
	defer laggingConn.Close()
	laggingConn.SetMaxOpenConns(1)

	// --- source: migrate further (commit C1, child of C0) and publish. ---
	mustExec("re-add latest row", sdb, "INSERT INTO schema_migrations (version, content_hash) VALUES (?, ?)", latest, latestHash)
	mustExec("stage C1", sdb, "CALL DOLT_ADD('-A')")
	mustExec("commit C1", sdb, "CALL DOLT_COMMIT('-m', 'test: re-migrate to latest (C1)')")
	mustExec("push C1", sdb, "CALL DOLT_PUSH('origin', 'main')")

	// --- laggingClone: fetch only (no merge) so its cached ref advances but
	// its own branch HEAD stays at C0. ---
	mustExec("fetch (no merge)", laggingConn, "CALL DOLT_FETCH('origin')")

	// Sanity: laggingClone's OWN schema is still behind before the gate runs.
	var laggingCurrent int
	if err := laggingConn.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&laggingCurrent); err != nil {
		t.Fatalf("read laggingClone current version: %v", err)
	}
	if laggingCurrent != pBehind {
		t.Fatalf("laggingClone current version = %d before the gate, want %d (still behind)", laggingCurrent, pBehind)
	}

	// The real preconditions now hold: laggingClone's local HEAD is a strict
	// ancestor of its cached remotes/origin/main, with a clean working set.
	// The gate must auto fast-forward silently (nil error) rather than
	// stopping with any directive.
	if err := schema.CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(
		ctx, laggingConn, "origin", nil, realFastForwardAdopter()); err != nil {
		t.Fatalf("auto fast-forward should succeed silently, got %v", err)
	}

	var laggingAfter int
	if err := laggingConn.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&laggingAfter); err != nil {
		t.Fatalf("read laggingClone version after fast-forward: %v", err)
	}
	if laggingAfter != latest {
		t.Fatalf("laggingClone version after fast-forward = %d, want %d (HEAD should now match origin)", laggingAfter, latest)
	}

	// HEAD must now equal origin/main exactly (ahead == 0 AND behind == 0),
	// not merely have advanced.
	var ahead, behind int
	if err := laggingConn.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM dolt_log WHERE commit_hash NOT IN
				(SELECT commit_hash FROM dolt_log AS OF 'remotes/origin/main')) AS ahead,
			(SELECT COUNT(*) FROM dolt_log AS OF 'remotes/origin/main' WHERE commit_hash NOT IN
				(SELECT commit_hash FROM dolt_log)) AS behind
	`).Scan(&ahead, &behind); err != nil {
		t.Fatalf("compare laggingClone HEAD to origin/main: %v", err)
	}
	if ahead != 0 || behind != 0 {
		t.Fatalf("laggingClone HEAD vs origin/main: ahead=%d behind=%d, want 0/0 (HEAD == remote)", ahead, behind)
	}
}

// TestDoltNew_SmartRemoteMigrateGate_UnpushedCommitDegrades_RealDolt
// (mybd-ae1i piece 3) covers the fallback half of the same real-Dolt
// fixture: a clone with an unpushed local commit is NOT a strict ancestor of
// the cached remote ref, so the gate must fall through to the destructive
// smartAdopt directive — never force a fast-forward — and must not move any
// data (the clone's own schema stays exactly where it was).
func TestDoltNew_SmartRemoteMigrateGate_UnpushedCommitDegrades_RealDolt(t *testing.T) {
	skipIfNoDolt(t)
	t.Setenv(schema.SmartGateEnv, "1")
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")

	ctx, cancel := testContext(t)
	defer cancel()

	latest := schema.LatestVersion()
	floor := schema.LastNonDeterministicMigration
	if latest-1 < floor {
		t.Skipf("latest=%d too close to floor=%d to build the fixture", latest, floor)
	}
	pBehind := latest - 1

	tmpDir := t.TempDir()
	sourceDB := uniqueTestDBName(t)

	source, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        sourceDB,
		CreateIfMissing: true,
		MaxOpenConns:    1,
	})
	if err != nil {
		t.Fatalf("New (source): %v", err)
	}
	sdb := source.db
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = sdb.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", sourceDB))
		source.Close()
	}()

	mustExec := func(stage string, db *sql.DB, q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}

	remoteURL := "file://" + filepath.Join(tmpDir, "ff-remote-unpushed")

	var latestHash string
	if err := sdb.QueryRowContext(ctx,
		"SELECT content_hash FROM schema_migrations WHERE version = ?", latest).Scan(&latestHash); err != nil {
		t.Fatalf("read latest content_hash: %v", err)
	}

	mustExec("regress to pBehind", sdb, "DELETE FROM schema_migrations WHERE version = ?", latest)
	mustExec("stage C0", sdb, "CALL DOLT_ADD('-A')")
	mustExec("commit C0", sdb, "CALL DOLT_COMMIT('--allow-empty', '-m', 'test: regress to pBehind (C0)')")
	mustExec("add remote", sdb, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
	mustExec("push C0", sdb, "CALL DOLT_PUSH('origin', 'main')")

	forkedDB := uniqueTestDBName(t)
	mustExec("clone forked peer", sdb, "CALL DOLT_CLONE(?, ?)", remoteURL, forkedDB)
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = sdb.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", forkedDB))
	}()

	forkedConn, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: forkedDB,
	}.String())
	if err != nil {
		t.Fatalf("open forkedClone connection: %v", err)
	}
	defer forkedConn.Close()
	forkedConn.SetMaxOpenConns(1)

	// The forked clone makes its OWN unpushed local commit — an ordinary
	// working-set edit (a config row, not a schema-migrations tamper) so it
	// diverges from origin without introducing any content-hash skew.
	mustExec("forked local edit", forkedConn, "REPLACE INTO config (`key`, value) VALUES ('ff-test-marker', 'unpushed')")
	mustExec("forked local commit", forkedConn, "CALL DOLT_COMMIT('-Am', 'test: forked clone local-only commit')")

	// source migrates further and publishes (same as the auto-FF test).
	mustExec("re-add latest row", sdb, "INSERT INTO schema_migrations (version, content_hash) VALUES (?, ?)", latest, latestHash)
	mustExec("stage C1", sdb, "CALL DOLT_ADD('-A')")
	mustExec("commit C1", sdb, "CALL DOLT_COMMIT('-m', 'test: re-migrate to latest (C1)')")
	mustExec("push C1", sdb, "CALL DOLT_PUSH('origin', 'main')")

	mustExec("fetch (no merge)", forkedConn, "CALL DOLT_FETCH('origin')")

	err = schema.CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(
		ctx, forkedConn, "origin", nil, realFastForwardAdopter())
	var gateErr *schema.RemoteMigrateGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected gate error (destructive adopt), got %v", err)
	}
	if gateErr.Decision != "adopt" {
		t.Errorf("Decision = %q, want %q (an unpushed local commit must degrade to the plain destructive adopt, never fast-forward)", gateErr.Decision, "adopt")
	}

	// No data motion: the forked clone's own schema must be untouched.
	var forkedCurrent int
	if err := forkedConn.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&forkedCurrent); err != nil {
		t.Fatalf("read forkedClone version after gate refusal: %v", err)
	}
	if forkedCurrent != pBehind {
		t.Fatalf("forkedClone version after gate refusal = %d, want %d (no data motion)", forkedCurrent, pBehind)
	}
}

// TestDoltNew_SmartRemoteMigrateGate_BelowLatestDegrades_RealDolt (mybd-ae1i
// follow-up fix) covers blocker 1 (fork risk) against a real Dolt server: a
// clone whose cached remote ref is a genuine strict-ancestor fast-forward
// candidate, but landing there would NOT reach this binary's latest
// migration. Auto-executing the fast-forward here would leave MigrateUp to
// apply the remaining migrations unconditionally in place immediately
// afterward, with no gate re-evaluation — reintroducing the #4259 fork risk
// this gate exists to prevent. The gate must never call FastForward in this
// case; it must still report the accurate loss-free adopt-ff directive
// (ancestor + clean genuinely hold), just not execute it.
//
// The fixture mirrors TestDoltNew_SmartRemoteMigrateGate_AutoFastForward_RealDolt
// exactly, except source's second commit only advances ONE step (to
// latest-1) instead of all the way back to latest — so laggingClone's cached
// remotes/origin/main ends up at latest-1, strictly between its own current
// version and the binary's latest.
func TestDoltNew_SmartRemoteMigrateGate_BelowLatestDegrades_RealDolt(t *testing.T) {
	skipIfNoDolt(t)
	t.Setenv(schema.SmartGateEnv, "1")
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")

	ctx, cancel := testContext(t)
	defer cancel()

	latest := schema.LatestVersion()
	floor := schema.LastNonDeterministicMigration
	if latest-2 < floor {
		t.Skipf("latest=%d too close to floor=%d to build the fixture", latest, floor)
	}
	pStart := latest - 2 // laggingClone's own version throughout
	pMid := latest - 1   // remote's version after source's second commit — still short of latest

	tmpDir := t.TempDir()
	sourceDB := uniqueTestDBName(t)

	source, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        sourceDB,
		CreateIfMissing: true,
		MaxOpenConns:    1,
	})
	if err != nil {
		t.Fatalf("New (source): %v", err)
	}
	sdb := source.db
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = sdb.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", sourceDB))
		source.Close()
	}()

	mustExec := func(stage string, db *sql.DB, q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}

	remoteURL := "file://" + filepath.Join(tmpDir, "ff-remote-below-latest")

	// Capture pMid's real content hash before deleting it, so re-adding it
	// later reproduces the exact same commit content. The "latest" row is
	// simply dropped and never re-applied — the remote must stay short of
	// latest for this fixture.
	var pMidHash string
	if err := sdb.QueryRowContext(ctx,
		"SELECT content_hash FROM schema_migrations WHERE version = ?", pMid).Scan(&pMidHash); err != nil {
		t.Fatalf("read pMid content_hash: %v", err)
	}

	// --- source: regress to pStart (commit C0) and publish it. ---
	mustExec("regress to pStart", sdb, "DELETE FROM schema_migrations WHERE version IN (?, ?)", latest, pMid)
	mustExec("stage C0", sdb, "CALL DOLT_ADD('-A')")
	mustExec("commit C0", sdb, "CALL DOLT_COMMIT('--allow-empty', '-m', 'test: regress to pStart (C0)')")
	mustExec("add remote", sdb, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
	mustExec("push C0", sdb, "CALL DOLT_PUSH('origin', 'main')")

	// --- laggingClone: a genuine server-side clone of origin at C0. ---
	laggingDB := uniqueTestDBName(t)
	mustExec("clone lagging peer", sdb, "CALL DOLT_CLONE(?, ?)", remoteURL, laggingDB)
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = sdb.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", laggingDB))
	}()

	laggingConn, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: laggingDB,
	}.String())
	if err != nil {
		t.Fatalf("open laggingClone connection: %v", err)
	}
	defer laggingConn.Close()
	laggingConn.SetMaxOpenConns(1)

	// --- source: migrate ONE step further (commit C1, child of C0, to
	// pMid) and publish — deliberately NOT all the way to latest. ---
	mustExec("re-add pMid row", sdb, "INSERT INTO schema_migrations (version, content_hash) VALUES (?, ?)", pMid, pMidHash)
	mustExec("stage C1", sdb, "CALL DOLT_ADD('-A')")
	mustExec("commit C1", sdb, "CALL DOLT_COMMIT('-m', 'test: re-migrate to pMid, short of latest (C1)')")
	mustExec("push C1", sdb, "CALL DOLT_PUSH('origin', 'main')")

	// --- laggingClone: fetch only (no merge) so its cached ref advances but
	// its own branch HEAD stays at C0. ---
	mustExec("fetch (no merge)", laggingConn, "CALL DOLT_FETCH('origin')")

	// Sanity: laggingClone's OWN schema is still behind before the gate runs.
	var laggingCurrent int
	if err := laggingConn.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&laggingCurrent); err != nil {
		t.Fatalf("read laggingClone current version: %v", err)
	}
	if laggingCurrent != pStart {
		t.Fatalf("laggingClone current version = %d before the gate, want %d (still behind)", laggingCurrent, pStart)
	}

	// The ancestor + clean preconditions genuinely hold (same shape as the
	// auto-fast-forward test), but remoteMax (pMid) != latest — the gate
	// must degrade to the adopt-ff directive WITHOUT executing any write.
	err = schema.CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(
		ctx, laggingConn, "origin", nil, realFastForwardAdopter())
	var gateErr *schema.RemoteMigrateGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected gate error (adopt-ff directive, no execution), got %v", err)
	}
	if gateErr.Decision != "adopt-ff" {
		t.Errorf("Decision = %q, want %q (remoteMax < latest must never auto-execute, but is still a loss-free adopt candidate)", gateErr.Decision, "adopt-ff")
	}

	// No data motion: laggingClone's own schema must be untouched — the
	// fast-forward must never have executed.
	var laggingAfter int
	if err := laggingConn.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&laggingAfter); err != nil {
		t.Fatalf("read laggingClone version after gate refusal: %v", err)
	}
	if laggingAfter != pStart {
		t.Fatalf("laggingClone version after gate refusal = %d, want %d (no data motion — must not have fast-forwarded to pMid=%d or beyond)", laggingAfter, pStart, pMid)
	}
}
