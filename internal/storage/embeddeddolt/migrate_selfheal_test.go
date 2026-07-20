//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests cover the migrate/bootstrap self-heal that finishes
// gastownhall/beads#4566. #4567 made the hand-commit recovery path
// (OpenForWorkingSetReconcile + bd dolt commit) able to open a store whose
// pending migration touches a dirty table. This suite proves the automatic
// path: when a migration pass is killed mid-flight, a plain retry converges to
// the latest schema with a clean working set and NO manual commit, because each
// numbered migration is now committed atomically as it applies.
//
// The fault is injected at a migration-step boundary via
// schema.SetMigrateStepFaultHookForTest, which models a process killed between
// operations (the working set is whatever the last completed SQL statement
// left) — the faithful shape of a SIGKILL/timeout that a supervisor restarts.
//
// Scope caveat: these repros call schema.MigrateUp directly, below the
// remote-migrate gate. On a remote-backed store the retry's open path re-runs
// the #4516 smart gate first, and a killed pass leaves the working-set cursor
// ahead of the remote, which routes to the undetermined verdict and blocks the
// unattended retry at RemoteMigrateGateError (a pre-existing gate limitation,
// not introduced here). "Plain retry converges" therefore holds for local
// stores and for gate-sanctioned paths, not for an unattended supervisor loop
// on a remote-backed store.

const selfHealSeedVersion = 48 // migrations 49..LatestVersion() form the killed jump

func requireEmbedded(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
}

// TestEmbeddedMigrateSelfHealsAfterMidPassFault_4566 is Repro A: pin a store
// below a multi-step jump, kill the pass right after a numbered migration
// applies (before the terminal commit), then prove a plain retry converges with
// a clean working set and no hand-commit.
//
// PRE-fix (per-step commit removed) this same test fails at the retry: MigrateUp
// returns *schema.DirtyTablesError because the killed pass left migration 49's
// ALTER (touching `issues`, which pending migrations 53/54 also touch)
// uncommitted, and the guard refuses to entangle it.
func TestEmbeddedMigrateSelfHealsAfterMidPassFault_4566(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, selfHealSeedVersion)

	// Attempt 1: kill the pass immediately after migration 49 applies.
	const killAfter = 49
	restore := schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, version int) error {
		if version == killAfter {
			return fmt.Errorf("injected fault: process killed after migration %d", killAfter)
		}
		return nil
	})
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	_, err := schema.MigrateUp(ctx, conn)
	closeConn()
	restore()
	if err == nil || !strings.Contains(err.Error(), "injected fault") {
		t.Fatalf("attempt 1 MigrateUp err = %v, want the injected fault", err)
	}

	// Attempt 2 (the supervisor's plain retry): no fault, no hand-commit.
	conn2, closeConn2 := openPinnedConn(t, ctx, dataDir)
	defer closeConn2()
	applied, err := schema.MigrateUp(ctx, conn2)
	if err != nil {
		// This is the PRE-fix failure: *schema.DirtyTablesError.
		var dirty *schema.DirtyTablesError
		if errors.As(err, &dirty) {
			t.Fatalf("retry MigrateUp returned *DirtyTablesError %v — the migrate path did NOT self-heal (this is the #4566 bug the fix removes)", dirty.Tables)
		}
		t.Fatalf("retry MigrateUp: %v", err)
	}
	t.Logf("retry applied %d migrations", applied)

	if v := currentMainVersion(t, ctx, conn2); v != schema.LatestVersion() {
		t.Fatalf("schema version after retry = %d, want latest %d", v, schema.LatestVersion())
	}
	if dirty := dirtyTableNames(t, ctx, conn2); len(dirty) != 0 {
		t.Fatalf("working set after retry is dirty: %v, want clean", dirty)
	}
}

