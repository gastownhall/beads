//go:build cgo

package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// TestShouldPurgeDroppedDatabasesGatesOnFlagAlone pins the --purge-dropped
// gating contract at the pure-function level: purge fires if and only if
// the flag is set, regardless of how many databases this run dropped. In
// particular it fixes a P1 found in review of the initial version of this
// change: gating on "did this run drop anything" instead of the flag alone
// meant a run that found zero stale databases (stale == 0, e.g. because a
// prior run already dropped them but never purged) silently skipped the
// purge even with --purge-dropped set, leaving that residue unreclaimed
// forever.
func TestShouldPurgeDroppedDatabasesGatesOnFlagAlone(t *testing.T) {
	cases := []struct {
		name         string
		purgeDropped bool
		droppedCount int
		want         bool
	}{
		{"flag off, nothing dropped this run", false, 0, false},
		{"flag off, dropped this run", false, 3, false},
		{"flag on, nothing dropped this run (residue case)", true, 0, true},
		{"flag on, dropped this run", true, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldPurgeDroppedDatabases(tc.purgeDropped, tc.droppedCount); got != tc.want {
				t.Errorf("shouldPurgeDroppedDatabases(%v, %d) = %v, want %v",
					tc.purgeDropped, tc.droppedCount, got, tc.want)
			}
		})
	}
}

// TestPurgeDroppedDatabasesRemovesUndropRecovery is the SQL-level regression
// gate for be-pq5's purge mechanism: purgeDroppedDatabases (called only when
// --purge-dropped is set — see TestShouldPurgeDroppedDatabasesGatesOnFlagAlone
// above and TestCleanDatabasesPurgeDroppedReclaimsResidue below for the CLI
// wiring) must actually reclaim a dropped database's directory, not just
// leave it recoverable.
//
// Dolt exposes no SQL view listing dropped-but-not-purged databases (the
// only on-disk signal is a directory inside the server's opaque data dir,
// which the shared testcontainer-based test server does not expose to the
// test process). Instead this uses Dolt's own `dolt_undrop()` stored
// procedure as the observable: a DROP DATABASE without a following PURGE
// leaves the database restorable via `CALL DOLT_UNDROP(name)`; once
// DOLT_PURGE_DROPPED_DATABASES() has run, the same call fails because
// there is nothing left to restore.
//
// purgeDroppedDatabases is server-global (see its doc comment), so this
// test assumes nothing else concurrently purges the shared test server
// between its own DROP and its own DOLT_UNDROP check. That holds here
// because neither subtest calls t.Parallel(): Go's testing package never
// runs two non-parallel top-level tests in the same package concurrently,
// so this test's DROP/(maybe PURGE)/UNDROP sequence for a given database
// name is never interleaved with another test's purge.
func TestPurgeDroppedDatabasesRemovesUndropRecovery(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("no test Dolt server running")
	}

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testDoltServerPort, User: "root"}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("connect to test dolt server: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()

	// Case (a): --purge-dropped's underlying mechanism. Purging after a DROP
	// removes the DOLT_UNDROP recovery option.
	t.Run("with purge, undrop fails", func(t *testing.T) {
		name := fmt.Sprintf("benchdb_purgetest_%d", time.Now().UnixNano())
		mustExec(t, ctx, db, fmt.Sprintf("CREATE DATABASE `%s`", name))
		mustExec(t, ctx, db, fmt.Sprintf("DROP DATABASE `%s`", name))

		if err := purgeDroppedDatabases(ctx, db); err != nil {
			t.Fatalf("purgeDroppedDatabases: %v", err)
		}

		undropCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err := db.ExecContext(undropCtx, fmt.Sprintf("CALL DOLT_UNDROP('%s')", name))
		if err == nil {
			// Undrop succeeded, meaning the database directory was still
			// present — the purge did not actually reclaim it. Clean up
			// the restored database before failing.
			mustExec(t, ctx, db, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name))
			t.Fatalf("DOLT_UNDROP(%q) succeeded after purgeDroppedDatabases; database was not actually purged", name)
		}
	})

	// Case (b): the default, no-flag behavior (shouldPurgeDroppedDatabases
	// returns false, so purgeDroppedDatabases is never called). Without a
	// purge, DROP DATABASE stays recoverable — this is what --purge-dropped
	// being opt-in preserves for anyone else relying on DOLT_UNDROP on a
	// shared server. It also pins the assertion methodology in case (a)
	// above: if Dolt ever changes so that DOLT_UNDROP no longer
	// distinguishes purged from merely-dropped databases, this control
	// catches it as a false negative here rather than making case (a)
	// silently vacuous.
	t.Run("without purge, undrop succeeds", func(t *testing.T) {
		name := fmt.Sprintf("benchdb_purgetest_%d", time.Now().UnixNano())
		mustExec(t, ctx, db, fmt.Sprintf("CREATE DATABASE `%s`", name))
		mustExec(t, ctx, db, fmt.Sprintf("DROP DATABASE `%s`", name))

		undropCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(undropCtx, fmt.Sprintf("CALL DOLT_UNDROP('%s')", name)); err != nil {
			t.Fatalf("DOLT_UNDROP(%q) failed without a purge in between: %v (assertion methodology in case (a) may be invalid)", name, err)
		}

		// The undrop recreated the database; drop and purge it for real so
		// the test doesn't leak a benchdb_* database into the shared server.
		mustExec(t, ctx, db, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name))
		if err := purgeDroppedDatabases(ctx, db); err != nil {
			t.Logf("cleanup purge after control case: %v", err)
		}
	})
}

