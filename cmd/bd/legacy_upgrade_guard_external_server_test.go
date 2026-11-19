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

// explicitServerWorkspace builds the provisioner shape from GH#5682
// (bd-xaq3hv): an existing explicit BEADS_DIR whose config.yaml selects server
// mode, with no witness and no local Dolt root yet. It returns the repo, the
// real .beads directory, and an alias of the repo reached through a symlinked
// parent, so a data directory spelled through the alias names the physical
// <beadsDir>/dolt while init's textual layout comparison (useLocalBeads) sees
// a different parent.
func explicitServerWorkspace(t *testing.T) (repoDir, beadsDir, aliasRepoDir string) {
	t.Helper()
	repoDir = t.TempDir()
	if resolved, err := filepath.EvalSymlinks(repoDir); err == nil {
		repoDir = resolved
	}
	initGitRepo(t, repoDir)
	beadsDir = filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(beadsDir, "config.yaml"), []byte("dolt.mode: server\n"))
	aliasRepoDir = filepath.Join(t.TempDir(), "repo-alias")
	if err := os.Symlink(repoDir, aliasRepoDir); err != nil {
		t.Skipf("symlinks unsupported here, alias coverage not exercised: %v", err)
	}
	return repoDir, beadsDir, aliasRepoDir
}

// TestExternalServerInitWritesWitnessWhenDataDirSpelledOutsideBeadsDir pins
// the writer/guard contract (GH#5682, bd-xaq3hv): a successful
// `bd init --server --external` into an existing explicit BEADS_DIR records the
// current-era witness even when the data directory is spelled outside that
// BEADS_DIR, so the later-populated local Dolt root is not refused as legacy.
func TestExternalServerInitWritesWitnessWhenDataDirSpelledOutsideBeadsDir(t *testing.T) {
	skipIfNoDolt(t)

	initAliased := func(t *testing.T) (repoDir, beadsDir string, env []string, database string) {
		t.Helper()
		repoDir, beadsDir, aliasRepoDir := explicitServerWorkspace(t)
		env = append(externalServerTestEnv(t),
			"BEADS_DIR="+beadsDir,
			"BEADS_DOLT_DATA_DIR="+filepath.Join(aliasRepoDir, ".beads", "dolt"))
		database = uniqueTestDBName(t)
		t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

		out, err := runExternalServerBD(t, repoDir, env, externalServerInitArgs(database)...)
		if err != nil {
			t.Fatalf("bd init --server --external with an aliased data dir failed: %v\n%s", err, out)
		}
		if info, statErr := os.Stat(filepath.Join(beadsDir, "dolt")); statErr != nil || !info.IsDir() {
			t.Fatalf("precondition: expected init to create the physical %s root: %v", filepath.Join(beadsDir, "dolt"), statErr)
		}
		return repoDir, beadsDir, env, database
	}

	t.Run("witness", func(t *testing.T) {
		_, beadsDir, _, _ := initAliased(t)
		assertCurrentVersionWitness(t, beadsDir)
		if _, err := os.Stat(filepath.Join(beadsDir, "metadata.json")); !os.IsNotExist(err) {
			t.Fatalf("init must leave the provisioner-owned layout alone; metadata.json stat err = %v", err)
		}
	})

	t.Run("reopen_after_root_populated", func(t *testing.T) {
		repoDir, beadsDir, env, database := initAliased(t)
		writeFile(t, filepath.Join(beadsDir, "dolt", "server-owned-entry"), []byte("x\n"))

		listEnv := append(env,
			"BEADS_DOLT_SERVER_HOST=127.0.0.1",
			fmt.Sprintf("BEADS_DOLT_SERVER_PORT=%d", testDoltServerPort),
			"BEADS_DOLT_SERVER_DATABASE="+database)
		out, err := runExternalServerBD(t, repoDir, listEnv, "list", "--json", "--all")
		if strings.Contains(out, "explicit migration is required") {
			t.Fatalf("bd list refused the workspace bd init just admitted:\n%s", out)
		}
		if err != nil {
			t.Fatalf("bd list after init failed: %v\n%s", err, out)
		}
	})

	t.Run("sibling_data_dir", func(t *testing.T) {
		repoDir, beadsDir, _ := explicitServerWorkspace(t)
		env := append(externalServerTestEnv(t),
			"BEADS_DIR="+beadsDir,
			"BEADS_DOLT_DATA_DIR="+filepath.Join(t.TempDir(), "dolt-data"))
		database := uniqueTestDBName(t)
		t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

		out, err := runExternalServerBD(t, repoDir, env, externalServerInitArgs(database)...)
		if err != nil {
			t.Fatalf("bd init --server --external with a sibling data dir failed: %v\n%s", err, out)
		}
		assertCurrentVersionWitness(t, beadsDir)
	})
}
