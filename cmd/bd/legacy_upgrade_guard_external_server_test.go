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

// aliasRepoThroughParent returns a path that reaches repoDir through a symlink
// in a separate temporary parent, so a data directory spelled beneath it names
// repoDir's physical layout without sharing its textual prefix. The
// useLocalBeads comparison in bd init cleans paths but does not resolve
// symlinks, so only this spelling difference turns it false while the data
// still physically lives under the workspace (GH#5682).
func aliasRepoThroughParent(t *testing.T, repoDir string) string {
	t.Helper()
	alias := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(repoDir, alias); err != nil {
		t.Skipf("symlinks are unsupported here, skipping aliased-path coverage: %v", err)
	}
	return alias
}

// provisionExplicitServerWorkspace creates an explicit BEADS_DIR holding a
// config.yaml that selects server mode, the shape a provisioner like gc sets
// up before handing the workspace to bd, with no metadata.json and no local
// dolt root.
func provisionExplicitServerWorkspace(t *testing.T, repoDir string) string {
	t.Helper()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(beadsDir, "config.yaml"), []byte("dolt.mode: server\n"))
	return beadsDir
}

// TestServerInitWithExplicitBeadsDirAndExternalDataDirWritesWitness pins the
// writer/guard contract from GH#5682 (bd-xaq3hv): `bd init --server` must leave
// a .local_version witness on every server-mode workspace it provisions, not
// only when the Dolt data directory sits directly under .beads (init's
// useLocalBeads predicate). With BEADS_DIR set explicitly and the data
// directory elsewhere, init used to succeed without a witness, and once a
// .beads/dolt root holds anything the legacy guard refuses every later command
// as a "legacy Dolt server workspace". The workspace directory must already
// exist: init never creates a provisioner-owned BEADS_DIR just to hold a
// witness (see TestExternalServerInitLeavesAbsentBeadsDirUntouched).
func TestServerInitWithExplicitBeadsDirAndExternalDataDirWritesWitness(t *testing.T) {
	skipIfNoDolt(t)
	env := externalServerTestEnv(t)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	beadsDir := provisionExplicitServerWorkspace(t, repoDir)
	dataDir := filepath.Join(t.TempDir(), "dolt-data")
	env = append(env, "BEADS_DIR="+beadsDir, "BEADS_DOLT_DATA_DIR="+dataDir)

	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	out, err := runExternalServerBD(t, repoDir, env, externalServerInitArgs(database)...)
	if err != nil {
		t.Fatalf("bd init --server --external with explicit BEADS_DIR failed: %v\n%s", err, out)
	}
	assertCurrentVersionWitness(t, beadsDir)
	// The witness is the layout change init owes this workspace; it must not
	// also start owning the provisioner's metadata layout.
	if _, statErr := os.Stat(filepath.Join(beadsDir, "metadata.json")); statErr == nil {
		t.Fatalf("bd init created metadata.json in a provisioner-owned explicit workspace")
	}
}

