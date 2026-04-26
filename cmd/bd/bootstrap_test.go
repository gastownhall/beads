//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

func snapshotBootstrapEnv(t *testing.T) func() {
	t.Helper()
	saved := make(map[string]string)
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "BD_") || strings.HasPrefix(env, "BEADS_") {
			parts := strings.SplitN(env, "=", 2)
			key := parts[0]
			saved[key] = os.Getenv(key)
			_ = os.Unsetenv(key)
		}
	}
	return func() {
		for _, env := range os.Environ() {
			if strings.HasPrefix(env, "BD_") || strings.HasPrefix(env, "BEADS_") {
				parts := strings.SplitN(env, "=", 2)
				_ = os.Unsetenv(parts[0])
			}
		}
		for key, val := range saved {
			_ = os.Setenv(key, val)
		}
	}
}

func TestDetectBootstrapAction_NoneWhenDatabaseExists(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Create embeddeddolt directory with content so it's detected as existing.
	// Default config uses embedded mode, so the detection logic looks for
	// beadsDir/embeddeddolt (not beadsDir/dolt).
	embeddedDir := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.MkdirAll(filepath.Join(embeddedDir, "beads"), 0o750); err != nil {
		t.Fatal(err)
	}

	// Run from tmpDir so auto-detect doesn't find parent git repo
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "none" {
		t.Errorf("action = %q, want %q", plan.Action, "none")
	}
	if !plan.HasExisting {
		t.Error("HasExisting = false, want true")
	}
}

func TestDetectBootstrapAction_RestoreWhenBackupExists(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	backupDir := filepath.Join(beadsDir, "backup")
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "issues.jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run from tmpDir so auto-detect doesn't find parent git repo
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "restore" {
		t.Errorf("action = %q, want %q", plan.Action, "restore")
	}
	if plan.BackupDir != backupDir {
		t.Errorf("BackupDir = %q, want %q", plan.BackupDir, backupDir)
	}
}

func TestDetectBootstrapAction_InitWhenNothingExists(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Run from the tmpDir so auto-detect doesn't find a git repo
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "init" {
		t.Errorf("action = %q, want %q", plan.Action, "init")
	}
}

func TestNoWorkspaceBootstrapPayload(t *testing.T) {
	payload := noWorkspaceBootstrapPayload()

	if got := payload["action"]; got != "none" {
		t.Fatalf("action = %v, want %q", got, "none")
	}
	if got := payload["reason"]; got != activeWorkspaceNotFoundError() {
		t.Fatalf("reason = %v, want %q", got, activeWorkspaceNotFoundError())
	}
	if got := payload["suggestion"]; got != diagHint() {
		t.Fatalf("suggestion = %v, want %q", got, diagHint())
	}
}

