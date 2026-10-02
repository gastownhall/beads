//go:build cgo

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runExternalServerBD runs the bd binary under test in repoDir with env and
// returns its combined output.
func runExternalServerBD(t *testing.T, repoDir string, env []string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, buildBDUnderTest(t), args...)
	cmd.Dir = repoDir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if ctxErr := ctx.Err(); ctxErr != nil {
		t.Fatalf("bd %s did not finish before the deadline: %v\n%s", strings.Join(args, " "), ctxErr, out)
	}
	return string(out), err
}

// externalServerTestEnv isolates a bd child from the developer's home and
// config (a real ~/.beads may select a shared server) and from the ambient
// BEADS_DOLT_* endpoint this package's TestMain exports, so only the flags and
// files under test choose the target.
func externalServerTestEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	return append(envWithoutBeadsStorageSettings(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BD_DISABLE_METRICS=1",
		"BEADS_DOLT_AUTO_START=0",
	)
}

func externalServerInitArgs(database string, extra ...string) []string {
	args := []string{
		"init", "--quiet", "--server", "--external",
		"--server-host", "127.0.0.1",
		"--server-port", fmt.Sprintf("%d", testDoltServerPort),
		"--database", database,
		"--prefix", "ext",
		"--skip-hooks", "--skip-agents",
	}
	return append(args, extra...)
}

func assertCurrentVersionWitness(t *testing.T, beadsDir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(beadsDir, localVersionFile))
	if err != nil {
		t.Fatalf("expected %s after the command: %v", localVersionFile, err)
	}
	if got := strings.TrimSpace(string(data)); got != Version {
		t.Fatalf("%s = %q, want %q", localVersionFile, got, Version)
	}
}

// TestExternalServerInitWritesWitnessOverEmptyDoltRoot reproduces the
// provisioner shape: config.yaml already selects server mode and an empty
// .beads/dolt exists before `bd init --server --external` runs. The legacy
// guard used to refuse that init as a "legacy Dolt server workspace", so the
// scope never got a witness and every later command was refused too.
func TestExternalServerInitWritesWitnessOverEmptyDoltRoot(t *testing.T) {
	skipIfNoDolt(t)
	env := externalServerTestEnv(t)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(beadsDir, "config.yaml"), []byte("dolt.mode: server\n"))

	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	out, err := runExternalServerBD(t, repoDir, append(env, "BEADS_DIR="+beadsDir),
		externalServerInitArgs(database, "--init-if-missing")...)
	if err != nil {
		t.Fatalf("bd init --server --external over an empty .beads/dolt failed: %v\n%s", err, out)
	}
	assertCurrentVersionWitness(t, beadsDir)
}

// TestExternalServerWorkspaceRecoversMissingWitness covers a workspace already
// in the broken state: initialized against an external server, then left
// without .local_version (it is gitignored, so a fresh checkout or a cleanup
// drops it) while bd's own empty .beads/dolt remains. The next ordinary
// command must open the workspace and re-seed the witness with no manual
// steps.
func TestExternalServerWorkspaceRecoversMissingWitness(t *testing.T) {
	skipIfNoDolt(t)
	env := externalServerTestEnv(t)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	beadsDir := filepath.Join(repoDir, ".beads")

	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	out, err := runExternalServerBD(t, repoDir, env, externalServerInitArgs(database)...)
	if err != nil {
		t.Fatalf("bd init --server --external failed: %v\n%s", err, out)
	}
	assertCurrentVersionWitness(t, beadsDir)

	entries, err := os.ReadDir(filepath.Join(beadsDir, "dolt"))
	if err != nil {
		t.Fatalf("precondition: expected bd init to leave a .beads/dolt directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("precondition: expected an empty .beads/dolt for an external server, found %d entries", len(entries))
	}
	if err := os.Remove(filepath.Join(beadsDir, localVersionFile)); err != nil {
		t.Fatal(err)
	}

	out, err = runExternalServerBD(t, repoDir, env, "list", "--json", "--all")
	if err != nil {
		t.Fatalf("bd list on an external-server workspace without a witness failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "explicit migration is required") {
		t.Fatalf("bd list reported a legacy-upgrade refusal:\n%s", out)
	}
	assertCurrentVersionWitness(t, beadsDir)
}