// TestCleanDatabasesPurgeDroppedReclaimsResidue is the CLI-level regression
// gate for the residue P1: it drives the real `bd dolt clean-databases
// --purge-dropped` command (not just the extracted helpers) against a
// database that was already dropped — but not purged — before the command
// runs, so this invocation's own SHOW DATABASES scan finds nothing stale
// (stale == 0). Before the round-2 fix, the purge call was gated on
// `dropped > 0`, so this exact scenario silently skipped the purge and the
// residue was never reclaimed despite --purge-dropped being passed.
//
// The shared test Dolt server container is itself bootstrapped with a
// "beads_test" database (internal/testutil/container_provider.go), which
// matches a stale prefix — so a single clean-databases invocation against a
// fresh container always finds at least one stale entry (dropped > 0),
// which would mask this exact bug under the old dropped>0 gate. This test
// primes the server with an unconditional --purge-dropped run first (drops
// and purges "beads_test" and any other ambient residue), then creates its
// own residue database and runs clean-databases --purge-dropped a second
// time, by which point SHOW DATABASES genuinely has nothing stale to drop.
//
// Reuses the buildBDUnderTest/writeServerRepo/runBDCommand harness from
// explicit_db_nodb_test.go, which already exercises `bd dolt <subcommand>`
// against this package's shared test Dolt server the same way.
func TestCleanDatabasesPurgeDroppedReclaimsResidue(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("no test Dolt server running")
	}

	binPath := buildBDUnderTest(t)
	repoDir := t.TempDir()
	writeServerRepo(t, repoDir, "purge_residue_test_db", "127.0.0.1", "purge-residue-test-origin", testDoltServerPort)

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testDoltServerPort, User: "root"}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("connect to test dolt server: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	// Prime: clear any ambient stale/residue databases (notably the
	// container's own "beads_test" bootstrap DB) so the second, assertive
	// run below starts from a genuinely clean SHOW DATABASES.
	_ = runBDCommand(t, binPath, repoDir, nil, "dolt", "clean-databases", "--purge-dropped")

	// Simulate residue from a prior clean-databases run: drop a stale
	// database directly, without purging. Once dropped it no longer shows
	// up in SHOW DATABASES, so the upcoming clean-databases invocation's
	// own scan will find zero stale databases — exactly the scenario that
	// exposed the P1.
	residue := fmt.Sprintf("benchdb_residue_%d", time.Now().UnixNano())
	mustExec(t, ctx, db, fmt.Sprintf("CREATE DATABASE `%s`", residue))
	mustExec(t, ctx, db, fmt.Sprintf("DROP DATABASE `%s`", residue))

	out := string(runBDCommand(t, binPath, repoDir, nil, "dolt", "clean-databases", "--purge-dropped"))

	if !strings.Contains(out, "No stale databases found.") {
		t.Fatalf("expected this run to find no stale databases (residue is already dropped, not in SHOW DATABASES), got:\n%s", out)
	}
	if !strings.Contains(out, "Purged all dropped databases") {
		t.Fatalf("expected --purge-dropped to purge even with stale == 0, got:\n%s", out)
	}

	undropCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, undropErr := db.ExecContext(undropCtx, fmt.Sprintf("CALL DOLT_UNDROP('%s')", residue))
	if undropErr == nil {
		mustExec(t, ctx, db, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", residue))
		t.Fatalf("DOLT_UNDROP(%q) succeeded after `bd dolt clean-databases --purge-dropped` with stale == 0; residue was not purged", residue)
	}
}

func mustExec(t *testing.T, ctx context.Context, db *sql.DB, query string) {
	t.Helper()
	execCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(execCtx, query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