func TestDetectBootstrapAction_ServerModeMissingConfiguredDBDoesNotReturnNone(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	sharedDir := filepath.Join(tmpDir, "shared-dolt")
	if err := os.MkdirAll(filepath.Join(sharedDir, "hq"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedDir, "dolt-server.port"), []byte("3311"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "project_missing"
	cfg.DoltDataDir = sharedDir
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_DOLT_DATA_DIR", sharedDir)

	origCheck := checkBootstrapServerDB
	checkBootstrapServerDB = func(probeCfg bootstrapServerProbeConfig) bootstrapServerDBCheck {
		if probeCfg.database != "project_missing" {
			t.Fatalf("unexpected dbName: %s", probeCfg.database)
		}
		if probeCfg.port != 3311 {
			t.Fatalf("expected resolved server port 3311, got %d", probeCfg.port)
		}
		return bootstrapServerDBCheck{Exists: false, Reachable: true}
	}
	defer func() { checkBootstrapServerDB = origCheck }()

	plan := detectBootstrapAction(beadsDir, cfg)
	if plan.Action == "none" {
		t.Fatalf("expected bootstrap to continue recovery when configured server DB is missing, got plan %#v", plan)
	}
	if plan.Action != "init" {
		t.Fatalf("expected init fallback when no remote/backup/jsonl exists, got %q", plan.Action)
	}
}

func TestDetectBootstrapAction_ServerModeProbeErrorStopsWithReason(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	sharedDir := filepath.Join(tmpDir, "shared-dolt")
	if err := os.MkdirAll(filepath.Join(sharedDir, "hq"), 0o750); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "project_missing"
	cfg.DoltDataDir = sharedDir
	t.Setenv("BEADS_DOLT_DATA_DIR", sharedDir)

	origCheck := checkBootstrapServerDB
	checkBootstrapServerDB = func(probeCfg bootstrapServerProbeConfig) bootstrapServerDBCheck {
		return bootstrapServerDBCheck{Reachable: true, Err: fmt.Errorf("permission denied")}
	}
	defer func() { checkBootstrapServerDB = origCheck }()

	plan := detectBootstrapAction(beadsDir, cfg)
	if plan.Action != "none" {
		t.Fatalf("expected bootstrap to stop when server probe errors, got %#v", plan)
	}
	if !strings.Contains(plan.Reason, "permission denied") {
		t.Fatalf("expected probe error in plan reason, got %#v", plan)
	}
}

func TestCheckBootstrapServerDB_HonorsTLSFlagInDSN(t *testing.T) {
	probeCfg := bootstrapServerProbeConfig{
		host:     "127.0.0.1",
		port:     1,
		user:     "root",
		database: "beads",
		tls:      true,
	}

	result := checkBootstrapServerDB(probeCfg)
	if result.Reachable {
		t.Fatal("expected unreachable test connection")
	}
	if result.Err == nil {
		t.Fatal("expected connection error for unreachable test host")
	}
}

func TestDetectBootstrapAction_SyncWhenOriginHasDoltRef(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	// Create a bare repo with a refs/dolt/data ref
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGitForBootstrapTest(t, "", "init", "--bare", bareDir)

	// Create a source repo, commit, push, then create the dolt ref
	sourceDir := t.TempDir()
	runGitForBootstrapTest(t, sourceDir, "init", "-b", "main")
	runGitForBootstrapTest(t, sourceDir, "config", "user.email", "test@test.com")
	runGitForBootstrapTest(t, sourceDir, "config", "user.name", "Test User")
	runGitForBootstrapTest(t, sourceDir, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", bareDir)
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "HEAD:refs/dolt/data")
	// Create refs/dolt/data by pushing HEAD to that ref
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "HEAD:refs/dolt/data")

	// Create a "clone" repo with origin pointing at the bare repo
	cloneDir := t.TempDir()
	runGitForBootstrapTest(t, cloneDir, "init", "-b", "main")
	runGitForBootstrapTest(t, cloneDir, "remote", "add", "origin", bareDir)

	beadsDir := filepath.Join(cloneDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(cloneDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "sync" {
		t.Errorf("action = %q, want %q", plan.Action, "sync")
	}
	if plan.SyncRemote == "" {
		t.Error("SyncRemote is empty, expected git+ prefixed URL")
	}
}

func TestDetectBootstrapAction_ExplicitSyncRemotePreservesRemotesAPIURL(t *testing.T) {
	restore := snapshotBootstrapEnv(t)
	defer restore()
	config.ResetForTesting()
	defer config.ResetForTesting()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	const syncRemote = "http://myserver:7007/mydb"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("sync.remote: "+syncRemote+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize failed: %v", err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "sync" {
		t.Errorf("action = %q, want %q", plan.Action, "sync")
	}
	if plan.SyncRemote != syncRemote {
		t.Errorf("SyncRemote = %q, want unnormalized explicit sync.remote %q", plan.SyncRemote, syncRemote)
	}
}

func TestDetectBootstrapAction_InitWhenOriginHasNoDoltRef(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	// Create a bare repo without refs/dolt/data
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGitForBootstrapTest(t, "", "init", "--bare", bareDir)

	cloneDir := t.TempDir()
	runGitForBootstrapTest(t, cloneDir, "init", "-b", "main")
	runGitForBootstrapTest(t, cloneDir, "remote", "add", "origin", bareDir)

	beadsDir := filepath.Join(cloneDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(cloneDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "init" {
		t.Errorf("action = %q, want %q (no dolt ref on origin)", plan.Action, "init")
	}
}

func runGitForBootstrapTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(output))
	}
}

// TestBootstrapFreshCloneDetectsRemote verifies that when .beads does NOT
// exist but origin has refs/dolt/data, the bootstrap handler's remote-probe
// logic synthesizes beadsDir and detectBootstrapAction produces a "sync"
// plan instead of the handler exiting with "No .beads directory found".
// This is the core fix for GH#2792.
func TestBootstrapFreshCloneDetectsRemote(t *testing.T) {
	// Create a bare repo and push a fake refs/dolt/data ref to it.
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGitForBootstrapTest(t, "", "init", "--bare", bareDir)

	sourceDir := t.TempDir()
	runGitForBootstrapTest(t, sourceDir, "init", "-b", "main")
	runGitForBootstrapTest(t, sourceDir, "config", "user.email", "test@test.com")
	runGitForBootstrapTest(t, sourceDir, "config", "user.name", "Test User")
	runGitForBootstrapTest(t, sourceDir, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", bareDir)
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "HEAD:refs/dolt/data")
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "HEAD:refs/dolt/data")

	// Clone into a fresh directory — no .beads exists.
	cloneDir := t.TempDir()
	runGitForBootstrapTest(t, cloneDir, "init", "-b", "main")
	runGitForBootstrapTest(t, cloneDir, "remote", "add", "origin", bareDir)

	// Verify .beads does NOT exist.
	beadsDir := filepath.Join(cloneDir, ".beads")
	if _, err := os.Stat(beadsDir); err == nil {
		t.Fatal(".beads should not exist before bootstrap")
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(cloneDir); err != nil {
		t.Fatal(err)
	}

	// Replicate the Run handler's remote-probe logic: when beadsDir is
	// empty, check origin for refs/dolt/data and synthesize beadsDir.
	// This exercises the same code path the handler uses before calling
	// detectBootstrapAction.
	if !isGitRepo() {
		t.Fatal("expected to be in a git repo")
	}
	originURL, err := gitOriginGetURL()
	if err != nil || originURL == "" {
		t.Fatalf("expected origin URL, got err=%v url=%q", err, originURL)
	}
	if !gitOriginHasDoltDataRef() {
		t.Fatal("expected origin to have refs/dolt/data")
	}

	// Synthesize beadsDir the same way the handler does, then feed it
	// through detectBootstrapAction — the single code path for plan building.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	synthesizedDir := filepath.Join(cwd, ".beads")
	cfg := configfile.DefaultConfig()
	plan := detectBootstrapAction(synthesizedDir, cfg)

	if plan.Action != "sync" {
		t.Errorf("action = %q, want %q", plan.Action, "sync")
	}
	if plan.SyncRemote == "" {
		t.Error("SyncRemote should not be empty")
	}
	if plan.BeadsDir != synthesizedDir {
		t.Errorf("BeadsDir = %q, want %q", plan.BeadsDir, synthesizedDir)
	}
}

// TestBootstrapFreshCloneNoRemoteData verifies that when .beads does NOT exist
// and origin has NO refs/dolt/data, bootstrap correctly reports no data found
// (does not create .beads or crash).
func TestBootstrapFreshCloneNoRemoteData(t *testing.T) {
	// Create a bare repo WITHOUT refs/dolt/data.
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGitForBootstrapTest(t, "", "init", "--bare", bareDir)

	cloneDir := t.TempDir()
	runGitForBootstrapTest(t, cloneDir, "init", "-b", "main")
	runGitForBootstrapTest(t, cloneDir, "remote", "add", "origin", bareDir)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(cloneDir); err != nil {
		t.Fatal(err)
	}

	// When no .beads and no remote data, the remote probe should return false.
	if !isGitRepo() {
		t.Fatal("expected to be in a git repo")
	}
	if gitOriginHasDoltDataRef() {
		t.Fatal("origin should NOT have refs/dolt/data")
	}

	// .beads should still not exist after detection.
	beadsDir := filepath.Join(cloneDir, ".beads")
	if _, err := os.Stat(beadsDir); err == nil {
		t.Fatal(".beads should not be created when remote has no data")
	}
}

// TestBootstrapExistingBeadsDirUnchanged verifies that when .beads already
// exists, the normal bootstrap flow is unaffected by the fresh-clone fix.
// TestDetectBootstrapAction_PlanUsesConfiguredDatabaseName verifies that
// detectBootstrapAction carries the configured dolt_database into the plan,
// rather than silently falling back to the default "beads". This is the
// core regression test for GH#3029.
func TestDetectBootstrapAction_PlanUsesConfiguredDatabaseName(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	cfg.DoltDatabase = "my_project_db"

	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Database != "my_project_db" {
		t.Errorf("plan.Database = %q, want %q; bootstrap must use the configured database name, not the default",
			plan.Database, "my_project_db")
	}
}

// TestDetectBootstrapAction_PlanDefaultDatabaseWhenNotConfigured verifies
// that the default "beads" is used when no dolt_database is configured.
func TestDetectBootstrapAction_PlanDefaultDatabaseWhenNotConfigured(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Database != configfile.DefaultDoltDatabase {
		t.Errorf("plan.Database = %q, want %q (default)", plan.Database, configfile.DefaultDoltDatabase)
	}
}

// TestDetectBootstrapAction_ServerModePlanUsesConfiguredDatabaseName verifies
// that in server mode, the plan carries the configured database name for
// both the plan.Database field and the server probe. This is the specific
// failure mode reported in GH#3029: when FindBeadsDir resolved to the wrong
// .beads/, the config had no dolt_database, and the plan fell back to "beads".
func TestDetectBootstrapAction_ServerModePlanUsesConfiguredDatabaseName(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Create a dolt data dir with a subdirectory so the existing-DB check fires.
	// Use BEADS_DOLT_DATA_DIR (not shared server mode) so ResolveDoltDir
	// returns our test directory instead of ~/.beads/shared-server/dolt/.
	doltDataDir := filepath.Join(tmpDir, "dolt-data")
	if err := os.MkdirAll(filepath.Join(doltDataDir, "myrig"), 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_DATA_DIR", doltDataDir)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "myrig"
	cfg.DoltDataDir = doltDataDir

	var probedDBName string
	origCheck := checkBootstrapServerDB
	checkBootstrapServerDB = func(probeCfg bootstrapServerProbeConfig) bootstrapServerDBCheck {
		probedDBName = probeCfg.database
		return bootstrapServerDBCheck{Exists: false, Reachable: true}
	}
	defer func() { checkBootstrapServerDB = origCheck }()

	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Database != "myrig" {
		t.Errorf("plan.Database = %q, want %q", plan.Database, "myrig")
	}
	if probedDBName != "myrig" {
		t.Errorf("server probe used database %q, want %q; bootstrap must probe the configured database, not the default",
			probedDBName, "myrig")
	}
}

func TestBootstrapExistingBeadsDirUnchanged(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// With .beads present but empty, detectBootstrapAction should return "init".
	cfg := configfile.DefaultConfig()
	plan := detectBootstrapAction(beadsDir, cfg)
	if plan.Action != "init" {
		t.Errorf("action = %q, want %q for existing empty .beads", plan.Action, "init")
	}
}

// TestDetectBootstrapAction_ServerModeUsesCustomDatabaseName verifies that when
// metadata.json has dolt_database set to a custom name (e.g. "my_rig"),
// detectBootstrapAction uses that name in the plan instead of the default "beads".
// This is the core fix for GH#3029.
func TestDetectBootstrapAction_ServerModeUsesCustomDatabaseName(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Write metadata.json with a custom dolt_database name
	metadataJSON := `{"dolt_mode": "server", "dolt_database": "my_rig"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadataJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Load config the same way bootstrap.go does (lines 172-174)
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	// Verify config loaded the custom database name
	if got := cfg.GetDoltDatabase(); got != "my_rig" {
		t.Fatalf("GetDoltDatabase() = %q, want %q (metadata.json dolt_database ignored)", got, "my_rig")
	}

	plan := detectBootstrapAction(beadsDir, cfg)

	// The plan should use the custom database name, not "beads"
	if plan.Database != "my_rig" {
		t.Errorf("plan.Database = %q, want %q", plan.Database, "my_rig")
	}
}

// TestDetectBootstrapAction_FreshCloneUsesMetadataDBName verifies that when
// .beads doesn't exist but origin has refs/dolt/data, and metadata.json is
// committed to git with a custom dolt_database, the bootstrap plan uses the
// correct database name after .beads/metadata.json is loaded.
// Part of the fix for GH#3029.
func TestDetectBootstrapAction_FreshCloneUsesMetadataDBName(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	// Create a bare repo with refs/dolt/data
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGitForBootstrapTest(t, "", "init", "--bare", "--initial-branch=main", bareDir)

	sourceDir := t.TempDir()
	runGitForBootstrapTest(t, sourceDir, "init", "-b", "main")
	runGitForBootstrapTest(t, sourceDir, "config", "user.email", "test@test.com")
	runGitForBootstrapTest(t, sourceDir, "config", "user.name", "Test User")

	// Commit .beads/metadata.json with custom dolt_database to the source repo
	srcBeads := filepath.Join(sourceDir, ".beads")
	if err := os.MkdirAll(srcBeads, 0o750); err != nil {
		t.Fatal(err)
	}
	metadataJSON := `{"dolt_mode": "server", "dolt_database": "my_rig"}`
	if err := os.WriteFile(filepath.Join(srcBeads, "metadata.json"), []byte(metadataJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForBootstrapTest(t, sourceDir, "add", ".beads/metadata.json")
	runGitForBootstrapTest(t, sourceDir, "commit", "-m", "add beads metadata")
	runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", bareDir)
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "main")
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "HEAD:refs/dolt/data")

	// Clone and verify .beads/metadata.json is checked out.
	// Use a subdirectory of TempDir so git clone creates it (clone fails
	// if the target directory already exists and is non-empty).
	cloneDir := filepath.Join(t.TempDir(), "repo")
	runGitForBootstrapTest(t, "", "clone", bareDir, cloneDir)

	beadsDir := filepath.Join(cloneDir, ".beads")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(cloneDir); err != nil {
		t.Fatal(err)
	}

	// Load config the same way bootstrap.go does
	cfg, cfgErr := configfile.Load(beadsDir)
	if cfgErr != nil || cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	// After a git clone with committed metadata.json, the config should
	// have the custom database name
	if got := cfg.GetDoltDatabase(); got != "my_rig" {
		t.Fatalf("GetDoltDatabase() = %q, want %q (metadata.json dolt_database not loaded after clone)", got, "my_rig")
	}

	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "sync" {
		t.Errorf("action = %q, want %q", plan.Action, "sync")
	}
	if plan.Database != "my_rig" {
		t.Errorf("plan.Database = %q, want %q", plan.Database, "my_rig")
	}
}

// TestBootstrapFreshCloneSynthesizedDirUsesDefaultDB verifies that when
// .beads directory doesn't exist (no metadata.json committed to git) and
// beadsDir is synthesized from the remote-probe path, the config falls back
// to DefaultConfig and uses the default "beads" database name.
// This is the expected behavior for repos that never committed metadata.json.
func TestBootstrapFreshCloneSynthesizedDirUsesDefaultDB(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	// Create a bare repo with refs/dolt/data but NO .beads/metadata.json
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGitForBootstrapTest(t, "", "init", "--bare", bareDir)

	sourceDir := t.TempDir()
	runGitForBootstrapTest(t, sourceDir, "init", "-b", "main")
	runGitForBootstrapTest(t, sourceDir, "config", "user.email", "test@test.com")
	runGitForBootstrapTest(t, sourceDir, "config", "user.name", "Test User")
	runGitForBootstrapTest(t, sourceDir, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", bareDir)
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "HEAD:refs/dolt/data")
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "HEAD:refs/dolt/data")

	// Clone — no .beads dir
	cloneDir := t.TempDir()
	runGitForBootstrapTest(t, cloneDir, "init", "-b", "main")
	runGitForBootstrapTest(t, cloneDir, "remote", "add", "origin", bareDir)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(cloneDir); err != nil {
		t.Fatal(err)
	}

	// Synthesize beadsDir the way the Run handler does
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	synthesizedDir := filepath.Join(cwd, ".beads")

	// Load config the same way bootstrap.go does — synthesized dir doesn't exist
	cfg, cfgErr := configfile.Load(synthesizedDir)
	if cfgErr != nil || cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	// Without metadata.json, default "beads" is expected
	if got := cfg.GetDoltDatabase(); got != "beads" {
		t.Fatalf("GetDoltDatabase() = %q, want %q (should default when no metadata.json)", got, "beads")
	}

	plan := detectBootstrapAction(synthesizedDir, cfg)
	if plan.Action != "sync" {
		t.Errorf("action = %q, want %q", plan.Action, "sync")
	}
	if plan.Database != "beads" {
		t.Errorf("plan.Database = %q, want %q (default when no metadata.json)", plan.Database, "beads")
	}
}

// TestBootstrapRigSubdirUsesParentDBName verifies that when running bootstrap
// from a rig subdirectory (its own git repo) that doesn't have a local .beads,
// but the parent workspace has .beads/metadata.json with dolt_database set,
// the bootstrap plan uses the parent workspace's database name instead of "beads".
// This is the core reproduction for GH#3029.
func TestBootstrapRigSubdirUsesParentDBName(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	// Create workspace layout:
	//   workspace/
	//     .beads/metadata.json  (dolt_database: "my_rig")
	//     mayor/rig/            (its own git repo, no .beads)
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	metadataJSON := `{"dolt_mode": "server", "dolt_database": "my_rig"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadataJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a rig subdirectory with its own git repo and remote that has refs/dolt/data
	rigDir := filepath.Join(workspace, "mayor", "rig")
	if err := os.MkdirAll(rigDir, 0o750); err != nil {
		t.Fatal(err)
	}

	bareDir := filepath.Join(t.TempDir(), "rig-origin.git")
	runGitForBootstrapTest(t, "", "init", "--bare", "--initial-branch=main", bareDir)
	runGitForBootstrapTest(t, rigDir, "init", "-b", "main")
	runGitForBootstrapTest(t, rigDir, "config", "user.email", "test@test.com")
	runGitForBootstrapTest(t, rigDir, "config", "user.name", "Test User")
	runGitForBootstrapTest(t, rigDir, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, rigDir, "remote", "add", "origin", bareDir)
	runGitForBootstrapTest(t, rigDir, "push", "origin", "HEAD:refs/dolt/data")
	runGitForBootstrapTest(t, rigDir, "push", "origin", "HEAD:refs/dolt/data")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(rigDir); err != nil {
		t.Fatal(err)
	}

	// Simulate what the bootstrap Run handler does when FindBeadsDir returns "":
	// 1. beadsDir is empty (rig's git root has no .beads)
	// 2. Remote probe finds refs/dolt/data on origin
	// 3. beadsDir is synthesized as <cwd>/.beads
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	synthesizedDir := filepath.Join(cwd, ".beads")

	// configfile.Load on synthesized dir fails — no metadata.json there
	cfg, cfgErr := configfile.Load(synthesizedDir)
	if cfgErr != nil || cfg == nil {
		// This is the fix path: search parent directories for metadata.json
		cfg = findParentConfig(synthesizedDir)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	// The key assertion: should find the workspace's dolt_database, not default "beads"
	if got := cfg.GetDoltDatabase(); got != "my_rig" {
		t.Fatalf("GetDoltDatabase() = %q, want %q (parent workspace metadata.json not found)", got, "my_rig")
	}

	plan := detectBootstrapAction(synthesizedDir, cfg)
	if plan.Action != "sync" {
		t.Errorf("action = %q, want %q", plan.Action, "sync")
	}
	if plan.Database != "my_rig" {
		t.Errorf("plan.Database = %q, want %q", plan.Database, "my_rig")
	}
}

// TestDetectBootstrapAction_SharedServerEnvUsesSharedPath verifies that when
// BEADS_DOLT_SHARED_SERVER=1 is set but cfg.DoltMode is the default (embedded),
// detectBootstrapAction looks in the shared-server directory — not embeddeddolt/.
// This is the root cause of GH#30.
func TestDetectBootstrapAction_SharedServerEnvUsesSharedPath(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Override HOME so SharedDoltDir() resolves to our temp directory
	// instead of the real ~/.beads/shared-server/dolt/.
	t.Setenv("HOME", tmpDir)

	// Create a database directory at the shared-server location.
	// SharedDoltDir() returns $HOME/.beads/shared-server/dolt/.
	sharedDoltDir := filepath.Join(tmpDir, ".beads", "shared-server", "dolt")
	if err := os.MkdirAll(filepath.Join(sharedDoltDir, "beads"), 0o750); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Shared server enabled, but cfg.DoltMode is default (embedded).
	// Before the fix, this would look in embeddeddolt/ and miss the
	// existing shared-server database.
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")

	cfg := configfile.DefaultConfig()
	// Deliberately do NOT set cfg.DoltMode = configfile.DoltModeServer.
	// This reproduces the bug: shared-server via env var with default DoltMode.

	// The server probe stub: report the DB exists so we get action=none.
	origCheck := checkBootstrapServerDB
	checkBootstrapServerDB = func(probeCfg bootstrapServerProbeConfig) bootstrapServerDBCheck {
		return bootstrapServerDBCheck{Exists: true, Reachable: true}
	}
	defer func() { checkBootstrapServerDB = origCheck }()

	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "none" {
		t.Fatalf("expected action=none (existing shared-server DB detected), got %q: %s", plan.Action, plan.Reason)
	}
	if !plan.HasExisting {
		t.Error("HasExisting = false, want true")
	}
}

// TestDetectBootstrapAction_WorktreeSynthesizedDirPrefersSyncOverDefaultSharedDB
// verifies that when bootstrap is running from a worktree whose fallback
// .beads path lives under a bare/common git directory, remote recovery via
// refs/dolt/data wins over an unrelated default "beads" database already
// present on the shared server.
func TestDetectBootstrapAction_WorktreeSynthesizedDirPrefersSyncOverDefaultSharedDB(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	originBare := filepath.Join(t.TempDir(), "origin.git")
	runGitForBootstrapTest(t, "", "init", "--bare", "--initial-branch=main", originBare)

	sourceDir := t.TempDir()
	runGitForBootstrapTest(t, sourceDir, "init", "-b", "main")
	runGitForBootstrapTest(t, sourceDir, "config", "user.email", "test@test.com")
	runGitForBootstrapTest(t, sourceDir, "config", "user.name", "Test User")
	runGitForBootstrapTest(t, sourceDir, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", originBare)
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "main")
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "HEAD:refs/dolt/data")

	localBare := filepath.Join(t.TempDir(), "local-bare.git")
	runGitForBootstrapTest(t, "", "clone", "--bare", originBare, localBare)

	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	runGitForBootstrapTest(t, "", "--git-dir="+localBare, "worktree", "add", worktreeDir, "main")

	sharedDoltDir := filepath.Join(homeDir, ".beads", "shared-server", "dolt")
	if err := os.MkdirAll(filepath.Join(sharedDoltDir, "beads"), 0o750); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatal(err)
	}

	synthesizedDir := filepath.Join(localBare, ".beads")
	cfg, cfgErr := configfile.Load(synthesizedDir)
	if cfgErr != nil || cfg == nil {
		cfg = findParentConfig(synthesizedDir)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	if got := cfg.GetDoltDatabase(); got != "beads" {
		t.Fatalf("GetDoltDatabase() = %q, want %q (default expected without local metadata)", got, "beads")
	}

	origCheck := checkBootstrapServerDB
	checkBootstrapServerDB = func(probeCfg bootstrapServerProbeConfig) bootstrapServerDBCheck {
		if probeCfg.database != "beads" {
			t.Fatalf("probeCfg.database = %q, want %q", probeCfg.database, "beads")
		}
		return bootstrapServerDBCheck{Exists: true, Reachable: true}
	}
	defer func() { checkBootstrapServerDB = origCheck }()

	plan := detectBootstrapAction(synthesizedDir, cfg)

	if plan.Action != "sync" {
		t.Fatalf("expected action=%q, got %q: %s", "sync", plan.Action, plan.Reason)
	}
	if plan.SyncRemote == "" {
		t.Fatal("expected SyncRemote to be populated from origin refs/dolt/data detection")
	}
	if plan.Database != "beads" {
		t.Errorf("plan.Database = %q, want %q (default metadata-free value should still recover via sync)", plan.Database, "beads")
	}
}

func TestDetectBootstrapAction_SynthesizedDirWithoutRecoveryStillUsesExistingSharedDB(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(worktreeDir, 0o750); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatal(err)
	}

	sharedDoltDir := filepath.Join(homeDir, ".beads", "shared-server", "dolt")
	if err := os.MkdirAll(filepath.Join(sharedDoltDir, "project_existing"), 0o750); err != nil {
		t.Fatal(err)
	}

	synthesizedDir := filepath.Join(worktreeDir, ".beads")
	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "project_existing"

	origCheck := checkBootstrapServerDB
	checkBootstrapServerDB = func(probeCfg bootstrapServerProbeConfig) bootstrapServerDBCheck {
		if probeCfg.database != "project_existing" {
			t.Fatalf("probeCfg.database = %q, want %q", probeCfg.database, "project_existing")
		}
		return bootstrapServerDBCheck{Exists: true, Reachable: true}
	}
	defer func() { checkBootstrapServerDB = origCheck }()

	plan := detectBootstrapAction(synthesizedDir, cfg)

	if plan.Action != "none" {
		t.Fatalf("expected action=%q, got %q: %s", "none", plan.Action, plan.Reason)
	}
	if !plan.HasExisting {
		t.Fatal("expected HasExisting to be true when configured shared-server DB already exists")
	}
}

// TestFinalizeSyncedBootstrapWritesConfigFiles verifies that after a sync
// clone, finalizeSyncedBootstrap writes the metadata.json and config.yaml
// files bd needs to reopen the cloned database. This is the regression
// guard for GH#3201: executeSyncAction previously left the workspace
// without these files, causing "no beads configuration found" and
// "Error 1105: no database selected" on every subsequent bd command.
func TestFinalizeSyncedBootstrapWritesConfigFiles(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Simulate a post-clone workspace: cloneFromRemote created the
	// embeddeddolt directory but no metadata.json / config.yaml exists.
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o750); err != nil {
		t.Fatal(err)
	}

	// Point findProjectConfigYaml at the test workspace instead of walking
	// up from CWD, so the sync.remote write lands in the right file even
	// when the test runs from an unrelated directory.
	t.Setenv("BEADS_DIR", beadsDir)

	const dbName = "beads_hq"
	const syncRemote = "file:///tmp/fake-origin.git"

	cfg := configfile.DefaultConfig()
	if err := finalizeSyncedBootstrap(beadsDir, syncRemote, cfg, dbName); err != nil {
		t.Fatalf("finalizeSyncedBootstrap failed: %v", err)
	}

	// metadata.json must exist and record the database name bd needs to
	// reopen the cloned data. Without this, GetDoltDatabase() falls back to
	// DefaultDoltDatabase ("beads") and the cloned DB is unreachable.
	loaded, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("configfile.Load after finalize failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("configfile.Load returned nil; metadata.json was not written")
	}
	if loaded.GetDoltDatabase() != dbName {
		t.Errorf("dolt_database = %q, want %q", loaded.GetDoltDatabase(), dbName)
	}
	if loaded.GetDoltMode() != configfile.DoltModeEmbedded {
		t.Errorf("dolt_mode = %q, want %q", loaded.GetDoltMode(), configfile.DoltModeEmbedded)
	}
	if loaded.GetBackend() != configfile.BackendDolt {
		t.Errorf("backend = %q, want %q", loaded.GetBackend(), configfile.BackendDolt)
	}

	// config.yaml must exist so GetYamlConfig / SetYamlConfig and other
	// yaml-backed settings (sync.remote, dolt.shared-server, etc.) work.
	configYamlPath := filepath.Join(beadsDir, "config.yaml")
	yamlBytes, err := os.ReadFile(configYamlPath)
	if err != nil {
		t.Fatalf("config.yaml missing after finalize: %v", err)
	}
	yaml := string(yamlBytes)

	// sync.remote must be persisted so subsequent fresh clones (and
	// bootstrap retries) can rediscover the remote without re-probing
	// origin refs.
	if !strings.Contains(yaml, "sync.remote: ") && !strings.Contains(yaml, "sync-remote: ") {
		t.Errorf("config.yaml does not contain sync.remote entry:\n%s", yaml)
	}
	if !strings.Contains(yaml, syncRemote) {
		t.Errorf("config.yaml does not contain sync remote URL %q:\n%s", syncRemote, yaml)
	}
}

func TestFinalizeSyncedBootstrap_WorktreeStubDoesNotShadowTargetConfig(t *testing.T) {
	restore := snapshotBootstrapEnv(t)
	defer restore()

	config.ResetForTesting()
	defer config.ResetForTesting()

	originBare := filepath.Join(t.TempDir(), "origin.git")
	runGitForBootstrapTest(t, "", "init", "--bare", "--initial-branch=main", originBare)

	sourceDir := t.TempDir()
	runGitForBootstrapTest(t, sourceDir, "init", "-b", "main")
	runGitForBootstrapTest(t, sourceDir, "config", "user.email", "test@test.com")
	runGitForBootstrapTest(t, sourceDir, "config", "user.name", "Test User")
	runGitForBootstrapTest(t, sourceDir, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", originBare)
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "main")

	localBare := filepath.Join(t.TempDir(), "local-bare.git")
	runGitForBootstrapTest(t, "", "clone", "--bare", originBare, localBare)

	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	runGitForBootstrapTest(t, "", "--git-dir="+localBare, "worktree", "add", worktreeDir, "main")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatal(err)
	}

	// Reproduce the failing shape from fix-worktree-config-yaml-resolution:
	// the worktree has a local .beads stub, but bootstrap is finalizing the
	// shared config under the bare/common-dir parent.
	worktreeStubDir := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(worktreeStubDir, 0o750); err != nil {
		t.Fatal(err)
	}

	targetBeadsDir := filepath.Join(localBare, ".beads")
	if err := os.MkdirAll(filepath.Join(targetBeadsDir, "embeddeddolt"), 0o750); err != nil {
		t.Fatal(err)
	}

	const remoteURL = "git+ssh://git@github.com/gastownhall/gascity.git"
	cfg := configfile.DefaultConfig()
	if err := finalizeSyncedBootstrap(targetBeadsDir, remoteURL, cfg, "gascity"); err != nil {
		t.Fatalf("finalizeSyncedBootstrap failed: %v", err)
	}

	targetConfigPath := filepath.Join(targetBeadsDir, "config.yaml")
	targetContent, err := os.ReadFile(targetConfigPath)
	if err != nil {
		t.Fatalf("failed to read target config.yaml: %v", err)
	}
	if !strings.Contains(string(targetContent), remoteURL) {
		t.Fatalf("expected target config.yaml to contain %q, got:\n%s", remoteURL, string(targetContent))
	}

	if _, err := os.Stat(filepath.Join(worktreeStubDir, "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected worktree stub config.yaml to remain absent, got err=%v", err)
	}
}

// TestFinalizeSyncedBootstrapIsIdempotent verifies that re-running the
// finalize step over an already-finalized workspace is a no-op — the
// clone retry path relies on this.
func TestFinalizeSyncedBootstrapIsIdempotent(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)

	cfg := configfile.DefaultConfig()
	if err := finalizeSyncedBootstrap(beadsDir, "file:///tmp/a.git", cfg, "beads_hq"); err != nil {
		t.Fatalf("first finalize failed: %v", err)
	}

	firstYaml, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml after first finalize: %v", err)
	}

	if err := finalizeSyncedBootstrap(beadsDir, "file:///tmp/a.git", cfg, "beads_hq"); err != nil {
		t.Fatalf("second finalize failed: %v", err)
	}

	secondYaml, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml after second finalize: %v", err)
	}

	// createConfigYaml skips existing files, so the template portion must
	// be unchanged. SetYamlConfig rewrites in place but should produce the
	// same output for the same value.
	if string(firstYaml) != string(secondYaml) {
		t.Errorf("config.yaml changed on second finalize.\nfirst:\n%s\nsecond:\n%s", firstYaml, secondYaml)
	}

	// metadata.json must still load cleanly.
	loaded, err := configfile.Load(beadsDir)
	if err != nil || loaded == nil {
		t.Fatalf("metadata.json missing after second finalize: %v", err)
	}
	if loaded.GetDoltDatabase() != "beads_hq" {
		t.Errorf("dolt_database drifted: got %q, want %q", loaded.GetDoltDatabase(), "beads_hq")
	}
}

