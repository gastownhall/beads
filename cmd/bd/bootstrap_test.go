//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/doltversion"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
)

// snapshotBootstrapEnv saves all BD_/BEADS_-prefixed env vars, clears them,
// and registers a t.Cleanup to restore them. It restores via t.Cleanup
// (rather than returning a function for the caller to defer) because
// t.Cleanup callbacks only start running after the test function's own
// regular defers have all completed. Tests using this helper commonly call
// t.Setenv("BEADS_DIR", ...) / t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", ...)
// afterward; t.Setenv captures its "restore to" value at call time, which
// would be the already-wiped (absent) state. If this helper's own
// restoration ran via a caller-deferred function, it would run before those
// t.Setenv cleanups fire and its correct restoration would be clobbered by
// them, permanently leaking the wipe forward into later tests in the same
// binary. Registering via t.Cleanup here instead makes this the
// first-registered (and thus, by t.Cleanup's LIFO order, last-run) cleanup,
// so it always has the final word.
func snapshotBootstrapEnv(t *testing.T) {
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
	t.Cleanup(func() {
		for _, env := range os.Environ() {
			if strings.HasPrefix(env, "BD_") || strings.HasPrefix(env, "BEADS_") {
				parts := strings.SplitN(env, "=", 2)
				_ = os.Unsetenv(parts[0])
			}
		}
		for key, val := range saved {
			_ = os.Setenv(key, val)
		}
	})
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
	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	defer func() { bootstrapRetryDelay = origDelay }()

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
	// The workspace names its server's port, never inheriting one: the Dolt
	// test server exports BEADS_DOLT_PORT/BEADS_DOLT_SERVER_PORT when it is
	// up, and with no port named bootstrap probes nothing.
	cfg.DoltServerPort = 13307
	t.Setenv("BEADS_DOLT_PORT", "")
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
	snapshotBootstrapEnv(t)
	config.ResetForTesting()
	defer config.ResetForTesting()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	const syncRemote = "http://myserver:7007/mydb"
	seedSyncRemote(t, beadsDir, syncRemote)
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

// TestDetectBootstrapAction_ExistingEmbeddedDBWithSyncRemoteIsNoOp is a
// regression for GH#5037: when sync.remote is set AND embeddeddolt already
// exists, plan must be Action=none (validate/report), not Action=sync which
// would DOLT_CLONE into the existing database and fail with Error 1007.
func TestDetectBootstrapAction_ExistingEmbeddedDBWithSyncRemoteIsNoOp(t *testing.T) {
	snapshotBootstrapEnv(t)
	config.ResetForTesting()
	defer config.ResetForTesting()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Non-empty embedded database footprint (what a prior successful bootstrap left).
	embedded := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.MkdirAll(filepath.Join(embedded, "mydb"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(embedded, "mydb", ".keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	const syncRemote = "http://myserver:7007/mydb"
	seedSyncRemote(t, beadsDir, syncRemote)
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

	if plan.Action != "none" {
		t.Fatalf("action = %q reason=%q, want none (existing DB must not re-clone when sync.remote set)", plan.Action, plan.Reason)
	}
	if !plan.HasExisting {
		t.Error("HasExisting = false, want true")
	}
	if !strings.Contains(plan.Reason, "already exists") {
		t.Errorf("Reason = %q, want to mention already exists", plan.Reason)
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
	// The workspace names its server's port, never inheriting one: the Dolt
	// test server exports BEADS_DOLT_PORT/BEADS_DOLT_SERVER_PORT when it is
	// up, and with no port named bootstrap probes nothing.
	cfg.DoltServerPort = 13307
	t.Setenv("BEADS_DOLT_PORT", "")

	var probedDBName string
	origCheck := checkBootstrapServerDB
	checkBootstrapServerDB = func(probeCfg bootstrapServerProbeConfig) bootstrapServerDBCheck {
		probedDBName = probeCfg.database
		return bootstrapServerDBCheck{Exists: false, Reachable: true}
	}
	defer func() { checkBootstrapServerDB = origCheck }()
	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	defer func() { bootstrapRetryDelay = origDelay }()

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
		cfg, cfgErr = findParentConfig(synthesizedDir)
		if cfgErr != nil {
			t.Fatalf("findParentConfig: %v", cfgErr)
		}
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

func TestFindParentConfigDoesNotSkipCorruptNearestAncestor(t *testing.T) {
	root := t.TempDir()
	higherBeadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(higherBeadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(higherBeadsDir, "metadata.json"), []byte(`{"dolt_database":"wrong_higher_database"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	nearestBeadsDir := filepath.Join(root, "workspace", ".beads")
	if err := os.MkdirAll(nearestBeadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("{\n")
	metadataPath := filepath.Join(nearestBeadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	synthesizedDir := filepath.Join(root, "workspace", "rig", ".beads")
	if cfg, err := findParentConfig(synthesizedDir); err == nil {
		t.Fatalf("findParentConfig skipped corrupt nearest metadata and selected higher database %q", cfg.GetDoltDatabase())
	} else if !strings.Contains(err.Error(), filepath.Join("workspace", ".beads", "metadata.json")) {
		t.Fatalf("findParentConfig error = %v, want nearest metadata path", err)
	}
	if after, err := os.ReadFile(metadataPath); err != nil || string(after) != string(corrupt) {
		t.Fatalf("corrupt nearest metadata changed: got %q, err %v", after, err)
	}
}

func TestFindParentConfigDoesNotAdoptOSTempRoot(t *testing.T) {
	sandbox := t.TempDir()
	tempRoot := filepath.Join(sandbox, "tmp")
	rootBeadsDir := filepath.Join(tempRoot, ".beads")
	if err := os.MkdirAll(rootBeadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootBeadsDir, "metadata.json"), []byte(`{"dolt_database":"temp_root"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tempRoot)

	requested := filepath.Join(tempRoot, "isolated", "project", ".beads")
	cfg, err := findParentConfig(requested)
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Fatalf("findParentConfig() adopted OS temp-root database %q", cfg.GetDoltDatabase())
	}
}

// TestFindParentConfigWithoutHomeKeepsSearchingAboveCwd pins the $HOME
// boundary's empty sentinel. os.UserHomeDir fails when HOME is unset, and
// canonicalizing the resulting "" resolves it to the current working
// directory, which silently turns "don't search above $HOME" into "stop at the
// CWD". The walk here starts exactly at the CWD, so the ancestor workspace
// config one level up is only reachable when "" still means "no boundary".
func TestFindParentConfigWithoutHomeKeepsSearchingAboveCwd(t *testing.T) {
	sandbox := t.TempDir()
	workspace := filepath.Join(sandbox, "workspace")
	workspaceBeadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(workspaceBeadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceBeadsDir, "metadata.json"), []byte(`{"dolt_database":"parent_workspace"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// findParentConfig starts at the parent of the requested .beads directory's
	// project, which is <workspace>/rig here.
	rig := filepath.Join(workspace, "rig")
	requested := filepath.Join(rig, "project", ".beads")
	if err := os.MkdirAll(requested, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Chdir(rig)
	// Keep the temp-root ceiling clear of this walk; it is not what this pins.
	t.Setenv("TMPDIR", filepath.Join(sandbox, "tmp"))
	// os.UserHomeDir reads HOME everywhere except Windows, where it reads
	// USERPROFILE; clear both so it fails the way an unset HOME does.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	cfg, err := findParentConfig(requested)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("findParentConfig() = nil, want the workspace config above the CWD")
	}
	if got := cfg.GetDoltDatabase(); got != "parent_workspace" {
		t.Errorf("GetDoltDatabase() = %q, want %q", got, "parent_workspace")
	}
}

// TestFindParentConfigPassesOverUserGlobalBeads pins GH#4402 for bootstrap's
// parent-config walk. A project under the account home with no metadata.json
// does not inherit the user-global <home>/.beads/metadata.json, whose database
// name and server bootstrap would otherwise set up, and the walk does not
// climb above the account home when $HOME points elsewhere. A home directory
// that is itself a repository root keeps its .beads as that repository's own.
func TestFindParentConfigPassesOverUserGlobalBeads(t *testing.T) {
	writeMetadata := func(t *testing.T, beadsDir, database string) {
		t.Helper()
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"dolt_database":"`+database+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// <sandbox>/root/home is the account home and holds the user-global
	// .beads; <sandbox>/root/.beads lies above it, where only a walk that
	// climbs past the home directory would find it.
	type fixture struct{ sandbox, home, requested string }
	setup := func(t *testing.T) fixture {
		t.Helper()
		sandbox, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(sandbox, "root")
		f := fixture{sandbox: sandbox, home: filepath.Join(root, "home")}
		writeMetadata(t, filepath.Join(root, ".beads"), "above_home")
		writeMetadata(t, filepath.Join(f.home, ".beads"), "user_global")
		f.requested = filepath.Join(f.home, "proj", ".beads")
		if err := os.MkdirAll(f.requested, 0o750); err != nil {
			t.Fatal(err)
		}
		// Keep the temp-root ceiling clear of this walk; it is not what this pins.
		t.Setenv("TMPDIR", filepath.Join(sandbox, "tmp"))
		t.Cleanup(beads.SetUserHomeDirForTesting(f.home))
		return f
	}
	setHome := func(t *testing.T, home string) {
		t.Helper()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
	}

	t.Run("project under the home directory", func(t *testing.T) {
		f := setup(t)
		setHome(t, f.home)
		cfg, err := findParentConfig(f.requested)
		if err != nil {
			t.Fatal(err)
		}
		if cfg != nil {
			t.Errorf("findParentConfig() inherited database %q, want no parent config", cfg.GetDoltDatabase())
		}
	})

	t.Run("HOME points elsewhere", func(t *testing.T) {
		f := setup(t)
		setHome(t, filepath.Join(f.sandbox, "elsewhere"))
		cfg, err := findParentConfig(f.requested)
		if err != nil {
			t.Fatal(err)
		}
		if cfg != nil {
			t.Errorf("findParentConfig() inherited database %q, want no parent config: the walk ends at the account home", cfg.GetDoltDatabase())
		}
	})

	t.Run("home is a repository root", func(t *testing.T) {
		f := setup(t)
		setHome(t, f.home)
		initGitRepoAt(t, f.home)
		cfg, err := findParentConfig(f.requested)
		if err != nil {
			t.Fatal(err)
		}
		if cfg == nil || cfg.GetDoltDatabase() != "user_global" {
			t.Errorf("findParentConfig() = %+v, want the home repository's own database %q", cfg, "user_global")
		}
	})

	t.Run("home repository without metadata, HOME elsewhere", func(t *testing.T) {
		// The home repository's .beads is examined and holds nothing, and
		// the walk still ends at the account home.
		f := setup(t)
		if err := os.Remove(filepath.Join(f.home, ".beads", "metadata.json")); err != nil {
			t.Fatal(err)
		}
		setHome(t, filepath.Join(f.sandbox, "elsewhere"))
		initGitRepoAt(t, f.home)
		cfg, err := findParentConfig(f.requested)
		if err != nil {
			t.Fatal(err)
		}
		if cfg != nil {
			t.Errorf("findParentConfig() inherited database %q, want no parent config: the walk ends at the account home", cfg.GetDoltDatabase())
		}
	})
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

	// Override the home dir so SharedDoltDir() resolves to our temp directory
	// instead of the real ~/.beads/shared-server/dolt/. It goes through
	// os.UserHomeDir(), which reads USERPROFILE on Windows and HOME elsewhere,
	// so both must be set for this to be portable.
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)

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
		cfg, cfgErr = findParentConfig(synthesizedDir)
		if cfgErr != nil {
			t.Fatalf("findParentConfig: %v", cfgErr)
		}
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
	// detectBootstrapAction resolves the home dir with os.UserHomeDir(), which reads
	// USERPROFILE on Windows and HOME elsewhere; set both so the shared-server probe
	// looks inside the temp home on every platform.
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

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
	//
	// Asserted by READING it back rather than by matching a spelling. This used
	// to require a literal `sync.remote: ` line, which is a key whose name
	// contains a dot — the shape config.GetStringFromDir and viper can never
	// find, because both split on the dot and walk nested mappings. The writer
	// now nests (bd-zj95), so the old assertion passed for exactly as long as
	// the value was unreadable and failed the moment it became readable. What
	// bootstrap owes its caller is a remote that can be read back, so that is
	// what this checks.
	if got := config.GetStringFromDir(beadsDir, "sync.remote"); got != syncRemote {
		t.Errorf("config.GetStringFromDir(%q, \"sync.remote\") = %q, want %q:\n%s",
			beadsDir, got, syncRemote, yaml)
	}
	if !strings.Contains(yaml, syncRemote) {
		t.Errorf("config.yaml does not contain sync remote URL %q:\n%s", syncRemote, yaml)
	}

	gitignoreBytes, err := os.ReadFile(filepath.Join(beadsDir, ".gitignore"))
	if err != nil {
		t.Fatalf(".beads/.gitignore missing after finalize: %v", err)
	}
	gitignore := string(gitignoreBytes)
	for _, pattern := range []string{".local_version", "backup/", "export-state.json", "last-touched"} {
		if !strings.Contains(gitignore, pattern) {
			t.Errorf(".beads/.gitignore missing runtime pattern %q:\n%s", pattern, gitignore)
		}
	}
}

func TestFinalizeSyncedBootstrap_WorktreeStubDoesNotShadowTargetConfig(t *testing.T) {
	snapshotBootstrapEnv(t)

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

	const remoteURL = "git+ssh://git@github.com/example-org/example-app.git"
	cfg := configfile.DefaultConfig()
	if err := finalizeSyncedBootstrap(targetBeadsDir, remoteURL, cfg, "example-org"); err != nil {
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

func TestApplyBootstrapMetadataRepair_UsesResolvedConfig(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	origResolve := resolveBootstrapAuthoritativeMetadata
	resolveBootstrapAuthoritativeMetadata = func(path string, apply bool) (*configfile.Config, string, error) {
		if path != tmpDir {
			t.Fatalf("path = %q, want %q", path, tmpDir)
		}
		if !apply {
			t.Fatal("expected apply=true")
		}
		return &configfile.Config{DoltMode: configfile.DoltModeServer, DoltDatabase: "canonical_db"}, "repaired dolt_database", nil
	}
	defer func() { resolveBootstrapAuthoritativeMetadata = origResolve }()

	resolved, msg, err := applyBootstrapMetadataRepair(beadsDir, configfile.DefaultConfig(), true)
	if err != nil {
		t.Fatalf("applyBootstrapMetadataRepair failed: %v", err)
	}
	if resolved == nil {
		t.Fatal("resolved config is nil")
	}
	if resolved.GetDoltDatabase() != "canonical_db" {
		t.Fatalf("GetDoltDatabase() = %q, want %q", resolved.GetDoltDatabase(), "canonical_db")
	}
	if msg != "repaired dolt_database" {
		t.Fatalf("msg = %q, want %q", msg, "repaired dolt_database")
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

	tmpDir2 := t.TempDir()
	beadsDir2 := filepath.Join(tmpDir2, ".beads")
	if err := os.MkdirAll(beadsDir2, 0o750); err != nil {
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
	if err := cfg.Save(beadsDir2); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}

	// cloneFromRemote should attempt a server connection (not a filesystem
	// clone). Since no server is running, we expect a connection error —
	// NOT a "dolt clone failed" error, which would indicate the filesystem
	// path was taken.
	err := cloneFromRemote(t.Context(), beadsDir2, "file:///tmp/nonexistent.git", "beads_proj", cfg)
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
	doltDir := filepath.Join(beadsDir2, "dolt")
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
	// Isolated, with the legacy BEADS_DOLT_PORT naming a closed loopback
	// port: a shared server's clone port follows it over the 3308 default,
	// so the clone fails fast here instead of dialing a harness server.
	clonePortTestEnv(t)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	port := freeLoopbackPort(t)
	t.Setenv("BEADS_DOLT_PORT", strconv.Itoa(port))

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
	if want := "dolt server unreachable at " + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)); !strings.Contains(err.Error(), want) {
		t.Errorf("clone error = %v, want it to name %s", err, want)
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

// TestDetectBootstrapAction_NoLocalShadowDirStillLiveChecksExisting reproduces
// be-cy41 bug #1: existingBootstrapDBPlan returned BootstrapPlan{}, false (and
// detectBootstrapAction fell through toward Action="init") purely because no
// local shadow dolt-data directory existed — but in server mode that is the
// NORMAL state for a fresh clone of an already-initialized project. When
// metadata.json carries a ProjectID (proving this workspace was previously
// initialized), the server-side existence check must still run instead of
// being short-circuited by local filesystem state that a legitimate clone
// will never have (the same ambiguity PR #5791 addresses for bd init; that
// PR is open and not yet merged, so nothing it adds is referenced here).
func TestDetectBootstrapAction_NoLocalShadowDirStillLiveChecksExisting(t *testing.T) {
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
	// Deliberately do NOT create doltDataDir at all — no local shadow
	// directory, which is the normal state for a fresh clone of a
	// pre-existing server-mode project.
	doltDataDir := filepath.Join(tmpDir, "dolt-data")

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
	cfg.DoltDatabase = "project_exists"
	cfg.DoltDataDir = doltDataDir
	// The workspace names its server's port, never inheriting one: the Dolt
	// test server exports BEADS_DOLT_PORT/BEADS_DOLT_SERVER_PORT when it is
	// up, and with no port named bootstrap probes nothing.
	cfg.DoltServerPort = 13307
	t.Setenv("BEADS_DOLT_PORT", "")
	cfg.ProjectID = "proj-already-initialized-456"
	t.Setenv("BEADS_DOLT_DATA_DIR", doltDataDir)

	probed := false
	origCheck := checkBootstrapServerDB
	checkBootstrapServerDB = func(probeCfg bootstrapServerProbeConfig) bootstrapServerDBCheck {
		probed = true
		if probeCfg.database != "project_exists" {
			t.Fatalf("unexpected dbName: %s", probeCfg.database)
		}
		return bootstrapServerDBCheck{Exists: true, Reachable: true}
	}
	defer func() { checkBootstrapServerDB = origCheck }()
	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	defer func() { bootstrapRetryDelay = origDelay }()

	plan := detectBootstrapAction(beadsDir, cfg)

	if !probed {
		t.Fatal("checkBootstrapServerDB was never called — a local-shadow-directory gate short-circuited the real server-side existence check before it could run")
	}
	if plan.Action != "none" {
		t.Fatalf("action = %q, want %q — bootstrap must not plan to recreate a database that already exists on the server just because no local shadow directory is present", plan.Action, "none")
	}
	if !plan.HasExisting {
		t.Error("HasExisting = false, want true")
	}
}

// TestExecuteInitAction_ServerModeExistingProjectMissingDBRefuses reproduces
// be-cy41 bug #2: when a server-mode workspace was already initialized
// (metadata.json has a project_id — the same signal PR #5791 uses) but the
// configured database is missing on the server, creating one would strand
// any existing issue data behind a new, empty database of the same name.
// executeInitAction must refuse instead (the same root cause as PR #5791's
// bd init guard), before it opens any store: neither the workspace's server
// (bootstrapStoreConfig) nor an embedded one.
func TestExecuteInitAction_ServerModeExistingProjectMissingDBRefuses(t *testing.T) {
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
	cfg.DoltDatabase = "myproj"
	cfg.ProjectID = "proj-existing-123"

	origCheck := checkBootstrapServerDB
	checkBootstrapServerDB = func(probeCfg bootstrapServerProbeConfig) bootstrapServerDBCheck {
		return bootstrapServerDBCheck{Exists: false, Reachable: true}
	}
	defer func() { checkBootstrapServerDB = origCheck }()
	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	defer func() { bootstrapRetryDelay = origDelay }()

	plan := BootstrapPlan{BeadsDir: beadsDir, Database: "myproj", Action: "init"}
	err = executeInitAction(context.Background(), plan, cfg)

	if err == nil {
		t.Fatal("executeInitAction succeeded silently for an existing project with a missing server-mode database — want a refusal error")
	}
	if !strings.Contains(err.Error(), "myproj") {
		t.Errorf("error %q does not mention the missing database name %q", err.Error(), "myproj")
	}
	if !strings.Contains(err.Error(), "will NOT create") && !strings.Contains(err.Error(), "not found on server") {
		t.Errorf("error %q does not read as a refusal to auto-create", err.Error())
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, "embeddeddolt")); statErr == nil {
		t.Error("executeInitAction created an embedded database as a side effect of a refused server-mode init — no database should have been created at all")
	}
}

// freshServerModeCloneFixture builds the state a fresh clone of an
// already-bootstrapped server-mode project is actually in: .beads/ present
// with a configured sync.remote, metadata.json carrying a ProjectID, and no
// local shadow dolt-data directory (a clone never has one).
func freshServerModeCloneFixture(t *testing.T) (beadsDir string, cfg *configfile.Config) {
	t.Helper()
	snapshotBootstrapEnv(t)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	tmpDir := t.TempDir()
	beadsDir = filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("sync.remote: http://myserver:7007/mydb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize failed: %v", err)
	}
	t.Chdir(tmpDir)
	cfg = configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "mydb"
	cfg.DoltDataDir = filepath.Join(tmpDir, "dolt-data") // never created: fresh clone
	cfg.ProjectID = "proj-fresh-clone-789"
	// The workspace names its server's port, so there is a server to probe.
	// With none named, bootstrap probes nothing (no 127.0.0.1:0, no 3307):
	// see TestBootstrapNeverDialsAnUnresolvedServerPort.
	cfg.DoltServerPort = 13307
	t.Setenv("BEADS_DOLT_DATA_DIR", cfg.DoltDataDir)
	return beadsDir, cfg
}

// TestDetectBootstrapAction_FreshCloneUnreachableServerStillSyncs pins the
// rule that an unverifiable probe means UNKNOWN, not "exists". A fresh clone
// of a locally-managed server-mode project has nothing running yet, so
// "connection refused" is its NORMAL state — it must not be converted into
// Action="none" ("nothing to do"), which would hide sync.remote,
// refs/dolt/data, .beads/backup/ and issues.jsonl behind a dead server.
func TestDetectBootstrapAction_FreshCloneUnreachableServerStillSyncs(t *testing.T) {
	beadsDir, cfg := freshServerModeCloneFixture(t)
	orig := checkBootstrapServerDB
	checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck {
		return bootstrapServerDBCheck{Reachable: false, Err: errors.New("dial tcp 127.0.0.1:3307: connect: connection refused")}
	}
	defer func() { checkBootstrapServerDB = orig }()
	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	defer func() { bootstrapRetryDelay = origDelay }()

	plan := detectBootstrapAction(beadsDir, cfg)
	if plan.Action != "sync" {
		t.Errorf("action = %q (reason %q), want sync: an unreachable server must not veto sync.remote recovery on a fresh clone", plan.Action, plan.Reason)
	}
}

// TestDetectBootstrapAction_FreshCloneReachableAbsentSkipsTransientBackoff
// pins that the 10/20/40s transient-restart budget is only spent when a local
// shadow directory says the database really does live here. A fresh clone has
// no shadow directory by design, so "reachable but absent" is simply "not
// synced yet" and must cost one probe, not 70 seconds.
func TestDetectBootstrapAction_FreshCloneReachableAbsentSkipsTransientBackoff(t *testing.T) {
	beadsDir, cfg := freshServerModeCloneFixture(t)
	probes := 0
	orig := checkBootstrapServerDB
	checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck {
		probes++
		return bootstrapServerDBCheck{Reachable: true, Exists: false}
	}
	defer func() { checkBootstrapServerDB = orig }()
	var slept time.Duration
	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(d time.Duration) { slept += d }
	defer func() { bootstrapRetryDelay = origDelay }()

	plan := detectBootstrapAction(beadsDir, cfg)
	if plan.Action != "sync" {
		t.Errorf("action = %q, want sync", plan.Action)
	}
	if slept > 0 {
		t.Errorf("fresh clone with no local DB paid %v of transient-restart backoff before sync", slept)
	}
	if probes != 1 {
		t.Errorf("probes = %d, want exactly 1 — a clone with no local shadow dir has nothing to be transiently missing", probes)
	}
}

// TestDetectBootstrapAction_ShadowDirUnreachableServerStillReportsNone pins
// the other side of that rule: with a populated local shadow directory there
// IS local evidence the database lives here, so an unreachable server is a
// live database we cannot see right now and "nothing to do" remains the
// correct answer. This is main's long-standing behavior and must survive the
// narrowing above.
func TestDetectBootstrapAction_ShadowDirUnreachableServerStillReportsNone(t *testing.T) {
	beadsDir, cfg := freshServerModeCloneFixture(t)
	if err := os.MkdirAll(filepath.Join(cfg.DoltDataDir, "mydb"), 0o750); err != nil {
		t.Fatal(err)
	}
	orig := checkBootstrapServerDB
	checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck {
		return bootstrapServerDBCheck{Reachable: false, Err: errors.New("dial tcp 127.0.0.1:3307: connect: connection refused")}
	}
	defer func() { checkBootstrapServerDB = orig }()
	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	defer func() { bootstrapRetryDelay = origDelay }()

	plan := detectBootstrapAction(beadsDir, cfg)
	if plan.Action != "none" {
		t.Errorf("action = %q, want none — a populated shadow dir is local evidence the DB exists, so a down server means 'do nothing', not 'recover'", plan.Action)
	}
	if !strings.Contains(plan.Reason, "Could not verify") {
		t.Errorf("reason = %q, want the 'Could not verify' wording", plan.Reason)
	}
}

// TestDetectBootstrapAction_ExistingProjectMissingServerDBRefusesAtPlanTime
// pins that the refusal is a PLAN, not an execute-time surprise. With every
// recovery source absent the fall-through would be a fresh, empty database;
// because metadata.json proves the workspace was already initialized, the
// plan itself must say "refuse" so --dry-run and --json report it and
// printBootstrapPlan never advertises "will create fresh database".
func TestDetectBootstrapAction_ExistingProjectMissingServerDBRefusesAtPlanTime(t *testing.T) {
	snapshotBootstrapEnv(t)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// No sync.remote, no backup, no issues.jsonl: nothing to recover from.
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize failed: %v", err)
	}
	t.Chdir(tmpDir)
	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "mydb"
	cfg.DoltDataDir = filepath.Join(tmpDir, "dolt-data")
	cfg.ProjectID = "proj-already-initialized-456"
	// A named port gives the plan a server to probe; with none, the refusal
	// comes from the data directory (TestBootstrapNeverDialsAnUnresolvedServerPort).
	cfg.DoltServerPort = 13307
	t.Setenv("BEADS_DOLT_DATA_DIR", cfg.DoltDataDir)

	probes := 0
	orig := checkBootstrapServerDB
	checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck {
		probes++
		return bootstrapServerDBCheck{Reachable: true, Exists: false}
	}
	defer func() { checkBootstrapServerDB = orig }()
	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	defer func() { bootstrapRetryDelay = origDelay }()

	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "refuse" {
		t.Fatalf("action = %q (reason %q), want refuse — an already-initialized workspace must never plan a fresh empty database", plan.Action, plan.Reason)
	}
	if probes != 1 {
		t.Errorf("probes = %d, want exactly 1 — the refusal must reuse the plan-time probe, not dial again", probes)
	}
	if !strings.Contains(plan.Reason, "mydb") {
		t.Errorf("reason = %q does not name the database", plan.Reason)
	}
	if strings.Contains(plan.Reason, "\x1b[") {
		t.Errorf("reason = %q carries ANSI escapes; --json consumers must get plain text", plan.Reason)
	}
	if plan.refusalDetail == "" {
		t.Error("refusalDetail is empty — the operator-facing explanation is lost")
	}
	if !strings.Contains(plan.refusalDetail, "will NOT create") {
		t.Errorf("refusalDetail = %q does not read as a refusal", plan.refusalDetail)
	}

	// The machine-readable surface is what provisioning scripts consume, and
	// "blocked" — not the action string — is the discriminator they are
	// documented to switch on (see the BootstrapPlan.Blocked comment). A
	// refusal produced no database, so it must carry the flag; otherwise it is
	// the one no-database outcome a "blocked"-keyed consumer reads as success.
	if !plan.Blocked {
		t.Error("Blocked = false on a refuse plan — a run that produced no database must set the flag scripts key on")
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("json.Marshal(plan): %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal plan JSON %s: %v", raw, err)
	}
	if decoded["action"] != "refuse" {
		t.Errorf("json action = %v, want refuse", decoded["action"])
	}
	// "blocked" is omitempty, so a false flag does not render as false — the
	// key vanishes entirely and the consumer cannot tell this run from a
	// successful one.
	if decoded["blocked"] != true {
		t.Errorf("json blocked = %v, want true; full plan JSON: %s", decoded["blocked"], raw)
	}
	if strings.Contains(string(raw), "\x1b[") {
		t.Errorf("plan JSON carries ANSI escapes, which --json consumers must never receive: %s", raw)
	}
}

// TestDetectBootstrapAction_RefusePlanDropsUnverifiedSyncRemote pins that a
// refusal honours SyncRemote's documented "empty on a Blocked plan" contract.
// A forge sync.remote with no Dolt data yet is recorded on the plan for the
// workspace bootstrap expected to create (planConfiguredSyncRemote keeps it
// and returns done=false), and the refusal is downstream of that — so without
// explicit clearing the credential-bearing URL rides out in --json for a run
// that wired up nothing, cloned nothing and pushed nothing.
func TestDetectBootstrapAction_RefusePlanDropsUnverifiedSyncRemote(t *testing.T) {
	beadsDir := newForgeSyncRemoteWorkspace(t, "https://github.com/org/repo.git")
	// Forge repo reachable, but no refs/dolt/data yet: sync.remote is recorded
	// and bootstrap keeps looking rather than cloning.
	stubProbeGitRemoteDoltData(t, func(string) (bool, error) { return false, nil })

	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "mydb"
	// Never created: a fresh clone has no local shadow directory.
	cfg.DoltDataDir = filepath.Join(filepath.Dir(beadsDir), "dolt-data")
	cfg.ProjectID = "proj-already-initialized-456"
	t.Setenv("BEADS_DOLT_DATA_DIR", cfg.DoltDataDir)

	orig := checkBootstrapServerDB
	checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck {
		return bootstrapServerDBCheck{Reachable: true, Exists: false}
	}
	defer func() { checkBootstrapServerDB = orig }()
	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	defer func() { bootstrapRetryDelay = origDelay }()

	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "refuse" {
		t.Fatalf("action = %q (reason %q), want refuse", plan.Action, plan.Reason)
	}
	if !plan.Blocked {
		t.Fatal("Blocked = false on a refuse plan")
	}
	if plan.SyncRemote != "" {
		t.Errorf("SyncRemote = %q on a Blocked plan, want empty — the refusal creates no workspace, so nothing may be cloned from or pushed to that remote", plan.SyncRemote)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("json.Marshal(plan): %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal plan JSON %s: %v", raw, err)
	}
	if got, ok := decoded["sync_remote"]; ok {
		t.Errorf("plan JSON carries sync_remote = %v on a Blocked plan; full JSON: %s", got, raw)
	}
}

// TestPrintBootstrapPlan_RefusePrintsSummaryNotDetail covers the refuse arm of
// printBootstrapPlan, which no test reached. The full explanation is the error
// executeBootstrapPlan returns, so printing refusalDetail here too would render
// it twice — a regression a future edit could reintroduce silently.
func TestPrintBootstrapPlan_RefusePrintsSummaryNotDetail(t *testing.T) {
	plan := BootstrapPlan{
		Action:   "refuse",
		Database: "mydb",
		Reason:   `Database "mydb" not found on server at 127.0.0.1:3307; workspace already initialized (project_id set) — refusing to create an empty database`,
		// A marker no production string can produce, so a match is proof the
		// detail field itself was printed rather than wording that overlaps.
		refusalDetail: "REFUSAL-DETAIL-MARKER: bd bootstrap will NOT create an empty database here",
		Blocked:       true,
	}

	out := captureStdout(t, func() error {
		printBootstrapPlan(plan)
		return nil
	})

	if !strings.Contains(out, "refuse — will NOT create a database") {
		t.Errorf("output does not announce the refusal:\n%s", out)
	}
	if !strings.Contains(out, plan.Reason) {
		t.Errorf("output omits the one-line reason:\n%s", out)
	}
	if strings.Contains(out, "REFUSAL-DETAIL-MARKER") {
		t.Errorf("printBootstrapPlan printed refusalDetail; executeBootstrapPlan already returns it, so the operator would read the whole explanation twice:\n%s", out)
	}
}

// TestExecuteBootstrapPlan_RefuseErrsWithoutPrompting pins that a refuse plan
// never reaches confirmPrompt or the workspace gates: it is a decision, not an
// action awaiting approval. Before this, the refusal was discovered inside
// executeInitAction — i.e. after the operator had already answered "Proceed?"
// to a plan that said the opposite.
func TestExecuteBootstrapPlan_RefuseErrsWithoutPrompting(t *testing.T) {
	snapshotBootstrapEnv(t)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "mydb"
	cfg.ProjectID = "proj-existing-123"

	probes := 0
	orig := checkBootstrapServerDB
	checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck {
		probes++
		return bootstrapServerDBCheck{Reachable: true, Exists: false}
	}
	defer func() { checkBootstrapServerDB = orig }()

	plan := BootstrapPlan{
		BeadsDir:      beadsDir,
		Database:      "mydb",
		Action:        "refuse",
		Reason:        "short summary",
		refusalDetail: "long refusal: bd bootstrap will NOT create an empty database here",
	}
	// nonInteractive=false so a confirmPrompt would be reached if one ran.
	err := executeBootstrapPlan(plan, cfg, false)
	if err == nil {
		t.Fatal("executeBootstrapPlan returned nil for a refuse plan — bootstrap would exit 0 having done nothing, hiding the refusal")
	}
	if !strings.Contains(err.Error(), "will NOT create") {
		t.Errorf("error = %q, want the refusalDetail text", err.Error())
	}
	if probes != 0 {
		t.Errorf("probes = %d, want 0 — the decision was already made at plan time", probes)
	}
}

// TestDetectBootstrapAction_FreshCloneServerNameMatchStillSyncs pins the
// POSITIVE half of the widened probe. Unlocking the server-side existence
// check for ProjectID-bearing workspaces also unlocked its Exists==true arm,
// and a reachable server merely holding a database of the configured NAME is
// not proof that this workspace is bound to it — nothing checks project_id or
// content. Before the widening such a clone never probed at all and always
// fell through to sync.remote, so a stale or unrelated same-named database
// (including one left behind by the very bug this guard fixes) must not
// silently settle the plan as "nothing to do" and suppress the configured
// recovery clone. The name match survives only as the fallback verdict, which
// TestDetectBootstrapAction_NoLocalShadowDirStillLiveChecksExisting pins.
func TestDetectBootstrapAction_FreshCloneServerNameMatchStillSyncs(t *testing.T) {
	beadsDir, cfg := freshServerModeCloneFixture(t)
	orig := checkBootstrapServerDB
	checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck {
		return bootstrapServerDBCheck{Reachable: true, Exists: true}
	}
	defer func() { checkBootstrapServerDB = orig }()
	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	defer func() { bootstrapRetryDelay = origDelay }()

	plan := detectBootstrapAction(beadsDir, cfg)
	if plan.Action != "sync" {
		t.Errorf("action = %q (reason %q), want sync — a server-side name match with no local evidence binding this workspace to that database must not mask configured sync.remote recovery", plan.Action, plan.Reason)
	}
}

// TestDetectBootstrapAction_ShadowDirServerNameMatchSettlesNone pins the other
// half of the same gate: a populated local shadow directory IS evidence this
// workspace is bound to the database the server confirmed, so the Exists
// verdict must settle the plan immediately instead of being deferred behind
// sync.remote. Deferring it would re-clone over a database that already exists
// — GH#5037's Error 1007, which the comment above the settle gate exists to
// prevent — and the fixture configures a sync.remote precisely so the
// assertion discriminates: without the local-evidence half of
// settledOnNameAlone, sync.remote wins and the action becomes "sync". The
// Err-arm twin is TestDetectBootstrapAction_ShadowDirUnreachableServerStillReportsNone;
// this is the Exists-arm one, which nothing covered.
func TestDetectBootstrapAction_ShadowDirServerNameMatchSettlesNone(t *testing.T) {
	beadsDir, cfg := freshServerModeCloneFixture(t)
	if err := os.MkdirAll(filepath.Join(cfg.DoltDataDir, "mydb"), 0o750); err != nil {
		t.Fatal(err)
	}
	orig := checkBootstrapServerDB
	checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck {
		return bootstrapServerDBCheck{Reachable: true, Exists: true}
	}
	defer func() { checkBootstrapServerDB = orig }()
	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	defer func() { bootstrapRetryDelay = origDelay }()

	plan := detectBootstrapAction(beadsDir, cfg)
	if plan.Action != "none" {
		t.Errorf("action = %q (reason %q), want none — with local evidence binding this workspace to the database the server reports, re-cloning it is GH#5037's Error 1007", plan.Action, plan.Reason)
	}
	if !plan.HasExisting {
		t.Error("HasExisting = false on a confirmed server-side database with local evidence")
	}
	// Discriminates the Exists arm from the Err arm's "Could not verify"
	// settle, which the same populated shadow directory also unlocks.
	if !strings.Contains(plan.Reason, "already exists on server") {
		t.Errorf("reason = %q, want the Exists-arm wording — a 'Could not verify' reason here means the probe result, not the local evidence, is what settled the plan", plan.Reason)
	}
}

// TestDetectBootstrapAction_ShadowDirSettlesForEveryHost pins the settle gate
// of the two tests above for every configured host, not only bd's own. A
// populated local data directory is the evidence bd bootstrap has always used
// to settle a re-run on an existing workspace (GH#5037), whatever host the
// workspace dials: a team that moved from bd's own server to one it runs at
// ::1 or on another machine, and kept .beads/dolt, re-runs bd bootstrap and
// must get "nothing to do", not a DOLT_CLONE over the server's existing
// database (Error 1007) or a JSONL import into a new embedded database beside
// it. sync.remote is configured so a deferred verdict shows up as "sync".
func TestDetectBootstrapAction_ShadowDirSettlesForEveryHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "[::1]", "127.0.0.2", "db.example.com"} {
		for _, arm := range []string{"exists", "unreachable"} {
			t.Run(host+"/"+arm, func(t *testing.T) {
				beadsDir, cfg := freshServerModeCloneFixture(t)
				cfg.DoltServerHost = host
				t.Setenv("BEADS_DOLT_SERVER_PORT", "23417")
				if err := os.MkdirAll(filepath.Join(cfg.DoltDataDir, "mydb", ".dolt"), 0o750); err != nil {
					t.Fatal(err)
				}
				orig := checkBootstrapServerDB
				checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck {
					if arm == "exists" {
						return bootstrapServerDBCheck{Reachable: true, Exists: true}
					}
					return bootstrapServerDBCheck{Reachable: false, Err: errors.New("dial tcp: connect: connection refused")}
				}
				defer func() { checkBootstrapServerDB = orig }()
				origDelay := bootstrapRetryDelay
				bootstrapRetryDelay = func(time.Duration) {}
				defer func() { bootstrapRetryDelay = origDelay }()

				plan := detectBootstrapAction(beadsDir, cfg)
				if plan.Action != "none" {
					t.Fatalf("action = %q (reason %q), want none: the populated local data directory settles a re-run, as it does for bd's own host", plan.Action, plan.Reason)
				}
				if arm == "exists" && !plan.HasExisting {
					t.Error("HasExisting = false on a confirmed server-side database with local evidence")
				}
			})
		}
	}
}

// loadClonePortConfig makes beadsDir's config.yaml the one the store reads
// dolt.port from.
func loadClonePortConfig(t *testing.T, beadsDir string) {
	t.Helper()
	t.Setenv("BEADS_DIR", beadsDir)
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
}

func writeClonePortFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeClonePortCredentials points BEADS_CREDENTIALS_FILE at a credentials
// file with a section for every host and port pair, whose password is the
// pair itself ("host:port"), so a password names the server it is for.
func writeClonePortCredentials(t *testing.T, hosts []string, ports []int) {
	t.Helper()
	var body strings.Builder
	for _, host := range hosts {
		for _, port := range ports {
			fmt.Fprintf(&body, "[%s:%d]\npassword=%s:%d\n\n", host, port, host, port)
		}
	}
	path := filepath.Join(t.TempDir(), "credentials")
	writeClonePortFile(t, path, body.String())
	t.Setenv("BEADS_CREDENTIALS_FILE", path)
}

// TestServerClonePortIsTheStorePort pins the port bd bootstrap clones
// through on an external Dolt server to the one the store dials in that
// workspace once metadata.json holds the config bootstrap writes, so the clone
// lands where every later command looks: DialConfig's rule, for a server on
// another machine. A fresh clone writes metadata.json only after the clone,
// so its config's dolt_server_port must still take the file's rank.
func TestServerClonePortIsTheStorePort(t *testing.T) {
	const legacy, serverEnv, configured, metadata = 13307, 13400, 15555, 14444
	doltPort := func(t *testing.T, beadsDir string) {
		writeClonePortFile(t, filepath.Join(beadsDir, "config.yaml"), fmt.Sprintf("dolt.port: %d\n", configured))
	}
	listenerPort := func(t *testing.T, beadsDir string) {
		writeClonePortFile(t, filepath.Join(beadsDir, "dolt", "config.yaml"), fmt.Sprintf("listener:\n  port: %d\n", configured))
	}
	localPort := func(t *testing.T, beadsDir string) {
		writeClonePortFile(t, filepath.Join(beadsDir, "config.local.yaml"), fmt.Sprintf("dolt.port: %d\n", configured))
	}
	for _, tc := range []struct {
		name       string
		serverPort int // BEADS_DOLT_SERVER_PORT
		legacyPort int // BEADS_DOLT_PORT
		metaPort   int // dolt_server_port in the config bootstrap writes
		fresh      bool
		shared     bool // BEADS_DOLT_SHARED_SERVER=1
		setup      func(t *testing.T, beadsDir string)
		want       int
	}{
		{name: "config.yaml dolt.port over BEADS_DOLT_PORT", legacyPort: legacy, setup: doltPort, want: configured},
		{name: "listener.port over BEADS_DOLT_PORT", legacyPort: legacy, setup: listenerPort, want: configured},
		{name: "config.local.yaml dolt.port over BEADS_DOLT_PORT", legacyPort: legacy, setup: localPort, want: configured},
		{name: "config.local.yaml dolt.port alone", setup: localPort, want: configured},
		{name: "BEADS_DOLT_SERVER_PORT over dolt_server_port", serverPort: serverEnv, metaPort: metadata, want: serverEnv},
		{name: "dolt_server_port over BEADS_DOLT_PORT", legacyPort: legacy, metaPort: metadata, want: metadata},
		{name: "config.yaml dolt.port over dolt_server_port", metaPort: metadata, setup: doltPort, want: configured},
		{name: "BEADS_DOLT_PORT alone", legacyPort: legacy, want: legacy},
		// No default 3307 stands in: later commands dial no such port.
		{name: "nothing named", want: 0},
		{name: "fresh clone/inherited dolt_server_port over BEADS_DOLT_PORT", fresh: true, legacyPort: legacy, metaPort: metadata, want: metadata},
		{name: "fresh clone/inherited dolt_server_port alone", fresh: true, metaPort: metadata, want: metadata},
		{name: "fresh clone/BEADS_DOLT_SERVER_PORT over inherited dolt_server_port", fresh: true, serverPort: serverEnv, metaPort: metadata, want: serverEnv},
		{name: "fresh clone/config.yaml dolt.port over inherited dolt_server_port", fresh: true, metaPort: metadata, setup: doltPort, want: configured},
		{name: "shared server/dolt_server_port not read", shared: true, metaPort: metadata, want: doltserver.DefaultSharedServerPort},
		{name: "shared server/fresh clone/inherited dolt_server_port not read", shared: true, fresh: true, metaPort: metadata, want: doltserver.DefaultSharedServerPort},
		{name: "shared server/BEADS_DOLT_PORT over the shared default", shared: true, legacyPort: legacy, metaPort: metadata, want: legacy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clonePortTestEnv(t)
			if tc.shared {
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
			}
			if tc.serverPort > 0 {
				t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(tc.serverPort))
			}
			if tc.legacyPort > 0 {
				t.Setenv("BEADS_DOLT_PORT", strconv.Itoa(tc.legacyPort))
			}
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			writeClonePortFile(t, filepath.Join(beadsDir, "config.yaml"), "# Beads Config\n")
			if tc.setup != nil {
				tc.setup(t, beadsDir)
			}
			loadClonePortConfig(t, beadsDir)
			cfg := &configfile.Config{
				Backend:        configfile.BackendDolt,
				DoltMode:       configfile.DoltModeServer,
				DoltServerHost: "db.example.com",
				DoltServerPort: tc.metaPort,
				DoltDatabase:   "clone_port",
			}
			if !tc.fresh {
				if err := cfg.Save(beadsDir); err != nil {
					t.Fatal(err)
				}
			}

			got := serverClonePort(beadsDir, cfg)

			if tc.fresh {
				// What finalizeSyncedBootstrap writes before the store opens.
				if err := cfg.Save(beadsDir); err != nil {
					t.Fatal(err)
				}
			}
			dial := doltserver.DialConfig(beadsDir)
			if got != tc.want {
				t.Errorf("serverClonePort = %d, want %d", got, tc.want)
			}
			if got != dial.Port {
				t.Errorf("serverClonePort = %d, but the store dials %d (%s)", got, dial.Port, dial.PortSource)
			}
		})
	}
}

// TestCloneFromRemoteServerPortFollowsTheCaller pins which port each caller's
// server clone dials when config.yaml dolt.port and the legacy BEADS_DOLT_PORT
// disagree. bd bootstrap clones into the workspace, whose later commands
// connect on dolt.port. bd init --server --external opens its own store with
// BEADS_DOLT_PORT first, so its clone stays there.
func TestCloneFromRemoteServerPortFollowsTheCaller(t *testing.T) {
	clonePortTestEnv(t)
	configured := freeLoopbackPort(t)
	legacy := freeLoopbackPort(t)
	for legacy == configured {
		legacy = freeLoopbackPort(t)
	}
	t.Setenv("BEADS_DOLT_PORT", strconv.Itoa(legacy))
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	writeClonePortFile(t, filepath.Join(beadsDir, "config.yaml"), fmt.Sprintf("dolt.port: %d\n", configured))
	loadClonePortConfig(t, beadsDir)
	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltDatabase:   "clone_port",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	const remote = "file:///nonexistent/remote"

	for _, tc := range []struct {
		name  string
		clone func() error
		want  int
	}{
		{name: "bd bootstrap", want: configured, clone: func() error {
			return cloneFromRemote(t.Context(), beadsDir, remote, "clone_port", cfg)
		}},
		{name: "bd init --server --external", want: legacy, clone: func() error {
			initCfg := initTimeCloneConfig(true, "", 0, "", "", "clone_port")
			return cloneFromRemoteWithMode(t.Context(), beadsDir, remote, "clone_port", initCfg, remoteCloneExternalServer)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.clone()
			if err == nil {
				t.Fatal("clone succeeded with no server running")
			}
			want := "dolt server unreachable at " + net.JoinHostPort("127.0.0.1", strconv.Itoa(tc.want))
			if !strings.Contains(err.Error(), want) {
				t.Errorf("clone error = %v, want it to name %s", err, want)
			}
		})
	}
}

// TestServerClonePortIgnoresAnAncestorDoltPort covers a fresh rig with no
// .beads yet, inside a town but bounded by its own git repository (GH#3029,
// GH#2792): config loads the town's config.yaml, so DialConfig sees the
// town's dolt.port or dolt.shared-server. finalizeSyncedBootstrap then writes
// the rig's own config.yaml, which names neither, and later commands dial by
// the rig's own sources: that file, the user config files (the legacy
// ~/.beads/config.yaml among them) and the BD_* variables. The clone must
// land there too.
func TestServerClonePortIgnoresAnAncestorDoltPort(t *testing.T) {
	const legacy, townPort, metadata, bdPort, userPort, sharedPort = 13307, 15555, 14444, 16666, 15566, 17777
	userDoltPort := fmt.Sprintf("dolt.port: %d\n", userPort)
	userShared := "dolt.shared-server: true\n"
	for _, tc := range []struct {
		name       string
		committed  bool   // the rig commits metadata.json (defensive: the CLI pins BEADS_DIR to it)
		townShared bool   // the town's config.yaml sets dolt.shared-server instead of dolt.port
		plainTown  bool   // the town's config.yaml names no dolt key
		noMetaPort bool   // the rig's config names no dolt_server_port
		legacyPort int    // BEADS_DOLT_PORT
		bdDoltPort int    // BD_DOLT_PORT
		bdShared   bool   // BD_DOLT_SHARED_SERVER=1
		userLegacy string // the legacy ~/.beads/config.yaml
		sharedFile bool   // a running shared server's port file names sharedPort
		want       int
	}{
		{name: "fresh clone", want: metadata},
		{name: "committed metadata.json (defensive)", committed: true, want: metadata},
		{name: "fresh clone/inherited dolt_server_port over BEADS_DOLT_PORT", legacyPort: legacy, want: metadata},
		{name: "fresh clone/BEADS_DOLT_PORT without dolt_server_port", noMetaPort: true, legacyPort: legacy, want: legacy},
		{name: "fresh clone/BD_DOLT_PORT", bdDoltPort: bdPort, want: bdPort},
		{name: "fresh clone/BD_DOLT_PORT under a town shared server", townShared: true, sharedFile: true, bdDoltPort: bdPort, want: bdPort},
		{name: "fresh clone/town shared-server", townShared: true, want: metadata},
		{name: "fresh clone/BD_DOLT_SHARED_SERVER", bdShared: true, want: doltserver.DefaultSharedServerPort},
		{name: "fresh clone/legacy user dolt.port", plainTown: true, noMetaPort: true, userLegacy: userDoltPort, want: userPort},
		{name: "fresh clone/legacy user dolt.port over inherited dolt_server_port", plainTown: true, userLegacy: userDoltPort, want: userPort},
		{name: "fresh clone/legacy user dolt.port over the town's", userLegacy: userDoltPort, want: userPort},
		{name: "fresh clone/legacy user dolt.shared-server", plainTown: true, noMetaPort: true, userLegacy: userShared, want: doltserver.DefaultSharedServerPort},
		{name: "fresh clone/legacy user dolt.shared-server over inherited dolt_server_port", plainTown: true, userLegacy: userShared, want: doltserver.DefaultSharedServerPort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clonePortTestEnv(t)
			t.Setenv("BEADS_CEILING_DIRECTORIES", "")
			if tc.userLegacy != "" {
				writeClonePortFile(t, filepath.Join(os.Getenv("HOME"), ".beads", "config.yaml"), tc.userLegacy)
			}
			if tc.sharedFile {
				writeClonePortFile(t, filepath.Join(os.Getenv("BEADS_SHARED_SERVER_DIR"), doltserver.PortFileName), strconv.Itoa(sharedPort))
			}
			if tc.bdShared {
				t.Setenv("BD_DOLT_SHARED_SERVER", "1")
			}
			if tc.legacyPort > 0 {
				t.Setenv("BEADS_DOLT_PORT", strconv.Itoa(tc.legacyPort))
			}
			if tc.bdDoltPort > 0 {
				t.Setenv("BD_DOLT_PORT", strconv.Itoa(tc.bdDoltPort))
			}
			town := t.TempDir()
			townConfig := fmt.Sprintf("dolt.port: %d\n", townPort)
			switch {
			case tc.townShared:
				townConfig = "dolt.shared-server: true\n"
			case tc.plainTown:
				townConfig = "issue-prefix: town\n"
			}
			writeClonePortFile(t, filepath.Join(town, ".beads", "config.yaml"), townConfig)
			rig := filepath.Join(town, "rig")
			beadsDir := filepath.Join(rig, ".beads")
			if err := os.MkdirAll(rig, 0o700); err != nil {
				t.Fatal(err)
			}
			cfg := &configfile.Config{
				Backend:        configfile.BackendDolt,
				DoltMode:       configfile.DoltModeServer,
				DoltServerHost: "db.example.com",
				DoltServerPort: metadata,
				DoltDatabase:   "rig_clone_port",
			}
			if tc.noMetaPort {
				cfg.DoltServerPort = 0
			}
			if tc.committed {
				if err := os.MkdirAll(beadsDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := cfg.Save(beadsDir); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(rig)
			t.Setenv("BEADS_DIR", "")
			config.ResetForTesting()
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}
			before := doltserver.DialConfig(beadsDir)
			switch {
			case tc.townShared && !before.PortSharedServer:
				t.Fatalf("precondition: DialConfig before bootstrap is not in shared-server mode (port %d, %s)", before.Port, before.PortSource)
			case !tc.townShared && !tc.plainTown && !tc.bdShared && tc.bdDoltPort == 0 && before.Port != townPort:
				t.Fatalf("precondition: DialConfig before bootstrap = %d (%s), want the town's dolt.port %d", before.Port, before.PortSource, townPort)
			}

			got := serverClonePort(beadsDir, cfg)

			// What finalizeSyncedBootstrap writes before the next command.
			if err := os.MkdirAll(beadsDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := cfg.Save(beadsDir); err != nil {
				t.Fatal(err)
			}
			if err := createConfigYaml(beadsDir, false, ""); err != nil {
				t.Fatal(err)
			}
			config.ResetForTesting()
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}
			dial := doltserver.DialConfig(beadsDir)
			if got != tc.want {
				t.Errorf("serverClonePort = %d, want %d", got, tc.want)
			}
			if got != dial.Port {
				t.Errorf("serverClonePort = %d, but later commands in the rig dial %d (%s)", got, dial.Port, dial.PortSource)
			}
		})
	}
}

// TestServerClonePortReadsTheUserLayers covers a fresh clone with no .beads
// and no enclosing workspace, whose port or shared-server mode comes from a
// layer every later command reads through config: BD_DOLT_SHARED_SERVER, the
// documented user config, or the legacy ~/.beads/config.yaml (which the
// directory walk also loads as the project file for a repository under
// HOME). None of these is an enclosing workspace's config, so the clone port
// must stay the one the store dials.
func TestServerClonePortReadsTheUserLayers(t *testing.T) {
	const port, metadata = 15555, 14444
	userFile := func(rel, body string) func(t *testing.T, home string) {
		return func(t *testing.T, home string) {
			writeClonePortFile(t, filepath.Join(home, rel), body)
		}
	}
	legacyPort := userFile(filepath.Join(".beads", "config.yaml"), fmt.Sprintf("dolt.port: %d\n", port))
	legacyShared := userFile(filepath.Join(".beads", "config.yaml"), "dolt.shared-server: true\n")
	documentedPort := userFile(filepath.Join(".config", "bd", "config.yaml"), fmt.Sprintf("dolt.port: %d\n", port))
	documentedShared := userFile(filepath.Join(".config", "bd", "config.yaml"), "dolt.shared-server: true\n")
	bdShared := func(value string) func(t *testing.T, home string) {
		return func(t *testing.T, home string) { t.Setenv("BD_DOLT_SHARED_SERVER", value) }
	}
	shared := doltserver.DefaultSharedServerPort
	for _, tc := range []struct {
		name      string
		setup     []func(t *testing.T, home string)
		underHome bool // the repository lives under HOME
		localHost bool // 127.0.0.1 instead of a remote host
		metaPort  int  // dolt_server_port in the config bootstrap writes
		want      int
	}{
		{name: "BD_DOLT_SHARED_SERVER", setup: []func(*testing.T, string){bdShared("1")}, want: shared},
		{name: "BD_DOLT_SHARED_SERVER/local host", setup: []func(*testing.T, string){bdShared("1")}, localHost: true, want: shared},
		{name: "BD_DOLT_SHARED_SERVER=false over the user config", setup: []func(*testing.T, string){documentedShared, bdShared("false")}, want: 0},
		{name: "legacy dolt.port", setup: []func(*testing.T, string){legacyPort}, want: port},
		{name: "legacy dolt.port over dolt_server_port", setup: []func(*testing.T, string){legacyPort}, metaPort: metadata, want: port},
		{name: "legacy dolt.shared-server", setup: []func(*testing.T, string){legacyShared}, want: shared},
		{name: "legacy dolt.shared-server/local host", setup: []func(*testing.T, string){legacyShared}, localHost: true, want: shared},
		{name: "repo under HOME/legacy dolt.port", setup: []func(*testing.T, string){legacyPort}, underHome: true, want: port},
		{name: "repo under HOME/legacy dolt.shared-server", setup: []func(*testing.T, string){legacyShared}, underHome: true, want: shared},
		{name: "user config dolt.port", setup: []func(*testing.T, string){documentedPort}, want: port},
		{name: "user config dolt.shared-server", setup: []func(*testing.T, string){documentedShared}, want: shared},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clonePortTestEnv(t)
			t.Setenv("BEADS_CEILING_DIRECTORIES", "")
			home := os.Getenv("HOME")
			for _, setup := range tc.setup {
				setup(t, home)
			}
			repo := filepath.Join(t.TempDir(), "repo")
			if tc.underHome {
				repo = filepath.Join(home, "code", "repo")
			}
			if err := os.MkdirAll(repo, 0o700); err != nil {
				t.Fatal(err)
			}
			beadsDir := filepath.Join(repo, ".beads")
			host := "db.example.com"
			if tc.localHost {
				host = "127.0.0.1"
			}
			cfg := &configfile.Config{
				Backend:        configfile.BackendDolt,
				DoltMode:       configfile.DoltModeServer,
				DoltServerHost: host,
				DoltServerPort: tc.metaPort,
				DoltDatabase:   "user_clone_port",
			}
			t.Chdir(repo)
			t.Setenv("BEADS_DIR", "")
			config.ResetForTesting()
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			got := serverClonePort(beadsDir, cfg)

			// What finalizeSyncedBootstrap writes before the next command,
			// which the CLI runs with BEADS_DIR pinned to it.
			if err := os.MkdirAll(beadsDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := cfg.Save(beadsDir); err != nil {
				t.Fatal(err)
			}
			if err := createConfigYaml(beadsDir, false, ""); err != nil {
				t.Fatal(err)
			}
			loadClonePortConfig(t, beadsDir)
			dial := doltserver.DialConfig(beadsDir)
			if got != tc.want {
				t.Errorf("serverClonePort = %d, want %d", got, tc.want)
			}
			if got != dial.Port {
				t.Errorf("serverClonePort = %d, but later commands dial %d (%s)", got, dial.Port, dial.PortSource)
			}
		})
	}
}

// TestDetectBootstrapActionProbesTheClonePort pins the existence probe that
// decides whether bd bootstrap clones at all, and the endpoint its "already
// exists" reason and its refusal name, to bootstrapServerEndpoint: the
// endpoint the clone dials, on the port every later command dials. The probe
// used to resolve its own port order, without listener.port or the
// shared-server BEADS_DOLT_PORT rule, so a workspace with a populated data
// directory could settle "already exists" against a server later commands
// never dial. It also took its password for another port and left out the
// socket, so with a per-port credentials entry it could not authenticate on
// the server the clone dials.
func TestDetectBootstrapActionProbesTheClonePort(t *testing.T) {
	const listener, metadata, legacy = 15555, 14444, 13307
	const socket = "/run/dolt/probe_port.sock"
	for _, tc := range []struct {
		name   string
		shared bool // BEADS_DOLT_SHARED_SERVER=1 with BEADS_DOLT_PORT, instead of listener.port
		exists bool // the probe finds the database
		want   int
	}{
		{name: "listener.port over dolt_server_port/exists", exists: true, want: listener},
		{name: "listener.port over dolt_server_port/refused", want: listener},
		{name: "shared server/BEADS_DOLT_PORT over the shared default/exists", shared: true, exists: true, want: legacy},
		{name: "shared server/BEADS_DOLT_PORT over the shared default/refused", shared: true, want: legacy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clonePortTestEnv(t)
			repo := t.TempDir()
			t.Chdir(repo)
			beadsDir := filepath.Join(repo, ".beads")
			writeClonePortFile(t, filepath.Join(beadsDir, "config.yaml"), "# Beads Config\n")
			cfg := &configfile.Config{
				Backend:          configfile.BackendDolt,
				DoltMode:         configfile.DoltModeServer,
				DoltServerHost:   "127.0.0.1",
				DoltServerPort:   metadata,
				DoltServerSocket: socket,
				DoltDatabase:     "probe_port",
				ProjectID:        "probe-port-project",
			}
			if err := cfg.Save(beadsDir); err != nil {
				t.Fatal(err)
			}
			writeClonePortCredentials(t, []string{"127.0.0.1"}, []int{listener, metadata, legacy})
			// Each shape also populates the data directory the probe takes as
			// evidence that the database lives here.
			if tc.shared {
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
				t.Setenv("BEADS_DOLT_PORT", strconv.Itoa(legacy))
				writeClonePortFile(t, filepath.Join(os.Getenv("BEADS_SHARED_SERVER_DIR"), "dolt", "probe_port", "marker"), "")
			} else {
				writeClonePortFile(t, filepath.Join(beadsDir, "dolt", "config.yaml"), fmt.Sprintf("listener:\n  port: %d\n", listener))
			}
			loadClonePortConfig(t, beadsDir)
			var probed []bootstrapServerProbeConfig
			origCheck := checkBootstrapServerDB
			checkBootstrapServerDB = func(probeCfg bootstrapServerProbeConfig) bootstrapServerDBCheck {
				probed = append(probed, probeCfg)
				return bootstrapServerDBCheck{Exists: tc.exists, Reachable: true}
			}
			t.Cleanup(func() { checkBootstrapServerDB = origCheck })
			origDelay := bootstrapRetryDelay
			bootstrapRetryDelay = func(time.Duration) {}
			t.Cleanup(func() { bootstrapRetryDelay = origDelay })

			plan := detectBootstrapAction(beadsDir, cfg)

			if len(probed) == 0 {
				t.Fatalf("the server was never probed (plan %q: %s)", plan.Action, plan.Reason)
			}
			clone := bootstrapServerEndpoint(beadsDir, cfg)
			for _, ep := range probed {
				if ep.port != tc.want {
					t.Errorf("probe dialed port %d, want %d", ep.port, tc.want)
				}
				if want := fmt.Sprintf("127.0.0.1:%d", tc.want); ep.pass != want {
					t.Errorf("probe password = %q, want the credentials entry for the port it dials, %q", ep.pass, want)
				}
				if ep.socket != socket {
					t.Errorf("probe socket = %q, want the configured %q", ep.socket, socket)
				}
				if ep != clone {
					t.Errorf("probe dialed %+v, but the clone dials %+v", ep, clone)
				}
			}
			wantAction := "refuse"
			if tc.exists {
				wantAction = "none"
			}
			if plan.Action != wantAction {
				t.Fatalf("action = %q (%s), want %q", plan.Action, plan.Reason, wantAction)
			}
			if endpoint := net.JoinHostPort("127.0.0.1", strconv.Itoa(tc.want)); !strings.Contains(plan.Reason, endpoint) {
				t.Errorf("reason = %q, want it to name %s", plan.Reason, endpoint)
			}
		})
	}
}

// TestInitServerClonePortIsInitsStorePort pins the port bd init --server
// --external clones through to the port of the store init opens right after
// the clone: --server-port, or initStoreServerPort's port, with the store's
// rule (dolt.ResolveServerPort) applied. In shared-server mode that store
// reads no config.yaml dolt.port, so neither does the clone.
func TestInitServerClonePortIsInitsStorePort(t *testing.T) {
	const configured, legacy, serverEnv, portFile, flag = 15555, 13307, 13400, 16000, 13500
	for _, tc := range []struct {
		name          string
		shared        bool // config.yaml dolt.shared-server
		doltPort      bool // config.yaml dolt.port
		serverPort    int  // --server-port
		serverEnvPort int  // BEADS_DOLT_SERVER_PORT
		legacyPort    int  // BEADS_DOLT_PORT
		portFile      bool // the workspace's port file
		want          int
	}{
		{name: "shared server/dolt.port not read", shared: true, doltPort: true, want: doltserver.DefaultSharedServerPort},
		{name: "shared server/BEADS_DOLT_PORT over the shared default", shared: true, doltPort: true, legacyPort: legacy, want: legacy},
		{name: "shared server/port file", shared: true, doltPort: true, portFile: true, want: portFile},
		{name: "shared server/BEADS_DOLT_SERVER_PORT", shared: true, doltPort: true, serverEnvPort: serverEnv, want: serverEnv},
		{name: "--server-port over BEADS_DOLT_SERVER_PORT", shared: true, serverPort: flag, serverEnvPort: serverEnv, want: flag},
		{name: "BEADS_DOLT_PORT over dolt.port", doltPort: true, legacyPort: legacy, want: legacy},
		{name: "BEADS_DOLT_PORT over the port file", portFile: true, legacyPort: legacy, want: legacy},
		{name: "dolt.port when nothing else names one", doltPort: true, want: configured},
		// Nothing names a port: the store refuses the unresolved port (or
		// starts bd's own server), and dials no default 3307, so neither
		// does the clone.
		{name: "nothing names a port", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clonePortTestEnv(t)
			if tc.serverEnvPort > 0 {
				t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(tc.serverEnvPort))
			}
			if tc.legacyPort > 0 {
				t.Setenv("BEADS_DOLT_PORT", strconv.Itoa(tc.legacyPort))
			}
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			body := "# Beads Config\n"
			if tc.shared {
				body += "dolt.shared-server: true\n"
			}
			if tc.doltPort {
				body += fmt.Sprintf("dolt.port: %d\n", configured)
			}
			writeClonePortFile(t, filepath.Join(beadsDir, "config.yaml"), body)
			if tc.portFile {
				writeClonePortFile(t, filepath.Join(beadsDir, doltserver.PortFileName), strconv.Itoa(portFile))
			}
			loadClonePortConfig(t, beadsDir)

			got := initServerClonePort(beadsDir, initTimeCloneConfig(true, "", tc.serverPort, "", "", "init_clone_port"))

			if got != tc.want {
				t.Errorf("initServerClonePort = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestWarmUpSyncedStoreOpensWhereTheCloneLanded covers the open that follows a
// fresh rig's clone, in the same process, still with the town's config.yaml
// loaded: it must dial the host and port the clone dialed, which later
// commands in the rig dial too, not the town's dolt.host and dolt.port. A
// warmup on the town's server misses the wisp tables and could open a
// same-named database there.
func TestWarmUpSyncedStoreOpensWhereTheCloneLanded(t *testing.T) {
	const townHost, townPort, metadata = "db.town.example", 15555, 14444
	clonePortTestEnv(t)
	t.Setenv("BEADS_CEILING_DIRECTORIES", "")
	town := t.TempDir()
	writeClonePortFile(t, filepath.Join(town, ".beads", "config.yaml"), fmt.Sprintf("dolt.host: %s\ndolt.port: %d\n", townHost, townPort))
	rig := filepath.Join(town, "rig")
	if err := os.MkdirAll(rig, 0o700); err != nil {
		t.Fatal(err)
	}
	beadsDir := filepath.Join(rig, ".beads")
	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerPort: metadata,
		DoltDatabase:   "rig_warmup",
	}
	t.Chdir(rig)
	t.Setenv("BEADS_DIR", "")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	clone := bootstrapServerEndpoint(beadsDir, cfg)
	if clone.host != "127.0.0.1" || clone.port != metadata {
		t.Fatalf("precondition: the clone dials %s:%d, want the rig's 127.0.0.1 and the inherited dolt_server_port %d", clone.host, clone.port, metadata)
	}
	// What finalizeSyncedBootstrap writes before the warmup.
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	if err := createConfigYaml(beadsDir, false, ""); err != nil {
		t.Fatal(err)
	}
	warmupHost, warmupPort := "", 0
	errStub := errors.New("stub store")
	origOpen := openSyncedWarmupStore
	openSyncedWarmupStore = func(_ context.Context, dir string) (storage.DoltStorage, error) {
		warmupHost, warmupPort = warmupStoreEndpoint(t, dir)
		return nil, errStub
	}
	t.Cleanup(func() { openSyncedWarmupStore = origOpen })

	if err := warmUpSyncedStore(t.Context(), BootstrapPlan{BeadsDir: beadsDir}); !errors.Is(err, errStub) {
		t.Fatalf("warmUpSyncedStore = %v, want the store open's error", err)
	}

	if warmupHost != clone.host || warmupPort != clone.port {
		t.Errorf("warmup dials %s:%d, but the clone dialed %s:%d", warmupHost, warmupPort, clone.host, clone.port)
	}
	if got := config.GetYamlConfig("dolt.port"); got != strconv.Itoa(townPort) {
		t.Errorf("after the warmup, the loaded dolt.port = %q, want the town's %d back", got, townPort)
	}
	if got := config.GetYamlConfig("dolt.host"); got != townHost {
		t.Errorf("after the warmup, the loaded dolt.host = %q, want the town's %q back", got, townHost)
	}
}

// warmupStoreEndpoint is the host and port of the store opened in dir under
// the configuration loaded now: metadata.json's host, as the store takes it
// (GetDoltServerHost), and DialConfig's port.
func warmupStoreEndpoint(t *testing.T, dir string) (string, int) {
	t.Helper()
	fileCfg, err := configfile.Load(dir)
	if err != nil || fileCfg == nil {
		t.Fatalf("no metadata.json in %s for the warmup to open: %v", dir, err)
	}
	return fileCfg.GetDoltServerHost(), doltserver.DialConfig(dir).Port
}

// TestSyncActionClonesWhereTheRigsCommandsLook runs bd bootstrap's sync action
// for a fresh rig with no .beads yet, inside a town but bounded by its own git
// repository (GH#3029, GH#2792), whose config.yaml names a Dolt server or
// mode. Config loads the town's config.yaml, but the rig's later commands read
// only the rig's own configuration and the user config files. Whether the
// clone goes to a server or the embedded engine, the server it dials and the
// password it presents there, the dolt_mode finalizeSyncedBootstrap records,
// and the server the warmup opens must all be the ones those commands use, and
// the existence probe (bootstrapServerEndpoint) must look where the clone
// lands.
func TestSyncActionClonesWhereTheRigsCommandsLook(t *testing.T) {
	const townHost, userHost, rigHost, townPort, metadata = "db.town.example", "db.user.example", "db.rig.example", 15555, 14444
	townShared := "dolt.shared-server: true\n"
	townServer := fmt.Sprintf("dolt.host: %s\ndolt.port: %d\n", townHost, townPort)
	server := func(port int) *configfile.Config {
		return &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltServerPort: port}
	}
	embedded := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeEmbedded}
	for _, tc := range []struct {
		name     string
		town     string // the town's config.yaml
		townKey  string // a key of it that config loads before the clone
		userHost string // dolt.host in the user config
		cfg      *configfile.Config
		// wantHost and wantPort name the server the clone dials; empty for
		// a clone into the embedded engine or into bd's own data directory.
		wantHost string
		wantPort int
		// wantDataDir is a clone with the dolt CLI into the data directory
		// bd's own server serves: no port is named, so the next command
		// starts that server, which no clone could dial before.
		wantDataDir bool
		// wantRefuse is the advice the clone refuses with, the store's for
		// the endpoint later commands resolve.
		wantRefuse string
		// serverModeEnv sets BEADS_DOLT_SERVER_MODE=1 for bootstrap and
		// the rig's later commands.
		serverModeEnv bool
	}{
		{name: "town shared-server/default config", town: townShared, townKey: "dolt.shared-server", cfg: configfile.DefaultConfig()},
		{name: "town shared-server/embedded config", town: townShared, townKey: "dolt.shared-server", cfg: embedded},
		// Nothing the rig reads names a port, so the next command starts
		// bd's own server over the rig's data directory: the clone goes
		// there, not to the default 3307 nor the town's shared server
		// (3308), neither of which any later command dials.
		{name: "town shared-server/server config naming no port", town: townShared, townKey: "dolt.shared-server", cfg: server(0), wantDataDir: true},
		{name: "town dolt.mode server/default config", town: "dolt.mode: server\n", townKey: "dolt.mode", cfg: configfile.DefaultConfig()},
		{name: "town dolt.host/server config naming no host or port", town: townServer, townKey: "dolt.host", cfg: server(0), wantDataDir: true},
		{name: "town dolt.host/inherited dolt_server_port", town: townServer, townKey: "dolt.host", cfg: server(metadata), wantHost: "127.0.0.1", wantPort: metadata},
		{name: "town dolt.host/user dolt.host", town: townServer, townKey: "dolt.host", userHost: userHost, cfg: server(metadata), wantHost: userHost, wantPort: metadata},
		// An inherited metadata.json naming a remote host with no port and
		// no dolt_mode (GH#3545 host inference). finalize records
		// dolt_mode: server, which turns host inference and its 3307
		// default off, so later commands resolve no port and refuse to
		// dial; the clone refuses with their advice rather than clone
		// into db.rig.example:3307, which no later command dials.
		{name: "town dolt.mode server/inherited remote host naming no port", town: "dolt.mode: server\n", townKey: "dolt.mode",
			cfg: &configfile.Config{Backend: configfile.BackendDolt, DoltServerHost: rigHost}, wantRefuse: "bd does not manage the Dolt server at " + rigHost + "; pin its port"},
		// The same, under BEADS_DOLT_SERVER_MODE=1: the store's
		// external-host default fills 3307 for a host bd does not run once
		// finalize writes it, so later commands dial host:3307, and the
		// clone goes there as on main.
		{name: "town dolt.mode server/inherited remote host naming no port, server mode env", town: "dolt.mode: server\n", townKey: "dolt.mode", serverModeEnv: true,
			cfg: &configfile.Config{Backend: configfile.BackendDolt, DoltServerHost: rigHost}, wantHost: rigHost, wantPort: configfile.DefaultDoltServerPort},
		{name: "town dolt.mode server/inherited ::1 naming no port, server mode env", town: "dolt.mode: server\n", townKey: "dolt.mode", serverModeEnv: true,
			cfg: &configfile.Config{Backend: configfile.BackendDolt, DoltServerHost: "::1"}, wantHost: "::1", wantPort: configfile.DefaultDoltServerPort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clonePortTestEnv(t)
			t.Setenv("BEADS_CEILING_DIRECTORIES", "")
			if tc.serverModeEnv {
				t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
			}
			if tc.userHost != "" {
				writeClonePortFile(t, filepath.Join(os.Getenv("HOME"), ".config", "bd", "config.yaml"), "dolt.host: "+tc.userHost+"\n")
			}
			writeClonePortCredentials(t, []string{"127.0.0.1", townHost, userHost, rigHost, "::1"}, []int{metadata, townPort, configfile.DefaultDoltServerPort})
			town := t.TempDir()
			writeClonePortFile(t, filepath.Join(town, ".beads", "config.yaml"), tc.town)
			rig := filepath.Join(town, "rig")
			if err := os.MkdirAll(rig, 0o700); err != nil {
				t.Fatal(err)
			}
			beadsDir := filepath.Join(rig, ".beads")
			cfg := *tc.cfg
			cfg.DoltDatabase = "rig_sync"
			t.Chdir(rig)
			t.Setenv("BEADS_DIR", "")
			config.ResetForTesting()
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}
			if config.GetYamlConfig(tc.townKey) == "" {
				t.Fatalf("precondition: bootstrap starts without the town's %s loaded", tc.townKey)
			}
			probe := bootstrapServerEndpoint(beadsDir, &cfg)

			var cloned *bootstrapServerProbeConfig
			clonedEmbedded := false
			clonedDataDir := ""
			origServer, origEmbedded, origDataDir, origOpen := remoteCloneViaServer, remoteCloneViaEmbedded, remoteCloneIntoDataDir, openSyncedWarmupStore
			t.Cleanup(func() {
				remoteCloneViaServer, remoteCloneViaEmbedded, remoteCloneIntoDataDir, openSyncedWarmupStore = origServer, origEmbedded, origDataDir, origOpen
				schema.SetLocalMigrateConsent(false)
			})
			remoteCloneIntoDataDir = func(_ context.Context, doltDir, _, _ string) error {
				clonedDataDir = doltDir
				return nil
			}
			remoteCloneViaServer = func(_ context.Context, _, _ string, ep bootstrapServerProbeConfig) error {
				cloned = &ep
				return nil
			}
			remoteCloneViaEmbedded = func(context.Context, string, string, string) error {
				clonedEmbedded = true
				return nil
			}
			warmupHost, warmupPort := "", 0
			openSyncedWarmupStore = func(_ context.Context, dir string) (storage.DoltStorage, error) {
				warmupHost, warmupPort = warmupStoreEndpoint(t, dir)
				return nil, errors.New("stub store")
			}

			plan := BootstrapPlan{Action: "sync", BeadsDir: beadsDir, Database: "rig_sync", SyncRemote: "file:///nonexistent/remote"}
			if tc.wantRefuse != "" {
				err := executeSyncAction(t.Context(), plan, &cfg)
				if err == nil || !strings.Contains(err.Error(), tc.wantRefuse) {
					t.Fatalf("executeSyncAction = %v, want a refusal with %q", err, tc.wantRefuse)
				}
				if cloned != nil || clonedDataDir != "" || clonedEmbedded {
					t.Errorf("cloned (server %v, data dir %q, embedded %v) after refusing", cloned, clonedDataDir, clonedEmbedded)
				}
				if probe.port != 0 {
					t.Errorf("the existence probe dials port %d, but later commands resolve none", probe.port)
				}
				// The metadata.json finalize would write (dolt_mode: server):
				// later commands resolve no port for it either.
				saved := cfg
				saved.DoltMode = configfile.DoltModeServer
				if err := saved.Save(beadsDir); err != nil {
					t.Fatal(err)
				}
				loadClonePortConfig(t, beadsDir)
				if laterEP := doltserver.ResolveEndpoint(beadsDir, &saved); laterEP.Dialable() {
					t.Errorf("later commands dial %s, but the clone refused", laterEP.Location())
				}
				return
			}
			if err := executeSyncAction(t.Context(), plan, &cfg); err != nil {
				t.Fatalf("executeSyncAction: %v", err)
			}

			// The rig's next command, which the CLI runs with BEADS_DIR pinned
			// to it.
			loadClonePortConfig(t, beadsDir)
			later, err := configfile.Load(beadsDir)
			if err != nil || later == nil {
				t.Fatalf("finalize wrote no metadata.json: %v", err)
			}
			wantServer := tc.wantHost != "" || tc.wantDataDir
			if laterServer := effectiveServerMode(beadsDir, later); laterServer != wantServer {
				t.Errorf("later commands use a server: %v (dolt_mode %q), want %v", laterServer, later.DoltMode, wantServer)
			}
			if tc.wantDataDir {
				if cloned != nil {
					t.Errorf("cloned through a server at %s:%d, but no later command dials it", cloned.host, cloned.port)
				}
				if probe.port != 0 {
					t.Errorf("the existence probe dials port %d, but later commands resolve none", probe.port)
				}
				if laterPort := doltserver.ResolveEndpoint(beadsDir, later).Port; laterPort != 0 {
					t.Fatalf("precondition: later commands resolve port %d, want none (bd starts its own server)", laterPort)
				}
				// The data directory bd's own server serves for the rig.
				if want := doltserver.ResolveDoltDir(beadsDir); clonedDataDir != want {
					t.Errorf("cloned into data directory %q, want %q, the one bd's server for the rig serves", clonedDataDir, want)
				}
				return
			}
			if clonedDataDir != "" {
				t.Errorf("cloned into data directory %s, want a clone through a server or the embedded engine", clonedDataDir)
			}
			if !wantServer {
				if cloned != nil {
					t.Errorf("cloned through a server at %s:%d, want a clone into the embedded engine", cloned.host, cloned.port)
				}
				if !clonedEmbedded {
					t.Error("no clone into the embedded engine")
				}
				return
			}
			if cloned == nil {
				t.Fatalf("no server clone (embedded: %v), want one through %s:%d", clonedEmbedded, tc.wantHost, tc.wantPort)
			}
			laterEP := doltserver.ResolveEndpoint(beadsDir, later)
			laterHost, laterPort := laterEP.Host, laterEP.Port
			if cloned.host != tc.wantHost || cloned.port != tc.wantPort {
				t.Errorf("clone dialed %s:%d, want %s:%d", cloned.host, cloned.port, tc.wantHost, tc.wantPort)
			}
			if cloned.host != laterHost || cloned.port != laterPort {
				t.Errorf("clone dialed %s:%d, but later commands dial %s:%d", cloned.host, cloned.port, laterHost, laterPort)
			}
			if want := laterEP.Password(); want == "" || cloned.pass != want {
				t.Errorf("clone password = %q, want %q, the one later commands present there", cloned.pass, want)
			}
			if *cloned != probe {
				t.Errorf("the existence probe dials %+v, but the clone dialed %+v", probe, *cloned)
			}
			if warmupHost != laterHost || warmupPort != laterPort {
				t.Errorf("warmup dials %s:%d, but later commands dial %s:%d", warmupHost, warmupPort, laterHost, laterPort)
			}
		})
	}
}