// TestEmbeddedMigrateStepCommitLeavesUntouchedUserDirt_4566 is the selective-
// staging safety unit test: a per-step commit must commit only the migration's
// own tables, never pre-existing user writes to a table the migration does not
// touch.
func TestEmbeddedMigrateStepCommitLeavesUntouchedUserDirt_4566(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, selfHealSeedVersion)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	// `config` is touched by no migration in the 49..latest jump, so a user
	// write to it is pre-existing dirt on an untouched table — it must survive
	// the per-step commits unstaged and uncommitted.
	const dirtKey = "user-dirt-before-migrate"
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES (?, ?)", dirtKey, "1"); err != nil {
		t.Fatalf("insert user dirt into config: %v", err)
	}

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	if v := currentMainVersion(t, ctx, conn); v != schema.LatestVersion() {
		t.Fatalf("schema version = %d, want latest %d", v, schema.LatestVersion())
	}

	// The user's config row must still be in the working set...
	var working int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM config WHERE `key` = ?", dirtKey).Scan(&working); err != nil {
		t.Fatalf("count working config: %v", err)
	}
	if working != 1 {
		t.Fatalf("working config row count = %d, want 1 (per-step commits must not drop user dirt)", working)
	}
	// ...and must NOT have been swept into any migration commit.
	var committed int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM config AS OF 'HEAD' WHERE `key` = ?", dirtKey).Scan(&committed); err != nil {
		t.Fatalf("count committed config: %v", err)
	}
	if committed != 0 {
		t.Fatalf("committed config row count = %d, want 0 (per-step commit must not commit untouched user dirt)", committed)
	}
	// config must still show dirty in the working set.
	dirty := dirtyTableNames(t, ctx, conn)
	if !contains(dirty, "config") {
		t.Fatalf("dirty tables = %v, want to still include 'config'", dirty)
	}
}

// TestEmbeddedMigrateConvergesUnderConcurrentInterruptedRetries_4566 is Repro B:
// the concurrent retry race #4567 never tested. N independent stores each run a
// supervisor loop — open, MigrateUp, and on any error retry — while a shared
// fault hook kills a random fraction of numbered-migration steps. POST-fix every
// store converges to the latest schema with a clean working set and zero manual
// commits, and every committed inter-attempt state is clean.
//
// PRE-fix a killed attempt strands the pass's applied migrations uncommitted, so
// the next attempt trips *schema.DirtyTablesError before it can make progress
// and the store never converges (the loop exhausts its attempt budget).
func TestEmbeddedMigrateConvergesUnderConcurrentInterruptedRetries_4566(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	const (
		workers     = 4
		maxAttempts = 40
	)

	dirs := make([]string, workers)
	for i := range dirs {
		dirs[i] = seedMainSchemaAt(t, ctx, selfHealSeedVersion)
	}

	// Install the fault hook only AFTER seeding, so the baseline builds cleanly.
	// It fires on a random ~55% of numbered main-source steps (versions above
	// the ignored source's max, so only the killed 49..latest jump is
	// affected, never the dolt-ignored tail — the hook cannot tell sources
	// apart, and a kill inside the ignored tail strands the pass's backfill
	// dirt, which only the end-of-pass commit stages). A thread-safe RNG keeps
	// it safe for the concurrent workers. Progress is monotonic post-fix: a
	// step that faults was already committed, so each attempt advances.
	var rngMu sync.Mutex
	rng := rand.New(rand.NewSource(1))
	restore := schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, version int) error {
		if version <= schema.LatestIgnoredVersion() {
			return nil
		}
		rngMu.Lock()
		fire := rng.Float64() < 0.55
		rngMu.Unlock()
		if fire {
			return fmt.Errorf("injected fault after migration %d", version)
		}
		return nil
	})
	defer restore()

	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			errs[w] = runSupervisorLoop(ctx, dirs[w], maxAttempts)
		}(i)
	}
	wg.Wait()

	for w, err := range errs {
		if err != nil {
			t.Errorf("worker %d did not converge: %v", w, err)
		}
	}
	if t.Failed() {
		return
	}

	// Final verification on each store: latest + clean, from a fresh conn.
	for w, dir := range dirs {
		conn, closeConn := openPinnedConn(t, ctx, dir)
		if v := currentMainVersion(t, ctx, conn); v != schema.LatestVersion() {
			t.Errorf("worker %d final version = %d, want latest %d", w, v, schema.LatestVersion())
		}
		if dirty := dirtyTableNames(t, ctx, conn); len(dirty) != 0 {
			t.Errorf("worker %d final working set dirty: %v", w, dirty)
		}
		closeConn()
	}
}