// TestFinalizeSyncedBootstrapServerModePreservesMode verifies that when
// metadata.json already declares dolt_mode=server (tracked in git),
// finalizeSyncedBootstrap preserves server mode instead of resetting to
// embedded. This is the root cause of GH#3343.
func TestFinalizeSyncedBootstrapServerModePreservesMode(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)

	// Simulate a workspace where metadata.json (tracked in git) declares
	// server mode — the scenario from GH#3343.
	cfg := &configfile.Config{
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 3308,
		DoltDatabase:   "beads_proj",
	}

	if err := finalizeSyncedBootstrap(beadsDir, "file:///tmp/fake.git", cfg, "beads_proj"); err != nil {
		t.Fatalf("finalizeSyncedBootstrap failed: %v", err)
	}

	loaded, err := configfile.Load(beadsDir)
	if err != nil || loaded == nil {
		t.Fatalf("metadata.json missing: %v", err)
	}
	if loaded.GetDoltMode() != configfile.DoltModeServer {
		t.Errorf("dolt_mode = %q, want %q — server mode was not preserved", loaded.GetDoltMode(), configfile.DoltModeServer)
	}
	if loaded.GetDoltDatabase() != "beads_proj" {
		t.Errorf("dolt_database = %q, want %q", loaded.GetDoltDatabase(), "beads_proj")
	}
}