// raiseRequiredMinForTest raises doltversion.RequiredMin above any Dolt the
// tests run against, so a real server or binary stands in for one below the
// floor, and restores it when t ends. Its caller must not run in parallel.
func raiseRequiredMinForTest(t *testing.T) {
	t.Helper()
	prev := doltversion.RequiredMin
	doltversion.RequiredMin = doltversion.MustParse("99.0.0")
	t.Cleanup(func() { doltversion.RequiredMin = prev })
}

// TestSyncActionFailsWhenTheWarmupRefusesDolt: a Dolt below bd's minimum
// refuses bootstrap's post-clone warmup, and every later command the same
// way, so bd bootstrap fails rather than report a usable workspace behind
// the non-fatal missing-wisp-table warning other warmup failures get.
func TestSyncActionFailsWhenTheWarmupRefusesDolt(t *testing.T) {
	clonePortTestEnv(t)
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_DIR", "")
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeEmbedded, DoltDatabase: "rig_floor"}
	origEmbedded, origOpen := remoteCloneViaEmbedded, openSyncedWarmupStore
	t.Cleanup(func() {
		remoteCloneViaEmbedded, openSyncedWarmupStore = origEmbedded, origOpen
		schema.SetLocalMigrateConsent(false)
	})
	remoteCloneViaEmbedded = func(context.Context, string, string, string) error { return nil }
	plan := BootstrapPlan{Action: "sync", BeadsDir: beadsDir, Database: "rig_floor", SyncRemote: "file:///nonexistent/remote"}

	refusal := doltversion.CheckRequired(doltversion.MustParse("2.5.1"), "dolt sql-server at 127.0.0.1:3307", true)
	openSyncedWarmupStore = func(context.Context, string) (storage.DoltStorage, error) {
		return nil, fmt.Errorf("Dolt server unreachable at 127.0.0.1:3307 and auto-start failed: %w", refusal)
	}
	err := executeSyncAction(t.Context(), plan, cfg)
	if !errors.Is(err, doltversion.ErrBelowRequiredMin) {
		t.Fatalf("executeSyncAction = %v, want the warmup's ErrBelowRequiredMin", err)
	}
	if !strings.Contains(err.Error(), "file:///nonexistent/remote") || !strings.Contains(err.Error(), "2.5.1") {
		t.Errorf("executeSyncAction = %q, want it to name the remote cloned from and the refused version", err)
	}

	// Any other warmup failure stays the warning it was.
	openSyncedWarmupStore = func(context.Context, string) (storage.DoltStorage, error) {
		return nil, errors.New("stub store")
	}
	if err := executeSyncAction(t.Context(), plan, cfg); err != nil {
		t.Errorf("executeSyncAction with a warmup failure that is no refusal = %v, want nil", err)
	}
}

