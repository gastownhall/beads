//go:build cgo

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
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

	// A fresh clone whose git remote carries Dolt data, with the empty embedded
	// skeleton `bd prime` auto-creates before hydration.
	cloneDir, _, _ := primeSkeletonForTest(t, bd)

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

// TestEmbeddedBootstrapKeepsUnprovenSiblingDatabase pins the guard on the one
// destructive act in the #5915 fix. embeddeddolt/ is a Dolt multi-database
// directory and the emptiness proof covers only the configured database, so
// when the skeleton shares that directory with any other database, bootstrap
// must refuse rather than remove the directory wholesale, and both must survive.
func TestEmbeddedBootstrapKeepsUnprovenSiblingDatabase(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	cloneDir, dataDir, dbName := primeSkeletonForTest(t, bd)
	sibling := filepath.Join(dataDir, "otherdb")
	if err := os.MkdirAll(sibling, 0o750); err != nil {
		t.Fatalf("create sibling database directory: %v", err)
	}

	out, err := bdBootstrapAllowError(t, bd, cloneDir, "--yes")
	if err == nil || !strings.Contains(out, "refusing to remove embedded skeleton") || !strings.Contains(out, "otherdb") {
		t.Fatalf("bootstrap did not refuse a skeleton sharing embeddeddolt/ with an unproven database (err=%v); output:\n%s", err, out)
	}
	for _, dir := range []string{filepath.Join(dataDir, dbName), sibling} {
		if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
			t.Fatalf("refused bootstrap still removed %s (err=%v); output:\n%s", dir, statErr, out)
		}
	}
}

// TestEmbeddedBootstrapHealthyWorkspaceSkipsRemoteProbe pins the order of the
// #5915 gate. Every `bd bootstrap` over a healthy embedded workspace reaches the
// gate, and in the configuration `bd init` writes, sync.remote is a git-forge
// URL whose check is a `git ls-remote`. The local emptiness proof runs first and
// rules out a healthy workspace, so the remote is never probed.
func TestEmbeddedBootstrapHealthyWorkspaceSkipsRemoteProbe(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "hot")
	bdCreate(t, bd, dir, "A real issue", "--type", "task")

	snapshotBootstrapEnv(t)
	t.Cleanup(setupSyncRemoteConfig(t, beadsDir, "https://github.com/org/repo.git"))
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	probes := 0
	stubProbeGitRemoteDoltData(t, func(string) (bool, error) {
		probes++
		return true, nil
	})
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load metadata.json: cfg=%v err=%v", cfg, err)
	}

	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "none" {
		t.Fatalf("action=%q, want %q for a healthy workspace (GH#5037)", plan.Action, "none")
	}
	if probes != 0 {
		t.Errorf("the remote was probed %d time(s) for a healthy workspace; the emptiness proof must rule it out first", probes)
	}
}