// TestCloneFromRemoteRoutesToServerMode verifies that cloneFromRemote uses
// the SQL server path (not filesystem clone) when ResolveServerMode
// detects external server mode from metadata.json. GH#3343.
func TestCloneFromRemoteRoutesToServerMode(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Write metadata.json with server mode and explicit port — this makes
	// ResolveServerMode return ServerModeExternal.
	cfg := &configfile.Config{
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 3308,
		DoltDatabase:   "beads_proj",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}

	// cloneFromRemote should attempt a server connection (not a filesystem
	// clone). Since no server is running, we expect a connection error —
	// NOT a "dolt clone failed" error, which would indicate the filesystem
	// path was taken.
	err := cloneFromRemote(t.Context(), beadsDir, "file:///tmp/nonexistent.git", "beads_proj", cfg)
	if err == nil {
		t.Fatal("expected error (no server running), got nil")
	}
	errMsg := err.Error()

	// The error should indicate a server connection attempt, not a CLI clone.
	if strings.Contains(errMsg, "dolt clone failed") {
		t.Errorf("cloneFromRemote used filesystem clone path in server mode: %v", err)
	}
	if !strings.Contains(errMsg, "server") {
		t.Errorf("expected server-related error, got: %v", err)
	}

	// Verify no local dolt directory was created.
	doltDir := filepath.Join(beadsDir, "dolt")
	if _, err := os.Stat(doltDir); err == nil {
		t.Errorf("local .beads/dolt/ directory was created — clone should have gone to server, not filesystem")
	}
}

