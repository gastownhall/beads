//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// TestInitServerFlagAdmitsEmptyDoltRootWithoutConfig reproduces the retry shape
// with nothing on disk that records the server choice: an empty .beads/dolt and
// neither config.yaml nor metadata.json, so the --server flag is the only place
// the intent lives. The legacy guard used to refuse that init as a legacy Dolt
// workspace, so the workspace was never created and no bd command could repair
// it.
func TestInitServerFlagAdmitsEmptyDoltRootWithoutConfig(t *testing.T) {
	skipIfNoDolt(t)
	env := externalServerTestEnv(t)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
		t.Fatal(err)
	}

	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	out, err := runExternalServerBD(t, repoDir, env, externalServerInitArgs(database)...)
	if err != nil {
		t.Fatalf("bd init --server over an empty .beads/dolt failed: %v\n%s", err, out)
	}
	assertCurrentVersionWitness(t, beadsDir)
}

// TestInitServerFlagRetryAfterFailedFirstAttempt replays the reported sequence: a
// first `bd init --server` that cannot reach a server fails after creating the
// local Dolt root, leaving an empty .beads/dolt and no witness, and the same
// command run again once the server is up must succeed instead of being refused
// as a legacy workspace over the root the first attempt made itself.
func TestInitServerFlagRetryAfterFailedFirstAttempt(t *testing.T) {
	skipIfNoDolt(t)
	env := externalServerTestEnv(t)
	// The first attempt has to fail without starting a server of its own: one
	// that started and then failed would leave a populated .beads/dolt, which
	// the guard refuses by design. externalServerTestEnv pins
	// BEADS_DOLT_AUTO_START=0 for exactly that, which keeps the attempt
	// deterministic even once init's separate --external handling stops trying
	// to start a per-project server. Fail here, naming the reason, if a change to
	// that helper drops it.
	if !slices.Contains(env, "BEADS_DOLT_AUTO_START=0") {
		t.Fatal("externalServerTestEnv must set BEADS_DOLT_AUTO_START=0: the failed first attempt must not start a server of its own")
	}

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	beadsDir := filepath.Join(repoDir, ".beads")

	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	out, err := runExternalServerBD(t, repoDir, env,
		externalServerInitArgs(database, "--server-port", strconv.Itoa(freeLoopbackPort(t)))...)
	if err == nil {
		t.Fatalf("precondition: bd init --server against an unreachable server succeeded:\n%s", out)
	}
	// The retry only has something to fix if the failed attempt left exactly
	// this. Any other leftover is a finding about init, not a reason to loosen
	// what follows.
	entries, readErr := os.ReadDir(filepath.Join(beadsDir, "dolt"))
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("precondition: the failed first attempt should leave an empty .beads/dolt, found %d entries (err %v)", len(entries), readErr)
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, localVersionFile)); !os.IsNotExist(statErr) {
		t.Fatalf("precondition: the failed first attempt should leave no %s (stat err %v)", localVersionFile, statErr)
	}

	out, err = runExternalServerBD(t, repoDir, env, externalServerInitArgs(database)...)
	if err != nil {
		t.Fatalf("retrying bd init --server over the empty .beads/dolt the first attempt left failed: %v\n%s", err, out)
	}
	assertCurrentVersionWitness(t, beadsDir)
}