// TestCloneViaServerRefusesBelowRequiredMin pins the floor cloneViaServer
// checks against the server before CALL DOLT_CLONE, which bd init --remote
// and bd bootstrap both clone through: with the floor raised above the test
// server's version, the clone is refused with the floor's error, where
// DOLT_CLONE would have failed on the missing remote with its own.
func TestCloneViaServerRefusesBelowRequiredMin(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}
	raiseRequiredMinForTest(t)
	ep := bootstrapServerProbeConfig{host: "127.0.0.1", port: testDoltServerPort, user: "root"}

	err := cloneViaServer(t.Context(), "file:///nonexistent/remote", uniqueTestDBName(t), ep)
	if !errors.Is(err, doltversion.ErrBelowRequiredMin) {
		t.Fatalf("cloneViaServer = %v, want ErrBelowRequiredMin before the clone", err)
	}
	if !strings.Contains(err.Error(), "dolt sql-server at "+ep.serverLocation()) {
		t.Errorf("cloneViaServer = %q, want it to name the server", err)
	}
}

// bootstrapServerWorkspace is a workspace whose own metadata.json selects a
// Dolt server on port (dolt_mode server, dolt_server_port), with database db
// and no project_id, bound as the CLI binds it. It returns the .beads
// directory and the configuration loaded from it.
func bootstrapServerWorkspace(t *testing.T, port int, db string) (string, *configfile.Config) {
	t.Helper()
	snapshotBootstrapEnv(t)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"database": "dolt", "backend": "dolt", "dolt_database": db}
	if port > 0 {
		meta["dolt_mode"] = "server"
		meta["dolt_server_port"] = port
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	t.Chdir(root)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("configfile.Load: %v", err)
	}
	return beadsDir, cfg
}