// TestCloneFromRemoteRoutesToServerModeViaEnv verifies that the
// BEADS_DOLT_SERVER_MODE=1 env var triggers the server clone path,
// even when metadata.json is absent.
func TestCloneFromRemoteRoutesToServerModeViaEnv(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 3309,
		DoltDatabase:   "beads_env",
	}

	err := cloneFromRemote(t.Context(), beadsDir, "file:///tmp/nonexistent.git", "beads_env", cfg)
	if err == nil {
		t.Fatal("expected error (no server running), got nil")
	}
	if strings.Contains(err.Error(), "dolt clone failed") {
		t.Errorf("cloneFromRemote used filesystem clone path despite BEADS_DOLT_SERVER_MODE=1: %v", err)
	}
	if !strings.Contains(err.Error(), "server") {
		t.Errorf("expected server-related error, got: %v", err)
	}
}

// TestCloneFromRemoteExternalNilCfgLoadsDisk verifies that when cfg is nil
// in external server mode, cloneFromRemote falls back to loading config
// from metadata.json on disk.
func TestCloneFromRemoteExternalNilCfgLoadsDisk(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Write metadata.json to disk with server mode — but pass nil cfg.
	diskCfg := &configfile.Config{
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 3310,
		DoltDatabase:   "beads_disk",
	}
	if err := diskCfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}

	// Pass nil cfg — cloneFromRemote should load from disk and still
	// take the server path.
	err := cloneFromRemote(t.Context(), beadsDir, "file:///tmp/nonexistent.git", "beads_disk", nil)
	if err == nil {
		t.Fatal("expected error (no server running), got nil")
	}
	if strings.Contains(err.Error(), "dolt clone failed") {
		t.Errorf("nil-cfg path used filesystem clone despite server metadata on disk: %v", err)
	}
	if !strings.Contains(err.Error(), "server") {
		t.Errorf("expected server-related error, got: %v", err)
	}
}

