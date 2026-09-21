package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestDoltNew_SmartRemoteMigrateGate_DataBehindBlocks_RealDolt is the
// regression test for gastownhall/beads#6575: the smart gate's first-mover
// verdict used to be computed from schema facts alone, so a clone level with
// the remote on SCHEMA but behind it in DATA commits was classified a safe
// first-mover and auto-migrated in place — minting local-only schema commits
// on a HEAD missing the remote's data commits, which is one route into the
// #6368 events pull refusal.
//
// The fixture builds a genuinely data-behind clone against a real Dolt server
// — the behind-ness is MEASURED here (LocalIsStrictAncestorOf against the
// clone's own cached ref), never stubbed, because a test that fakes the fact
// the bug is about proves nothing:
//
//   - "source" regresses its schema cursor one migration below latest (commit
//     C0) and publishes C0 to a file:// origin.
//   - "laggingClone" is a genuine server-side DOLT_CLONE of origin at C0.
//   - "source" then makes a pure DATA commit C1 (a config row — no schema
//     change at all, so the two stay at the SAME schema version) and pushes.
//   - laggingClone runs CALL DOLT_FETCH('origin') only, so its cached
//     remotes/origin/main advances to C1 while its own branch HEAD stays at C0.
//
// laggingClone is now schema-level with the remote (equal content hashes,
// equal max version — the smart gate's first-mover precondition) and yet one
// data commit behind it. The gate must refuse with the pull-first fallback
// reason rather than auto-migrate.
//
// The second half then proves the remedy loop self-heals and that the fix does
// not over-refuse: once the clone fast-forwards (what `bd dolt pull` does), it
// is no longer a strict ancestor, and the very same gate call auto-migrates it
// as a true first-mover.
func TestDoltNew_SmartRemoteMigrateGate_DataBehindBlocks_RealDolt(t *testing.T) {
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

	remoteURL := "file://" + filepath.Join(tmpDir, "data-behind-remote")

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

	// --- source: a pure DATA commit C1 (no schema change) and publish. The
	// schema cursor stays at pBehind on BOTH sides, so the clone lands on the
	// gate's equal-version first-mover path while being a data commit behind. ---
	mustExec("source data edit", sdb, "REPLACE INTO config (`key`, value) VALUES ('data-behind-marker', 'C1')")
	mustExec("commit C1", sdb, "CALL DOLT_COMMIT('-Am', 'test: pure data commit (C1)')")
	mustExec("push C1", sdb, "CALL DOLT_PUSH('origin', 'main')")

	// --- laggingClone: fetch only (no merge) so its cached ref advances but
	// its own branch HEAD stays at C0. ---
	mustExec("fetch (no merge)", laggingConn, "CALL DOLT_FETCH('origin')")

	// Fixture sanity: the clone's schema cursor equals the remote's (the
	// first-mover precondition), not below it.
	var laggingCurrent int
	if err := laggingConn.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&laggingCurrent); err != nil {
		t.Fatalf("read laggingClone current version: %v", err)
	}
	if laggingCurrent != pBehind {
		t.Fatalf("laggingClone current version = %d, want %d", laggingCurrent, pBehind)
	}
	var remoteCurrent int
	if err := laggingConn.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations AS OF 'remotes/origin/main'").Scan(&remoteCurrent); err != nil {
		t.Fatalf("read cached remote current version: %v", err)
	}
	if remoteCurrent != laggingCurrent {
		t.Fatalf("cached remote version = %d, want %d (equal-version first-mover path)", remoteCurrent, laggingCurrent)
	}

	// The load-bearing measurement: the clone IS data-behind its own cached
	// remote ref, read through the same primitive the gate consults.
	behind, err := versioncontrolops.LocalIsStrictAncestorOf(ctx, laggingConn, "remotes/origin/main")
	if err != nil {
		t.Fatalf("LocalIsStrictAncestorOf: %v", err)
	}
	if !behind {
		t.Fatalf("fixture did not produce a data-behind clone: LocalIsStrictAncestorOf = false")
	}
	// And it is behind for data reasons only — the working set is clean, so the
	// refusal below cannot be attributed to local dirt.
	clean, err := versioncontrolops.WorkingSetClean(ctx, laggingConn)
	if err != nil {
		t.Fatalf("WorkingSetClean: %v", err)
	}
	if !clean {
		t.Fatalf("fixture working set is dirty; the data-behind refusal must not depend on dirt")
	}

	// #6575: the gate must refuse rather than auto-migrate this clone.
	err = schema.CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(
		ctx, laggingConn, "origin", nil, realFastForwardAdopter())
	var gateErr *schema.RemoteMigrateGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("data-behind clone: expected the gate to refuse, got %v (#6575: schema parity is not proof of first-mover status)", err)
	}
	if gateErr.Decision != "" {
		t.Errorf("Decision = %q, want \"\" (the blunt #4515 stop)", gateErr.Decision)
	}
	if got, want := gateErr.FallbackReason, "data-behind"; got != want {
		t.Errorf("FallbackReason = %q, want %q", got, want)
	}
	if msg := gateErr.UserMessage(); !strings.Contains(msg, "Run `bd dolt pull` first") {
		t.Errorf("UserMessage does not carry the smart gate's pull-first note:\n%s", msg)
	}

	// No data motion: the refusal must not have moved the clone's HEAD.
	var aheadAfter, behindAfter int
	if err := laggingConn.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM dolt_log WHERE commit_hash NOT IN
				(SELECT commit_hash FROM dolt_log AS OF 'remotes/origin/main')) AS ahead,
			(SELECT COUNT(*) FROM dolt_log AS OF 'remotes/origin/main' WHERE commit_hash NOT IN
				(SELECT commit_hash FROM dolt_log)) AS behind
	`).Scan(&aheadAfter, &behindAfter); err != nil {
		t.Fatalf("compare laggingClone HEAD to origin/main after refusal: %v", err)
	}
	if aheadAfter != 0 || behindAfter == 0 {
		t.Errorf("after refusal: ahead=%d behind=%d, want ahead=0 behind>=1 (no data motion)", aheadAfter, behindAfter)
	}

	// --- remedy loop: `bd dolt pull` (a pure fast-forward here) makes the
	// clone level with the remote, and the SAME gate call then auto-migrates it
	// as a true first-mover. This is what keeps the fix from over-refusing. ---
	mustExec("pull (fast-forward)", laggingConn, "CALL DOLT_MERGE('--ff-only', 'remotes/origin/main')")

	stillBehind, err := versioncontrolops.LocalIsStrictAncestorOf(ctx, laggingConn, "remotes/origin/main")
	if err != nil {
		t.Fatalf("LocalIsStrictAncestorOf after pull: %v", err)
	}
	if stillBehind {
		t.Fatalf("clone is still a strict ancestor after the fast-forward; fixture broken")
	}

	if err := schema.CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(
		ctx, laggingConn, "origin", nil, realFastForwardAdopter()); err != nil {
		t.Fatalf("level clone: the first-mover auto-migrate must still be allowed after pulling, got %v", err)
	}
}
