//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedBootstrapHydratesEmptyPrimeSkeleton is the regression for
// beads#5915: on a fresh clone whose git remote carries Dolt data
// (refs/dolt/data), the SessionStart `bd prime` hook auto-creates an empty
// embedded Dolt skeleton before anyone hydrates. Before the fix, bootstrap saw
// the non-empty embeddeddolt DIRECTORY and reported "Database already exists —
// Nothing to do" (exit 0), stranding the workspace with zero issues. The fix
// recognizes that the skeleton holds zero issues, so bootstrap clones from the
// remote instead and the seeded issue appears.
func TestEmbeddedBootstrapHydratesEmptyPrimeSkeleton(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// Publisher: init, seed one issue, and push Dolt data to a bare git remote.
	// The .beads config files are git-committed so the clone inherits them,
	// which is what lets `bd prime` create an embedded skeleton before bootstrap.
	bareDir := filepath.Join(t.TempDir(), "origin.git")
	runGitForBootstrapTest(t, "", "init", "--bare", "--initial-branch=main", bareDir)
	remoteURL := "file://" + bareDir

	sourceDir := t.TempDir()
	initGitRepoAt(t, sourceDir)
	runGitForBootstrapTest(t, sourceDir, "branch", "-M", "main")
	runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", remoteURL)
	// `bd init` auto-commits the .beads workspace (config.yaml, metadata.json)
	// and gitignores embeddeddolt/, so pushing that commit is what carries the
	// config to the clone. bdCreate writes to Dolt, not the git tree.
	runBDInit(t, bd, sourceDir, "--prefix", "hyd", "--skip-hooks", "--skip-agents")
	bdCreate(t, bd, sourceDir, "Seed remote data", "--type", "task")
	runGitForBootstrapTest(t, sourceDir, "push", "-u", "origin", "main")
	bdDolt(t, bd, sourceDir, "push")

	// Fresh clone: inherits the committed .beads config but has no database yet.
	cloneParent := t.TempDir()
	cloneDir := filepath.Join(cloneParent, "clone")
	runGitForBootstrapTest(t, "", "clone", remoteURL, cloneDir)

	// Simulate the SessionStart hook: `bd prime` auto-creates the empty skeleton.
	primeCmd := exec.Command(bd, "prime")
	primeCmd.Dir = cloneDir
	primeCmd.Env = bdEnv(cloneDir)
	_, _ = primeCmd.CombinedOutput() // prime is best-effort; assert its effect next.

	embeddedDir := filepath.Join(cloneDir, ".beads", "embeddeddolt")
	if info, err := os.Stat(embeddedDir); err != nil || !info.IsDir() {
		t.Fatalf("precondition not met: `bd prime` did not create an embedded skeleton at %s (err=%v)", embeddedDir, err)
	}

	// The fix: bootstrap must plan a clone (not "Nothing to do") and hydrate.
	out := bdBootstrap(t, bd, cloneDir, "--yes")
	if strings.Contains(out, "Nothing to do") || !strings.Contains(out, "clone from remote") {
		t.Fatalf("bootstrap did not hydrate the empty skeleton (#5915); want a clone plan, got:\n%s", out)
	}

	listed := bdList(t, bd, cloneDir)
	if !strings.Contains(listed, "Seed remote data") {
		t.Fatalf("hydrated workspace is missing the seeded issue; bd list:\n%s", listed)
	}
}

// TestEmbeddedDBIsEmpty covers the emptiness probe that gates #5915 hydration.
// The deliberate contract: return true only for a readable embedded database
// with zero issues; return false for a populated database, a bare/non-Dolt
// directory, and a missing directory — so bootstrap only ever re-clones over a
// local database it can prove holds no issue data.
func TestEmbeddedDBIsEmpty(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)

	// embeddedDBName returns the single database subdirectory name that bd
	// creates under .beads/embeddeddolt (the on-disk database name to USE).
	embeddedDBName := func(t *testing.T, beadsDir string) (string, string) {
		t.Helper()
		dataDir := filepath.Join(beadsDir, "embeddeddolt")
		entries, err := os.ReadDir(dataDir)
		if err != nil {
			t.Fatalf("read embeddeddolt dir: %v", err)
		}
		for _, e := range entries {
			if e.IsDir() {
				return dataDir, e.Name()
			}
		}
		t.Fatalf("no database subdirectory under %s", dataDir)
		return "", ""
	}

	t.Run("empty_database_is_empty", func(t *testing.T) {
		_, beadsDir, _ := bdInit(t, bd, "--prefix", "emp")
		dataDir, dbName := embeddedDBName(t, beadsDir)
		if !embeddedDBIsEmpty(dataDir, dbName) {
			t.Errorf("embeddedDBIsEmpty(%q, %q) = false, want true for a freshly initialized 0-issue database", dataDir, dbName)
		}
	})

	t.Run("populated_database_is_not_empty", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pop")
		bdCreate(t, bd, dir, "A real issue", "--type", "task")
		dataDir, dbName := embeddedDBName(t, beadsDir)
		if embeddedDBIsEmpty(dataDir, dbName) {
			t.Errorf("embeddedDBIsEmpty(%q, %q) = true, want false when the database holds an issue", dataDir, dbName)
		}
	})

	t.Run("bare_directory_is_not_empty", func(t *testing.T) {
		// A directory that is not a valid embedded Dolt database (OpenSQL
		// errors) must be treated as non-empty so bootstrap leaves it alone.
		dataDir := filepath.Join(t.TempDir(), "embeddeddolt")
		if err := os.MkdirAll(filepath.Join(dataDir, "mydb"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, "mydb", ".keep"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if embeddedDBIsEmpty(dataDir, "mydb") {
			t.Errorf("embeddedDBIsEmpty on a bare non-Dolt directory = true, want false")
		}
	})

	t.Run("missing_directory_is_not_empty", func(t *testing.T) {
		if embeddedDBIsEmpty(filepath.Join(t.TempDir(), "does-not-exist"), "beads") {
			t.Errorf("embeddedDBIsEmpty on a missing directory = true, want false")
		}
	})
}