// TestCloneFromRemoteOwnedModeUsesCLI verifies that owned-server mode
// (the default when no metadata.json exists) uses the CLI clone path.
func TestCloneFromRemoteOwnedModeUsesCLI(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// No metadata.json → ResolveServerMode returns ServerModeOwned → CLI path.
	// The CLI path calls BootstrapFromRemoteWithDB, which requires dolt CLI.
	// Since dolt may not be installed in CI, we accept either:
	// - "dolt CLI not found" (no dolt binary)
	// - "dolt clone failed" (dolt binary exists but remote is invalid)
	// Both confirm the CLI path was taken, not the server path.
	err := cloneFromRemote(t.Context(), beadsDir, "file:///tmp/nonexistent.git", "beads_owned", nil)
	if err == nil {
		// BootstrapFromRemoteWithDB returns (false, nil) if doltExists — skip.
		return
	}
	errMsg := err.Error()
	if strings.Contains(errMsg, "dolt server unreachable") || strings.Contains(errMsg, "connect to dolt server") {
		t.Errorf("owned-mode clone routed to server path: %v", err)
	}
}

func TestResolveRemoteCloneModeDefaultConfigUsesEmbedded(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	cfg := configfile.DefaultConfig()

	got := resolveRemoteCloneMode(beadsDir, cfg, remoteCloneAuto)
	if got != remoteCloneEmbedded {
		t.Fatalf("resolveRemoteCloneMode(default cfg) = %v, want embedded", got)
	}
}