// TestExternalServerInitWritesWitnessWhenDataDirSpelledOutsideBeadsDir covers
// the aliased spelling from GH#5682: the data directory physically resolves to
// .beads/dolt but is spelled through a symlinked parent, so useLocalBeads is
// false and the witness write used to be skipped while init still admitted the
// workspace and created the physical root. The reopen subtest shows the
// consequence the witness prevents: a local root that later holds anything
// makes an ordinary command refuse the workspace as legacy.
func TestExternalServerInitWritesWitnessWhenDataDirSpelledOutsideBeadsDir(t *testing.T) {
	skipIfNoDolt(t)
	env := externalServerTestEnv(t)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	beadsDir := provisionExplicitServerWorkspace(t, repoDir)
	alias := aliasRepoThroughParent(t, repoDir)
	dataDir := filepath.Join(alias, ".beads", "dolt")
	env = append(env, "BEADS_DIR="+beadsDir, "BEADS_DOLT_DATA_DIR="+dataDir)

	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	out, err := runExternalServerBD(t, repoDir, env, externalServerInitArgs(database)...)
	if err != nil {
		t.Fatalf("bd init --server --external through an aliased data directory failed: %v\n%s", err, out)
	}

	t.Run("witness", func(t *testing.T) {
		assertCurrentVersionWitness(t, beadsDir)
		// The physical root exists, created through the alias; the witness
		// beside it is what keeps a later non-empty root admissible.
		if _, statErr := os.Stat(filepath.Join(beadsDir, "dolt")); statErr != nil {
			t.Errorf("expected the aliased data directory to create the physical dolt root: %v", statErr)
		}
		if _, statErr := os.Stat(filepath.Join(beadsDir, "metadata.json")); statErr == nil {
			t.Errorf("bd init created metadata.json in a provisioner-owned explicit workspace")
		}
	})

	t.Run("reopen-after-root-populated", func(t *testing.T) {
		// Simulate a managed server materializing data in the local root.
		writeFile(t, filepath.Join(beadsDir, "dolt", "population.marker"), []byte("server data\n"))
		listEnv := append(env,
			"BEADS_DOLT_SERVER_HOST=127.0.0.1",
			fmt.Sprintf("BEADS_DOLT_SERVER_PORT=%d", testDoltServerPort),
			"BEADS_DOLT_SERVER_DATABASE="+database,
		)
		out, err := runExternalServerBD(t, repoDir, listEnv, "list", "--json", "--all")
		if err != nil {
			t.Fatalf("bd list over a populated local root after init failed: %v\n%s", err, out)
		}
		if strings.Contains(out, "explicit migration is required") {
			t.Fatalf("bd list reported a legacy-upgrade refusal:\n%s", out)
		}
	})
}

// TestExternalServerInitRefusesPopulatedRootWithoutWitnessOutsideLayout pins
// the protection the widened writer must not weaken: a server workspace whose
// local dolt root already holds entries and carries no witness stays a legacy
// layout, and init must refuse it without writing the witness — only a
// current-era binary writing after its own admission gives the witness its
// meaning.
func TestExternalServerInitRefusesPopulatedRootWithoutWitnessOutsideLayout(t *testing.T) {
	skipIfNoDolt(t)
	env := externalServerTestEnv(t)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	beadsDir := provisionExplicitServerWorkspace(t, repoDir)
	writeFile(t, filepath.Join(beadsDir, "dolt", "leftover.txt"), []byte("historical data\n"))
	alias := aliasRepoThroughParent(t, repoDir)
	dataDir := filepath.Join(alias, ".beads", "dolt")
	env = append(env, "BEADS_DIR="+beadsDir, "BEADS_DOLT_DATA_DIR="+dataDir)

	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	out, err := runExternalServerBD(t, repoDir, env, externalServerInitArgs(database)...)
	if err == nil {
		t.Fatalf("bd init --server --external admitted a populated local root with no witness:\n%s", out)
	}
	if !strings.Contains(out, "explicit migration is required") {
		t.Fatalf("bd init did not report the explicit-migration refusal (err=%v):\n%s", err, out)
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, localVersionFile)); !os.IsNotExist(statErr) {
		t.Fatalf("refused init must leave %s absent, stat err=%v", localVersionFile, statErr)
	}
}

// TestExternalServerInitLeavesAbsentBeadsDirUntouched pins the ownership side
// of the widened witness writer: an explicit BEADS_DIR that does not exist is
// the provisioner's to create, so a successful init that puts its data
// elsewhere must leave the directory absent rather than create it just to
// hold a witness.
func TestExternalServerInitLeavesAbsentBeadsDirUntouched(t *testing.T) {
	skipIfNoDolt(t)
	env := externalServerTestEnv(t)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	beadsDir := filepath.Join(t.TempDir(), "absent-workspace")
	dataDir := filepath.Join(t.TempDir(), "dolt-data")
	env = append(env, "BEADS_DIR="+beadsDir, "BEADS_DOLT_DATA_DIR="+dataDir)

	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	out, err := runExternalServerBD(t, repoDir, env, externalServerInitArgs(database)...)
	if err != nil {
		t.Fatalf("bd init --server --external against an absent explicit workspace failed: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(beadsDir); !os.IsNotExist(statErr) {
		t.Fatalf("bd init must leave an absent explicit BEADS_DIR absent; stat err=%v", statErr)
	}
}
