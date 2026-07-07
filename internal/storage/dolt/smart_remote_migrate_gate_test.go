package dolt

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
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