func TestResolveRemoteCloneModeExplicitExternalOverridesMissingMetadata(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	cfg := &configfile.Config{
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 3312,
		DoltDatabase:   "beads_external",
	}

	got := resolveRemoteCloneMode(beadsDir, cfg, remoteCloneExternalServer)
	if got != remoteCloneExternalServer {
		t.Fatalf("resolveRemoteCloneMode(explicit external) = %v, want external server", got)
	}
}

// TestCloneFromRemoteSharedServerModeUsesServer verifies that
// BEADS_DOLT_SHARED_SERVER=1 triggers the server clone path.
func TestCloneFromRemoteSharedServerModeUsesServer(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 3311,
		DoltDatabase:   "beads_shared",
	}

	err := cloneFromRemote(t.Context(), beadsDir, "file:///tmp/nonexistent.git", "beads_shared", cfg)
	if err == nil {
		t.Fatal("expected error (no server running), got nil")
	}
	if strings.Contains(err.Error(), "dolt clone failed") {
		t.Errorf("shared-server mode used filesystem clone: %v", err)
	}
	if !strings.Contains(err.Error(), "server") {
		t.Errorf("expected server-related error, got: %v", err)
	}
}

// TestFinalizeSyncedBootstrapSharedServerSetsServerMode verifies that
// finalizeSyncedBootstrap writes dolt_mode=server when shared-server
// mode is active via env var.
func TestFinalizeSyncedBootstrapSharedServerSetsServerMode(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)

	cfg := configfile.DefaultConfig()
	if err := finalizeSyncedBootstrap(beadsDir, "file:///tmp/fake.git", cfg, "beads_shared"); err != nil {
		t.Fatalf("finalizeSyncedBootstrap failed: %v", err)
	}

	loaded, err := configfile.Load(beadsDir)
	if err != nil || loaded == nil {
		t.Fatalf("metadata.json missing: %v", err)
	}
	if loaded.GetDoltMode() != configfile.DoltModeServer {
		t.Errorf("dolt_mode = %q, want %q — shared server should set server mode", loaded.GetDoltMode(), configfile.DoltModeServer)
	}
}