// TestBootstrapStoreConfigOpensTheWorkspaceStore pins the store bd
// bootstrap's init, restore and jsonl-import actions open. In a workspace
// whose own metadata selects no server it is exactly the embedded config
// those actions always built. In one whose metadata selects a Dolt server it
// is that server, at the port later commands resolve, where main built the
// embedded config there too and imported into .beads/embeddeddolt, which no
// later command in the workspace opens.
func TestBootstrapStoreConfigOpensTheWorkspaceStore(t *testing.T) {
	t.Run("embedded workspace", func(t *testing.T) {
		beadsDir, cfg := bootstrapServerWorkspace(t, 0, "emb_db")
		got, err := bootstrapStoreConfig(t.Context(), beadsDir, cfg)
		if err != nil {
			t.Fatalf("bootstrapStoreConfig: %v", err)
		}
		want := dolt.Config{
			Path:            doltserver.ResolveDoltDir(beadsDir),
			Database:        cfg.GetDoltDatabase(),
			CreateIfMissing: true,
			AutoStart:       true,
			BeadsDir:        beadsDir,
		}
		if !reflect.DeepEqual(*got, want) {
			t.Errorf("bootstrapStoreConfig = %+v, want main's embedded config %+v", *got, want)
		}
	})
	t.Run("server-mode workspace", func(t *testing.T) {
		beadsDir, cfg := bootstrapServerWorkspace(t, 13463, "srv_db")
		got, err := bootstrapStoreConfig(t.Context(), beadsDir, cfg)
		if err != nil {
			t.Fatalf("bootstrapStoreConfig: %v", err)
		}
		if !got.ServerMode || got.ServerPort != 13463 || got.ServerHost != "127.0.0.1" || got.Database != "srv_db" || !got.CreateIfMissing {
			t.Errorf("bootstrapStoreConfig = %+v, want the workspace's server at 127.0.0.1:13463, creating srv_db", *got)
		}
	})
	// A workspace with no metadata.json of its own, in shared-server mode,
	// keeps main's embedded config: no later command there opens a server
	// database under the name bootstrap would use.
	t.Run("metadata-less workspace in shared-server mode", func(t *testing.T) {
		beadsDir, cfg := bootstrapServerWorkspace(t, 0, "emb_db")
		if err := os.Remove(filepath.Join(beadsDir, "metadata.json")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
		got, err := bootstrapStoreConfig(t.Context(), beadsDir, cfg)
		if err != nil {
			t.Fatalf("bootstrapStoreConfig: %v", err)
		}
		if got.ServerMode {
			t.Errorf("bootstrapStoreConfig = %+v, want main's embedded config", *got)
		}
	})
	// Shared-server mode named only in a config.yaml, while the workspace's
	// own metadata.json does not select server mode, keeps main's embedded
	// config: workspace discovery does not count that mode, so every later
	// command finds the workspace through the .beads/embeddeddolt the action
	// creates (and then opens the shared server's database).
	t.Run("user-level dolt.shared-server, metadata without dolt_mode", func(t *testing.T) {
		beadsDir, cfg := bootstrapServerWorkspace(t, 0, "shr_db")
		userDir := filepath.Join(os.Getenv("HOME"), ".config", "bd")
		if err := os.MkdirAll(userDir, 0o750); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("HOME"), ".config"))
		if err := os.WriteFile(filepath.Join(userDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		config.ResetForTesting()
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		if !effectiveServerMode(beadsDir, cfg) {
			t.Fatal("precondition: the store factories do not take this workspace for shared-server mode")
		}
		got, err := bootstrapStoreConfig(t.Context(), beadsDir, cfg)
		if err != nil {
			t.Fatalf("bootstrapStoreConfig: %v", err)
		}
		if got.ServerMode {
			t.Errorf("bootstrapStoreConfig = %+v, want main's embedded config", *got)
		}
	})
	t.Run("dolt_mode embedded, workspace config.yaml dolt.shared-server", func(t *testing.T) {
		beadsDir, _ := bootstrapServerWorkspace(t, 0, "shr_db")
		meta := []byte(`{"database":"dolt","backend":"dolt","dolt_database":"shr_db","dolt_mode":"embedded"}`)
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), meta, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := configfile.Load(beadsDir)
		if err != nil || cfg == nil {
			t.Fatalf("configfile.Load: %v", err)
		}
		if !effectiveServerMode(beadsDir, cfg) {
			t.Fatal("precondition: the store factories do not take this workspace for shared-server mode")
		}
		got, err := bootstrapStoreConfig(t.Context(), beadsDir, cfg)
		if err != nil {
			t.Fatalf("bootstrapStoreConfig: %v", err)
		}
		if got.ServerMode {
			t.Errorf("bootstrapStoreConfig = %+v, want main's embedded config", *got)
		}
	})
	// A proxied-server workspace keeps main's embedded config even when the
	// environment forces server mode: the store factories refuse a plain
	// server store there before they consult server mode.
	t.Run("proxied-server workspace under BEADS_DOLT_SERVER_MODE", func(t *testing.T) {
		beadsDir, cfg := bootstrapServerWorkspace(t, 0, "prx_db")
		meta := []byte(`{"database":"dolt","backend":"dolt","dolt_database":"prx_db","dolt_mode":"proxied-server"}`)
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), meta, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
		got, err := bootstrapStoreConfig(t.Context(), beadsDir, cfg)
		if err != nil {
			t.Fatalf("bootstrapStoreConfig: %v", err)
		}
		if got.ServerMode {
			t.Errorf("bootstrapStoreConfig = %+v, want main's embedded config", *got)
		}
	})
}