// TestEmbeddedDBIsEmpty covers the emptiness probe that gates #5915 hydration.
// The deliberate contract: return true ONLY for a readable, UNIDENTIFIED
// embedded database that provably holds no user work (the pre-hydration `bd
// prime` skeleton); return false for an initialized workspace (one carrying an
// issue_prefix / _project_id even with zero issues), a skeleton that has taken
// any user write, a populated database, a bare/non-Dolt directory, and a missing
// directory. Because the caller deletes the whole database before re-cloning,
// the false cases are what keep the GH#5037 "already exists, nothing to do"
// contract from turning into data loss.
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

	// One skeleton, copied per case below so every case starts from a database
	// the proof has just accepted and differs from it by exactly one write.
	skelDir, skelDataDir, dbName := primeSkeletonForTest(t, bd)

	t.Run("unidentified_skeleton_is_empty", func(t *testing.T) {
		// The one true case: the empty embedded skeleton `bd prime` auto-creates
		// on a fresh clone before hydration — a migrated schema with no bootstrap
		// markers and no user rows. This is the exact database the fix must be
		// willing to discard and re-clone over. It is also the drift guard for
		// skeletonSeededConfig and skeletonBookkeepingTables: a migration that
		// seeds a new row fails here, and the reason names it.
		if why := embeddedDBUserWork(skelDataDir, dbName); why != "" {
			t.Errorf("embeddedDBUserWork(%q, %q) = %q, want \"\" for an unidentified pre-hydration skeleton", skelDataDir, dbName, why)
		}
	})

	// Regression for the beads#6102 r2 review: `bd remember`, `bd kv set` and
	// `bd config set` all succeed on the unidentified skeleton and write to
	// config, and the old proof (identity markers plus six named tables) still
	// judged it empty, so bootstrap deleted the data. The SQL cases put a row
	// straight into a table on the skeleton, so they reach the table check
	// rather than stopping at a missing identity marker. local_metadata was
	// never in the old list: it stands for any table nobody thought to name.
	userWork := []struct {
		name    string
		bdArgs  []string // a bd command run in the copied clone, or
		sql     string   // a statement run against the copied database
		wantWhy string   // must appear in embeddedDBUserWork's reason
	}{
		{name: "bd_remember", bdArgs: []string{"remember", "precious local note"}, wantWhy: `"kv.memory.`},
		{name: "bd_kv_set", bdArgs: []string{"kv", "set", "mykey", "myval"}, wantWhy: `"kv.mykey"`},
		{name: "bd_config_set_new_key", bdArgs: []string{"config", "set", "custom.setting", "v"}, wantWhy: `"custom.setting"`},
		{name: "bd_config_set_changed_default", bdArgs: []string{"config", "set", "compact_batch_size", "7"}, wantWhy: `"compact_batch_size"`},
		// r3: these three verbs write no table row, only a Dolt remote or branch.
		{name: "bd_dolt_remote_add", bdArgs: []string{"dolt", "remote", "add", "extra", "file:///tmp/probe-remote"}, wantWhy: `dolt remote "extra"`},
		{name: "bd_federation_add_peer", bdArgs: []string{"federation", "add-peer", "peer1", "file:///tmp/peer1"}, wantWhy: `dolt remote "peer1"`},
		{name: "bd_branch", bdArgs: []string{"branch", "feature"}, wantWhy: `dolt branch "feature"`},
		{name: "issue_row", sql: "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes) VALUES ('hyd-zz1', 't', '', '', '', '')", wantWhy: "table issues"},
		{name: "project_id_in_metadata", sql: "INSERT INTO metadata (`key`, value) VALUES ('_project_id', 'p')", wantWhy: "table metadata"},
		{name: "route_row", sql: "INSERT INTO routes (prefix, path) VALUES ('zz', '/tmp/zz')", wantWhy: "table routes"},
		{name: "local_metadata_row", sql: "INSERT INTO local_metadata (`key`, value) VALUES ('k', 'v')", wantWhy: "table local_metadata"},
	}
	for _, tc := range userWork {
		t.Run("skeleton_with_"+tc.name+"_is_not_empty", func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "clone")
			if err := os.CopyFS(dir, os.DirFS(skelDir)); err != nil {
				t.Fatalf("copy skeleton workspace: %v", err)
			}
			dataDir := filepath.Join(dir, ".beads", "embeddeddolt")
			if why := embeddedDBUserWork(dataDir, dbName); why != "" {
				t.Fatalf("precondition: the copied skeleton is not empty before the write: %s", why)
			}

			if tc.bdArgs != nil {
				bdRunOK(t, bd, dir, tc.bdArgs...)
			} else {
				execSkeletonSQL(t, dataDir, dbName, tc.sql)
			}

			why := embeddedDBUserWork(dataDir, dbName)
			if why == "" {
				t.Fatalf("embeddedDBUserWork = \"\" after %s, want the write reported as user work", tc.name)
			}
			if !strings.Contains(why, tc.wantWhy) {
				t.Errorf("embeddedDBUserWork = %q, want a reason naming %s", why, tc.wantWhy)
			}
		})
	}

	t.Run("initialized_database_is_not_empty", func(t *testing.T) {
		// Regression for the beads#5915 review: a `bd init --prefix` workspace
		// has zero issues but IS identified (issue_prefix in config, _project_id
		// in metadata). Judging it empty would let bootstrap delete a real
		// workspace — the GH#5037 "nothing to do" contract, but with data loss.
		// (An earlier revision of this test wrongly asserted this DB was empty;
		// that assertion was the bug the reviewer caught.)
		_, beadsDir, _ := bdInit(t, bd, "--prefix", "emp")
		dataDir, dbName := embeddedDBName(t, beadsDir)
		if embeddedDBIsEmpty(dataDir, dbName) {
			t.Errorf("embeddedDBIsEmpty(%q, %q) = true, want false for an identified 0-issue workspace", dataDir, dbName)
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

// execSkeletonSQL runs one statement against the embedded database dbName in
// dataDir, the way a bd command would write to it.
func execSkeletonSQL(t *testing.T, dataDir, dbName, query string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, dbName, "main")
	if err != nil {
		t.Fatalf("open embedded database: %v", err)
	}
	defer func() { _ = cleanup() }()
	if _, err := db.ExecContext(ctx, query); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// primeSkeletonForTest builds the exact pre-hydration state #5915 is about: a
// publisher seeds one issue and pushes Dolt data to a bare git remote, a fresh
// clone inherits the git-committed .beads config, and the SessionStart `bd prime`
// hook auto-creates an empty embedded skeleton before anyone hydrates. It returns
// the clone directory and the (dataDir, dbName) of that skeleton — a migrated
// schema with no bootstrap markers and no user rows.
func primeSkeletonForTest(t *testing.T, bd string) (cloneDir, dataDir, dbName string) {
	t.Helper()

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
	cloneDir = filepath.Join(t.TempDir(), "clone")
	runGitForBootstrapTest(t, "", "clone", remoteURL, cloneDir)

	// Simulate the SessionStart hook: `bd prime` auto-creates the empty skeleton.
	// Lift prime's 10s store-open deadline: a -race bd (CI's embedded shards)
	// can need longer to migrate a fresh database, and a prime that gives up
	// mid-migration leaves dirty, half-migrated tables that every later bd
	// command refuses to open. The skeleton under test is the migrated one.
	primeCmd := exec.Command(bd, "prime")
	primeCmd.Dir = cloneDir
	primeCmd.Env = append(bdEnv(cloneDir), primeStoreTimeoutEnv+"=5m")
	_, _ = primeCmd.CombinedOutput() // prime is best-effort; assert its effect next.

	dataDir = filepath.Join(cloneDir, ".beads", "embeddeddolt")
	if info, err := os.Stat(dataDir); err != nil || !info.IsDir() {
		t.Fatalf("precondition not met: `bd prime` did not create an embedded skeleton at %s (err=%v)", dataDir, err)
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("read embeddeddolt dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			return cloneDir, dataDir, e.Name()
		}
	}
	t.Fatalf("no database subdirectory under %s", dataDir)
	return "", "", ""
}
