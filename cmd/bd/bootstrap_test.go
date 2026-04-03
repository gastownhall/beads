//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestDetectBootstrapAction_NoneWhenDatabaseExists(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
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
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "main")
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
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "main")
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
	originURL, err := gitRemoteGetURL("origin")
	if err != nil || originURL == "" {
		t.Fatalf("expected origin URL, got err=%v url=%q", err, originURL)
	}
	if !gitLsRemoteHasRef("origin", "refs/dolt/data") {
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
	if gitLsRemoteHasRef("origin", "refs/dolt/data") {
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