// runSupervisorLoop emulates a supervisor: repeatedly open the store and run
// MigrateUp, retrying on any error, until the schema reaches the latest version
// with a clean working set. It asserts the committed state is never left dirty
// between attempts and never issues a manual bd-dolt-commit.
func runSupervisorLoop(ctx context.Context, dataDir string, maxAttempts int) error {
	for attempt := 0; attempt < maxAttempts; attempt++ {
		conn, cleanup, err := openConn(ctx, dataDir)
		if err != nil {
			return fmt.Errorf("attempt %d open: %w", attempt, err)
		}
		_, migErr := schema.MigrateUp(ctx, conn)

		v, verr := scalarVersion(ctx, conn)
		if verr != nil {
			cleanup()
			return fmt.Errorf("attempt %d read version: %w", attempt, verr)
		}
		dirty, derr := statusTables(ctx, conn)
		cleanup()
		if derr != nil {
			return fmt.Errorf("attempt %d read status: %w", attempt, derr)
		}

		if migErr == nil {
			if v != schema.LatestVersion() {
				return fmt.Errorf("attempt %d: MigrateUp succeeded at version %d, want latest %d", attempt, v, schema.LatestVersion())
			}
			if len(dirty) != 0 {
				return fmt.Errorf("attempt %d: converged version but working set dirty: %v", attempt, dirty)
			}
			return nil
		}

		// A killed attempt must never strand a *committed* migration mid-jump:
		// the working set the next attempt inherits is always committed-clean.
		if len(dirty) != 0 {
			return fmt.Errorf("attempt %d: killed pass left a DIRTY working set %v at version %d (per-step commit violated)", attempt, dirty, v)
		}
	}
	return fmt.Errorf("did not converge within %d attempts", maxAttempts)
}

// --- helpers ---

// seedMainSchemaAt creates a fresh embedded database whose main schema is
// genuinely migrated to version `at` and committed clean, so a later MigrateUp
// runs migrations at+1..latest as a real multi-step jump. Returns the dataDir.
func seedMainSchemaAt(t *testing.T, ctx context.Context, at int) string {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), ".beads", "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	boot, bootCleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "", "")
	if err != nil {
		t.Fatalf("OpenSQL boot: %v", err)
	}
	if _, err := boot.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS testdb"); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	_ = bootCleanup()

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	if _, err := schema.MigrateUpTo(ctx, conn, at); err != nil {
		t.Fatalf("MigrateUpTo(%d): %v", at, err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		t.Fatalf("DOLT_ADD -A: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'test: seed baseline schema')"); err != nil {
		t.Fatalf("DOLT_COMMIT baseline: %v", err)
	}
	return dataDir
}

func openConn(ctx context.Context, dataDir string) (*sql.Conn, func(), error) {
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		return nil, nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = cleanup()
		return nil, nil, err
	}
	return conn, func() { _ = conn.Close(); _ = cleanup() }, nil
}

func openPinnedConn(t *testing.T, ctx context.Context, dataDir string) (*sql.Conn, func()) {
	t.Helper()
	conn, cleanup, err := openConn(ctx, dataDir)
	if err != nil {
		t.Fatalf("open pinned conn: %v", err)
	}
	return conn, cleanup
}

func scalarVersion(ctx context.Context, conn *sql.Conn) (int, error) {
	var v int
	err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&v)
	return v, err
}

func currentMainVersion(t *testing.T, ctx context.Context, conn *sql.Conn) int {
	t.Helper()
	v, err := scalarVersion(ctx, conn)
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	return v
}

func statusTables(ctx context.Context, conn *sql.Conn) ([]string, error) {
	rows, err := conn.QueryContext(ctx, "SELECT table_name FROM dolt_status")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, rows.Err()
}

func dirtyTableNames(t *testing.T, ctx context.Context, conn *sql.Conn) []string {
	t.Helper()
	out, err := statusTables(ctx, conn)
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
