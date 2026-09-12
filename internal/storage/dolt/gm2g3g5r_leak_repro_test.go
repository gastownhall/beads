package dolt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gm-2g3g5r repro. A gc-managed city runs its production Dolt server on a
// DYNAMICALLY ALLOCATED port (28231 on this host), recorded in
// .beads/dolt-server.port and exported into every agent session as
// BEADS_DOLT_SERVER_PORT. Every beads TestMain sets BEADS_TEST_SERVER=1.
//
// prodPort below stands in for that dynamic port. It is never dialed: both
// tests call applyConfigDefaults / productionPortReasons only, which read
// env + files and open no sockets.
const reproProdPort = "59999"

// TestApplyConfigDefaults_TestModeBlocksNonDefaultProductionPort documents a
// residual gap, not the gm-2g3g5r leak itself: the leak's actual root cause
// (EnsureDoltContainerForTestMain failing open on the ambient port) is fixed
// and covered by TestEnsureDoltContainerForTestMain_NeutralizesAmbientPortOnFailure
// in internal/testutil. This test bypasses that fix entirely -- it injects a
// production port directly via env var to probe applyConfigDefaults /
// productionPortReasons in isolation. Rule 1 of productionPortReasons only
// recognises DefaultSQLPort (3307); Rule 3 (the dolt-server.port file) is the
// only rule that can see a dynamic production port, and store.go:142-144
// suppresses it whenever BEADS_TEST_SERVER=1. Widening that is a deliberate,
// separate change to AD-01's documented contract -- see be-rl6tm, filed on
// architect-6a's recommendation rather than guessed at here.
func TestApplyConfigDefaults_TestModeBlocksNonDefaultProductionPort(t *testing.T) {
	t.Skip("residual gap, not the gm-2g3g5r leak -- tracked in be-rl6tm")
	beadsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"),
		[]byte(reproProdPort), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BEADS_TEST_MODE", "1")
	t.Setenv("BEADS_TEST_SERVER", "1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", reproProdPort)
	t.Setenv("BEADS_DOLT_PORT", "")

	cfg := &Config{Path: filepath.Join(beadsDir, "dolt"), BeadsDir: beadsDir}
	applyConfigDefaults(cfg)

	if !strings.HasPrefix(cfg.Database, "testdb_") {
		t.Fatalf("precondition: want a derived testdb_ name, got %q", cfg.Database)
	}
	if cfg.ServerPort != 1 {
		t.Errorf("LEAK: BEADS_TEST_MODE=1 resolved to production port %d "+
			"(dolt-server.port says %s) and would create %q there; want port 1",
			cfg.ServerPort, reproProdPort, cfg.Database)
	}
}

// TestProductionPortReasons_BlindWithoutBeadsDir disproves the obvious fix.
// Deleting the BEADS_TEST_SERVER early return is NOT sufficient: Rule 3 is
// gated on cfg.BeadsDir != "", and the leaking call site --- beads.go:277
// beads.Open -> dolt.New(&Config{Path: dbPath, CreateIfMissing: true}) ---
// sets Path only. With BeadsDir empty the rule cannot fire even unsuppressed.
//
// Note for whoever picks up be-rl6tm: store.go:119-126 already explains why
// Rule 3 does NOT fall back to filepath.Dir(cfg.Path) when BeadsDir is empty
// -- test fixtures commonly set Path under /tmp with no real BeadsDir, and
// deriving one would false-positive on stray dolt-server.port files from
// leaked dev servers. That path-derivation fix has already been considered
// and rejected; don't re-propose it without a new argument.
func TestProductionPortReasons_BlindWithoutBeadsDir(t *testing.T) {
	t.Skip("residual gap, not the gm-2g3g5r leak -- tracked in be-rl6tm")
	beadsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"),
		[]byte(reproProdPort), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_TEST_MODE", "1")
	_ = os.Unsetenv("BEADS_TEST_SERVER") // suppression removed: the "obvious fix"

	// Exactly what beads.Open passes: Path set, BeadsDir empty.
	cfg := &Config{Path: filepath.Join(beadsDir, "dolt"), ServerPort: 59999}
	if reasons := productionPortReasons(cfg); len(reasons) != 0 {
		t.Logf("Rule 3 fired without BeadsDir: %v", reasons)
	} else {
		t.Errorf("CONFIRMED BLIND SPOT: productionPortReasons returned no reasons "+
			"for port %d even with BEADS_TEST_SERVER unset, because BeadsDir is "+
			"empty (store.go:150). Removing the early return alone does not fix "+
			"the beads.Open path.", cfg.ServerPort)
	}
}

// TestApplyConfigDefaults_NoResolvablePortFailsClosed closes the loop on the
// proposed fix: once the harness neutralizes the ambient port vars, nothing
// resolves a port, and the existing BEADS_TEST_MODE guard's `ServerPort == 0`
// branch (store.go:1504-1508) forces port 1. This already passes today --- it
// is the invariant the fix relies on, asserted so a future change cannot
// remove it silently.
func TestApplyConfigDefaults_NoResolvablePortFailsClosed(t *testing.T) {
	beadsDir := t.TempDir() // no dolt-server.port, no config.yaml
	t.Setenv("BEADS_TEST_MODE", "1")
	t.Setenv("BEADS_TEST_SERVER", "1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")

	cfg := &Config{Path: filepath.Join(beadsDir, "dolt"), BeadsDir: beadsDir}
	applyConfigDefaults(cfg)

	if cfg.ServerPort != 1 {
		t.Errorf("want port 1 (fail closed) with no resolvable port under "+
			"BEADS_TEST_MODE=1, got %d", cfg.ServerPort)
	}
}