// TestExecuteJSONLImportAction_ServerAlreadyHoldsDatabaseWritesNothing: the
// plan can reach jsonl-import with nothing on this machine tying the
// server's database to the workspace (a fresh clone matched by name, or no
// project_id). Importing the tracked issues.jsonl there would write its
// stale rows over live ones, so the action asks the server first, as the
// store would dial it, and writes nothing when the database is there. Main
// imported into .beads/embeddeddolt instead, which no later command opens.
func TestExecuteJSONLImportAction_ServerAlreadyHoldsDatabaseWritesNothing(t *testing.T) {
	beadsDir, cfg := bootstrapServerWorkspace(t, 13464, "held_db")
	jsonl := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonl, []byte(`{"id":"hd-1","title":"stale","status":"open","priority":2,"issue_type":"task"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := checkBootstrapServerDB
	t.Cleanup(func() { checkBootstrapServerDB = orig })
	var probed []string
	checkBootstrapServerDB = func(ep bootstrapServerProbeConfig) bootstrapServerDBCheck {
		probed = append(probed, ep.serverLocation()+"/"+ep.database)
		return bootstrapServerDBCheck{Reachable: true, Exists: true}
	}
	plan := BootstrapPlan{BeadsDir: beadsDir, Database: "held_db", Action: "jsonl-import", JSONLFile: jsonl}
	if err := executeJSONLImportAction(t.Context(), plan, cfg); err != nil {
		t.Fatalf("executeJSONLImportAction: %v", err)
	}
	if len(probed) != 1 || probed[0] != "127.0.0.1:13464/held_db" {
		t.Errorf("server probes %v, want one for held_db at 127.0.0.1:13464, the server the store opens", probed)
	}
	for _, dir := range []string{"embeddeddolt", "dolt"} {
		if _, err := os.Stat(filepath.Join(beadsDir, dir)); err == nil {
			t.Errorf("bootstrap created .beads/%s in a server-mode workspace whose server already holds the database", dir)
		}
	}
}

// TestExecuteRestoreAction_ServerModeRefusesBeforeCreating: the restore
// action creates the database and then restores the backup over it without
// --force, which Dolt refuses. On main it did so in .beads/embeddeddolt even
// in a server-mode workspace. On a server it would leave an empty database
// every later command takes for the real one, so it refuses before anything
// is created, with the restore command to run, and offers the tracked
// issues.jsonl only where there is one. When the server already holds the
// database it says so and names no --force restore, which would replace the
// database's current contents.
func TestExecuteRestoreAction_ServerModeRefusesBeforeCreating(t *testing.T) {
	for _, tc := range []struct {
		withJSONL, held bool
	}{{false, false}, {true, false}, {false, true}} {
		withJSONL := tc.withJSONL
		t.Run(fmt.Sprintf("tracked issues.jsonl %v, server holds the database %v", withJSONL, tc.held), func(t *testing.T) {
			beadsDir, cfg := bootstrapServerWorkspace(t, 13465, "rst_db")
			backupDir := filepath.Join(beadsDir, "backup")
			if err := os.MkdirAll(backupDir, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(backupDir, "issues.jsonl"), []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if withJSONL {
				if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			orig := checkBootstrapServerDB
			t.Cleanup(func() { checkBootstrapServerDB = orig })
			var probed []string
			checkBootstrapServerDB = func(ep bootstrapServerProbeConfig) bootstrapServerDBCheck {
				probed = append(probed, ep.serverLocation()+"/"+ep.database)
				return bootstrapServerDBCheck{Reachable: true, Exists: tc.held}
			}
			plan := BootstrapPlan{BeadsDir: beadsDir, Database: "rst_db", Action: "restore", BackupDir: backupDir}
			err := executeRestoreAction(t.Context(), plan, cfg)
			if err == nil {
				t.Fatal("executeRestoreAction succeeded in a server-mode workspace, want a refusal")
			}
			if len(probed) != 1 || probed[0] != "127.0.0.1:13465/rst_db" {
				t.Errorf("server probes %v, want one for rst_db at 127.0.0.1:13465, the server the store opens", probed)
			}
			msg := err.Error()
			if tc.held {
				for _, want := range []string{"rst_db", "already exists", "127.0.0.1:13465", "did not restore", "replace the database's current contents"} {
					if !strings.Contains(msg, want) {
						t.Errorf("refusal %q does not name %q", msg, want)
					}
				}
				for _, unwanted := range []string{"once database", "bd backup restore --force " + backupDir} {
					if strings.Contains(msg, unwanted) {
						t.Errorf("refusal %q for a database the server holds says %q", msg, unwanted)
					}
				}
			} else {
				for _, want := range []string{"nothing was created", "127.0.0.1:13465", "bd backup restore --force " + backupDir, "rst_db", "init-missing-server-db"} {
					if !strings.Contains(msg, want) {
						t.Errorf("refusal %q does not name %q", msg, want)
					}
				}
				if got := strings.Contains(msg, "re-run 'bd bootstrap'"); got != withJSONL {
					t.Errorf("refusal offers the tracked issues.jsonl = %v, want %v: %q", got, withJSONL, msg)
				}
			}
			for _, dir := range []string{"embeddeddolt", "dolt"} {
				if _, err := os.Stat(filepath.Join(beadsDir, dir)); err == nil {
					t.Errorf("the refused restore created .beads/%s", dir)
				}
			}
		})
	}
}
