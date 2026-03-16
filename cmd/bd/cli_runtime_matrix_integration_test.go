//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

type runtimeMatrixDoctorResult struct {
	OverallOK bool                       `json:"overall_ok"`
	Checks    []runtimeMatrixDoctorCheck `json:"checks"`
}

type runtimeMatrixDoctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
}

type runtimeMatrixRedirectedRepo struct {
	RepoDir        string
	SourceBeadsDir string
	TargetBeadsDir string
	SourceDB       string
	TargetDB       string
	Port           int
	Cleanup        func()
}

var (
	runtimeMatrixBinaryPath string
	runtimeMatrixBinaryOnce sync.Once
	runtimeMatrixBinaryErr  error
)

func TestE2E_RuntimeMatrix_RepoLocalCommandsPreserveTrackedServerState(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("repo-local runtime matrix test not supported on windows")
	}

	bdBinary := buildRuntimeMatrixTestBinary(t)
	repoDir := setupRuntimeMatrixGitRepo(t)
	env := runtimeMatrixEnv()

	initOut, initErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		lower := strings.ToLower(initOut)
		if strings.Contains(lower, "dolt") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "not available") || strings.Contains(lower, "unknown")) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	issueOne := runtimeMatrixCreateIssue(t, bdBinary, repoDir, env, "runtime matrix one")
	issueTwo := runtimeMatrixCreateIssue(t, bdBinary, repoDir, env, "runtime matrix two")

	cases := []struct {
		name     string
		args     []string
		validate func(t *testing.T, output string)
	}{
		{
			name: "show",
			args: []string{"show", issueOne, "--json"},
			validate: func(t *testing.T, output string) {
				t.Helper()
				items := decodeIssueObjects(t, output)
				if len(items) != 1 {
					t.Fatalf("show returned %d items, want 1\n%s", len(items), output)
				}
				if got := stringField(items[0], "id"); got != issueOne {
					t.Fatalf("show id = %q, want %q", got, issueOne)
				}
			},
		},
		{
			name: "list",
			args: []string{"list", "--json"},
			validate: func(t *testing.T, output string) {
				t.Helper()
				items := decodeIssueObjects(t, output)
				assertContainsIssueIDs(t, items, issueOne, issueTwo)
			},
		},
		{
			name: "ready",
			args: []string{"ready", "--json"},
			validate: func(t *testing.T, output string) {
				t.Helper()
				items := decodeIssueObjects(t, output)
				assertContainsIssueIDs(t, items, issueOne, issueTwo)
			},
		},
		{
			name: "update",
			args: []string{"update", issueOne, "--status", "in_progress", "--json"},
			validate: func(t *testing.T, output string) {
				t.Helper()
				items := decodeIssueObjects(t, output)
				if len(items) == 0 {
					t.Fatalf("update returned no issues\n%s", output)
				}
				if got := stringField(items[0], "id"); got != issueOne {
					t.Fatalf("update id = %q, want %q", got, issueOne)
				}
				if got := stringField(items[0], "status"); got != string(types.StatusInProgress) {
					t.Fatalf("update status = %q, want %q", got, types.StatusInProgress)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtimeMatrixEnsureStoppedBaseline(t, bdBinary, repoDir, env)

			output, err := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, tc.args...)
			if err != nil {
				t.Fatalf("bd %s failed: %v\n%s", tc.name, err, output)
			}
			tc.validate(t, output)

			statusAfter, statusErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, "dolt", "status")
			if statusErr != nil {
				t.Fatalf("bd dolt status after %s failed: %v\n%s", tc.name, statusErr, statusAfter)
			}
			assertTrackedServerRunning(t, tc.name, statusAfter)
		})
	}
}

func TestE2E_RuntimeMatrix_MultiRepoRepoLocalServersStayIsolated(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("multi-repo runtime matrix test not supported on windows")
	}

	bdBinary := buildRuntimeMatrixTestBinary(t)
	env := runtimeMatrixEnv()

	repoA := setupRuntimeMatrixGitRepo(t)
	repoB := setupRuntimeMatrixGitRepo(t)
	runtimeMatrixInitDoltRepo(t, bdBinary, repoA, env, "alpha")
	runtimeMatrixInitDoltRepo(t, bdBinary, repoB, env, "beta")

	issueA := runtimeMatrixCreateIssue(t, bdBinary, repoA, env, "alpha issue")
	issueB := runtimeMatrixCreateIssue(t, bdBinary, repoB, env, "beta issue")

	runtimeMatrixEnsureStoppedBaseline(t, bdBinary, repoA, env)
	runtimeMatrixEnsureStoppedBaseline(t, bdBinary, repoB, env)

	showA, showAErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoA, env, "show", issueA, "--json")
	if showAErr != nil {
		t.Fatalf("bd show in repo A failed: %v\n%s", showAErr, showA)
	}
	itemsA := decodeIssueObjects(t, showA)
	if len(itemsA) != 1 || stringField(itemsA[0], "id") != issueA {
		t.Fatalf("repo A show returned unexpected payload:\n%s", showA)
	}

	statusA, statusAErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoA, env, "dolt", "status")
	if statusAErr != nil {
		t.Fatalf("bd dolt status in repo A failed: %v\n%s", statusAErr, statusA)
	}
	assertTrackedServerRunning(t, "repo A baseline", statusA)
	portA := extractStatusPort(t, statusA)

	showB, showBErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoB, env, "show", issueB, "--json")
	if showBErr != nil {
		t.Fatalf("bd show in repo B failed: %v\n%s", showBErr, showB)
	}
	itemsB := decodeIssueObjects(t, showB)
	if len(itemsB) != 1 || stringField(itemsB[0], "id") != issueB {
		t.Fatalf("repo B show returned unexpected payload:\n%s", showB)
	}

	statusB, statusBErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoB, env, "dolt", "status")
	if statusBErr != nil {
		t.Fatalf("bd dolt status in repo B failed: %v\n%s", statusBErr, statusB)
	}
	assertTrackedServerRunning(t, "repo B baseline", statusB)
	portB := extractStatusPort(t, statusB)

	if portA == portB {
		t.Fatalf("expected distinct repo-local ports, got shared port %d", portA)
	}

	statusAAfter, statusAAfterErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoA, env, "dolt", "status")
	if statusAAfterErr != nil {
		t.Fatalf("bd dolt status in repo A after repo B start failed: %v\n%s", statusAAfterErr, statusAAfter)
	}
	assertTrackedServerRunning(t, "repo A after repo B start", statusAAfter)
	assertStatusContainsPort(t, statusAAfter, portA)

	statusBAfter, statusBAfterErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoB, env, "dolt", "status")
	if statusBAfterErr != nil {
		t.Fatalf("bd dolt status in repo B after isolation check failed: %v\n%s", statusBAfterErr, statusBAfter)
	}
	assertTrackedServerRunning(t, "repo B after repo A validation", statusBAfter)
	assertStatusContainsPort(t, statusBAfter, portB)

	listA, listAErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoA, env, "list", "--json")
	if listAErr != nil {
		t.Fatalf("bd list in repo A failed: %v\n%s", listAErr, listA)
	}
	itemsA = decodeIssueObjects(t, listA)
	if len(itemsA) != 1 {
		t.Fatalf("repo A list returned %d issues, want 1\n%s", len(itemsA), listA)
	}
	assertContainsIssueIDs(t, itemsA, issueA)

	listB, listBErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoB, env, "list", "--json")
	if listBErr != nil {
		t.Fatalf("bd list in repo B failed: %v\n%s", listBErr, listB)
	}
	itemsB = decodeIssueObjects(t, listB)
	if len(itemsB) != 1 {
		t.Fatalf("repo B list returned %d issues, want 1\n%s", len(itemsB), listB)
	}
	assertContainsIssueIDs(t, itemsB, issueB)
}

func TestE2E_RuntimeMatrix_DoctorDryRunPreservesRedirectSourceDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("redirect runtime matrix test not supported on windows")
	}

	bdBinary := buildRuntimeMatrixTestBinary(t)
	env := runtimeMatrixEnv()

	repo := setupRedirectedRuntimeMatrixRepo(t, true, 1, 2)
	defer repo.Cleanup()

	statusBefore, statusErr := runBDExecAllowErrorWithEnv(t, bdBinary, repo.RepoDir, env, "dolt", "status")
	if statusErr != nil {
		t.Fatalf("bd dolt status before doctor --dry-run failed: %v\n%s", statusErr, statusBefore)
	}
	assertTrackedServerRunning(t, "doctor --dry-run baseline", statusBefore)

	doctorOut, _ := runBDExecAllowErrorWithEnv(t, bdBinary, repo.RepoDir, env, "doctor", "--dry-run", "--json")

	result := decodeDoctorResult(t, doctorOut)
	assertDoctorCheckStatus(t, result, "Dolt Connection", "ok")
	assertDoctorCheckStatus(t, result, "Dolt Schema", "ok")
	assertDoctorCheckMessageContains(t, result, "Dolt Issue Count", "1 issues")

	statusAfter, statusErr := runBDExecAllowErrorWithEnv(t, bdBinary, repo.RepoDir, env, "dolt", "status")
	if statusErr != nil {
		t.Fatalf("bd dolt status after doctor --dry-run failed: %v\n%s", statusErr, statusAfter)
	}
	assertTrackedServerRunning(t, "doctor --dry-run", statusAfter)
	assertStatusContainsPort(t, statusAfter, repo.Port)
}

func TestE2E_RuntimeMatrix_DoctorFixYesCreatesSelectedDatabaseWhenSharedDirExists(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("doctor --fix shared-dir matrix test not supported on windows")
	}

	bdBinary := buildRuntimeMatrixTestBinary(t)
	env := runtimeMatrixEnv()

	repo := setupRedirectedRuntimeMatrixRepo(t, false, 0, 2)
	defer repo.Cleanup()

	writeRuntimeMatrixJSONL(t, repo.SourceBeadsDir, []types.Issue{
		{
			ID:        "srcfix-1",
			Title:     "imported from jsonl",
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			Priority:  2,
		},
	})

	statusBefore, statusErr := runBDExecAllowErrorWithEnv(t, bdBinary, repo.RepoDir, env, "dolt", "status")
	if statusErr != nil {
		t.Fatalf("bd dolt status before doctor --fix --yes failed: %v\n%s", statusErr, statusBefore)
	}
	assertTrackedServerRunning(t, "doctor --fix --yes baseline", statusBefore)

	_, _ = runBDExecAllowErrorWithEnv(t, bdBinary, repo.RepoDir, env, "doctor", "--fix", "--yes")

	cfg, err := configfile.Load(repo.SourceBeadsDir)
	if err != nil {
		t.Fatalf("load source metadata after fix: %v", err)
	}
	if got := cfg.GetDoltDatabase(); got != repo.SourceDB {
		t.Fatalf("source metadata dolt_database = %q, want %q", got, repo.SourceDB)
	}

	listOut, listErr := runBDExecAllowErrorWithEnv(t, bdBinary, repo.RepoDir, env, "list", "--json")
	if listErr != nil {
		t.Fatalf("bd list after doctor --fix --yes failed: %v\n%s", listErr, listOut)
	}
	items := decodeIssueObjects(t, listOut)
	if len(items) != 1 {
		t.Fatalf("list returned %d issues after doctor --fix --yes, want 1\n%s", len(items), listOut)
	}
	if got := stringField(items[0], "title"); got != "imported from jsonl" {
		t.Fatalf("imported issue title = %q, want %q", got, "imported from jsonl")
	}

	statusAfter, statusErr := runBDExecAllowErrorWithEnv(t, bdBinary, repo.RepoDir, env, "dolt", "status")
	if statusErr != nil {
		t.Fatalf("bd dolt status after doctor --fix --yes failed: %v\n%s", statusErr, statusAfter)
	}
	assertTrackedServerRunning(t, "doctor --fix --yes", statusAfter)
	assertStatusContainsPort(t, statusAfter, repo.Port)

	doctorOut, _ := runBDExecAllowErrorWithEnv(t, bdBinary, repo.RepoDir, env, "doctor", "--dry-run", "--json")
	assertDoctorCheckStatus(t, decodeDoctorResult(t, doctorOut), "Dolt Connection", "ok")
}

func TestE2E_RuntimeMatrix_DoctorFixYesRecoversMalformedMetadata(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("malformed metadata runtime matrix test not supported on windows")
	}

	bdBinary := buildRuntimeMatrixTestBinary(t)
	repoDir := setupRuntimeMatrixGitRepo(t)
	env := runtimeMatrixEnv()

	initOut, initErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		lower := strings.ToLower(initOut)
		if strings.Contains(lower, "dolt") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "not available") || strings.Contains(lower, "unknown")) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	runtimeMatrixCreateIssue(t, bdBinary, repoDir, env, "malformed metadata survivor")

	cfg, err := configfile.Load(filepath.Join(repoDir, ".beads"))
	if err != nil {
		t.Fatalf("load metadata before corruption: %v", err)
	}
	expectedDB := cfg.GetDoltDatabase()
	if expectedDB == "" {
		t.Fatal("expected non-empty dolt_database before corruption")
	}

	if err := os.WriteFile(filepath.Join(repoDir, ".beads", configfile.ConfigFileName), []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write malformed metadata: %v", err)
	}

	fixOut, _ := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, "doctor", "--fix", "--yes")
	if strings.Contains(fixOut, "failed to reinitialize Dolt database") {
		t.Fatalf("doctor --fix attempted stale destructive repair after metadata recovery\n%s", fixOut)
	}

	cfg, err = configfile.Load(filepath.Join(repoDir, ".beads"))
	if err != nil {
		t.Fatalf("load metadata after recovery: %v", err)
	}
	if got := cfg.GetDoltDatabase(); got != expectedDB {
		t.Fatalf("recovered dolt_database = %q, want %q", got, expectedDB)
	}

	listOut, listErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, "list", "--json")
	if listErr != nil {
		t.Fatalf("bd list after malformed-metadata recovery failed: %v\n%s", listErr, listOut)
	}
	items := decodeIssueObjects(t, listOut)
	if len(items) != 1 {
		t.Fatalf("list returned %d issues after malformed-metadata recovery, want 1\n%s", len(items), listOut)
	}
	if got := stringField(items[0], "title"); got != "malformed metadata survivor" {
		t.Fatalf("recovered issue title = %q, want %q", got, "malformed metadata survivor")
	}

	doctorOut, _ := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, "doctor", "--dry-run", "--json")
	result := decodeDoctorResult(t, doctorOut)
	assertDoctorCheckStatus(t, result, "Config Values", "ok")
	assertDoctorCheckStatus(t, result, "Dolt Connection", "ok")
}

func TestE2E_RuntimeMatrix_DoctorFixYesRemovesStaleAccessLock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("stale lock runtime matrix test not supported on windows")
	}

	bdBinary := buildRuntimeMatrixTestBinary(t)
	repoDir := setupRuntimeMatrixGitRepo(t)
	env := runtimeMatrixEnv()

	runtimeMatrixInitDoltRepo(t, bdBinary, repoDir, env, "lock")
	issueID := runtimeMatrixCreateIssue(t, bdBinary, repoDir, env, "stale lock survivor")
	runtimeMatrixEnsureStoppedBaseline(t, bdBinary, repoDir, env)

	lockPath := filepath.Join(repoDir, ".beads", "dolt-access.lock")
	if err := os.WriteFile(lockPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale dolt-access.lock: %v", err)
	}
	oldTime := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
		t.Fatalf("age stale dolt-access.lock: %v", err)
	}

	fixOut, fixErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, "doctor", "--fix", "--yes")
	if fixErr != nil {
		t.Fatalf("bd doctor --fix --yes failed: %v\n%s", fixErr, fixOut)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected stale dolt-access.lock to be removed, got err=%v", err)
	}

	listOut, listErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, "list", "--json")
	if listErr != nil {
		t.Fatalf("bd list after stale-lock recovery failed: %v\n%s", listErr, listOut)
	}
	items := decodeIssueObjects(t, listOut)
	if len(items) != 1 {
		t.Fatalf("list returned %d issues after stale-lock recovery, want 1\n%s", len(items), listOut)
	}
	assertContainsIssueIDs(t, items, issueID)

	doctorOut, _ := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, "doctor", "--dry-run", "--json")
	assertDoctorCheckStatus(t, decodeDoctorResult(t, doctorOut), "Dolt Connection", "ok")
}

func runtimeMatrixEnv() []string {
	return append(os.Environ(),
		"BEADS_TEST_MODE=",
		"GT_ROOT=",
		"BEADS_DOLT_AUTO_START=",
		"BEADS_DOLT_SERVER_PORT=",
		"BEADS_DOLT_PORT=",
		"BEADS_DOLT_SHARED_SERVER=",
	)
}

func buildRuntimeMatrixTestBinary(t *testing.T) string {
	t.Helper()

	runtimeMatrixBinaryOnce.Do(func() {
		pkgDir, err := os.Getwd()
		if err != nil {
			runtimeMatrixBinaryErr = fmt.Errorf("getwd: %w", err)
			return
		}
		tmpDir, err := os.MkdirTemp("", "bd-runtime-matrix-*")
		if err != nil {
			runtimeMatrixBinaryErr = fmt.Errorf("mkdir temp: %w", err)
			return
		}
		runtimeMatrixBinaryPath = filepath.Join(tmpDir, "bd-runtime-matrix")
		cmd := exec.Command("go", "build", "-o", runtimeMatrixBinaryPath, ".")
		cmd.Dir = pkgDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			runtimeMatrixBinaryErr = fmt.Errorf("go build failed: %w\n%s", err, out)
			runtimeMatrixBinaryPath = ""
		}
	})

	if runtimeMatrixBinaryErr != nil {
		t.Fatal(runtimeMatrixBinaryErr)
	}
	return runtimeMatrixBinaryPath
}

func setupRuntimeMatrixGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := runCommandInDir(dir, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	_ = runCommandInDir(dir, "git", "config", "user.email", "test@example.com")
	_ = runCommandInDir(dir, "git", "config", "user.name", "Test User")
	_ = runCommandInDir(dir, "git", "config", "remote.origin.url", "https://github.com/test/repo.git")
	return dir
}

func runtimeMatrixInitDoltRepo(t *testing.T, bdBinary, repoDir string, env []string, prefix string) {
	t.Helper()

	initOut, initErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, "init", "--backend", "dolt", "--prefix", prefix, "--quiet")
	if initErr != nil {
		lower := strings.ToLower(initOut)
		if strings.Contains(lower, "dolt") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "not available") || strings.Contains(lower, "unknown")) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}
}

func runtimeMatrixCreateIssue(t *testing.T, bdBinary, repoDir string, env []string, title string) string {
	t.Helper()
	out, err := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, "create", title, "--json")
	if err != nil {
		t.Fatalf("bd create %q failed: %v\n%s", title, err, out)
	}
	items := decodeIssueObjects(t, out)
	if len(items) == 0 {
		t.Fatalf("create returned no issues\n%s", out)
	}
	id := stringField(items[0], "id")
	if id == "" {
		t.Fatalf("create output missing id\n%s", out)
	}
	return id
}

func runtimeMatrixEnsureStoppedBaseline(t *testing.T, bdBinary, repoDir string, env []string) {
	t.Helper()

	statusOut, _ := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, "dolt", "status")
	if strings.Contains(statusOut, "Dolt server: running") {
		stopOut, stopErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, "dolt", "stop")
		if stopErr != nil {
			t.Fatalf("bd dolt stop failed: %v\n%s", stopErr, stopOut)
		}
	}

	statusOut, statusErr := runBDExecAllowErrorWithEnv(t, bdBinary, repoDir, env, "dolt", "status")
	if statusErr != nil {
		t.Fatalf("bd dolt status baseline failed: %v\n%s", statusErr, statusOut)
	}
	if !strings.Contains(statusOut, "Dolt server: not running") {
		t.Fatalf("expected stopped baseline, got:\n%s", statusOut)
	}
}

func setupRedirectedRuntimeMatrixRepo(t *testing.T, createSource bool, sourceIssues int, targetIssues int) *runtimeMatrixRedirectedRepo {
	t.Helper()

	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}

	repoDir := setupRuntimeMatrixGitRepo(t)
	sourceBeadsDir := filepath.Join(repoDir, ".beads")
	targetRoot := t.TempDir()
	targetBeadsDir := filepath.Join(targetRoot, ".beads")
	targetDataDir := filepath.Join(targetBeadsDir, "dolt")
	if err := os.MkdirAll(sourceBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir source beads dir: %v", err)
	}
	if err := os.MkdirAll(targetDataDir, 0o755); err != nil {
		t.Fatalf("mkdir target data dir: %v", err)
	}

	sourceDB := "source_db"
	targetDB := "target_db"

	sourceCfg := &configfile.Config{
		Backend:      configfile.BackendDolt,
		Database:     "dolt",
		DoltDatabase: sourceDB,
	}
	if err := sourceCfg.Save(sourceBeadsDir); err != nil {
		t.Fatalf("save source metadata: %v", err)
	}

	targetCfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		Database:       "dolt",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: port,
		DoltServerUser: "root",
		DoltDatabase:   targetDB,
	}
	if err := targetCfg.Save(targetBeadsDir); err != nil {
		t.Fatalf("save target metadata: %v", err)
	}

	if err := os.WriteFile(filepath.Join(sourceBeadsDir, "redirect"), []byte(targetBeadsDir+"\n"), 0o644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	serverCmd := exec.Command("dolt", "sql-server", "--data-dir", targetDataDir, "-H", "127.0.0.1", "-P", strconv.Itoa(port))
	serverCmd.Dir = targetRoot
	logFile, err := os.Create(filepath.Join(targetBeadsDir, "dolt-server.log"))
	if err != nil {
		t.Fatalf("create server log: %v", err)
	}
	serverCmd.Stdout = logFile
	serverCmd.Stderr = logFile
	if err := serverCmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start dolt sql-server: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetBeadsDir, "dolt-server.pid"), []byte(fmt.Sprintf("%d\n", serverCmd.Process.Pid)), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetBeadsDir, "dolt-server.port"), []byte(fmt.Sprintf("%d\n", port)), 0o644); err != nil {
		t.Fatalf("write port file: %v", err)
	}
	if !testutil.WaitForServer(port, 15*time.Second) {
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
		_ = logFile.Close()
		t.Fatal("dolt sql-server did not become ready within timeout")
	}

	t.Cleanup(func() {
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
		_ = logFile.Close()
	})

	for i := 0; i < targetIssues; i++ {
		title := fmt.Sprintf("target issue %d", i+1)
		seedRuntimeMatrixDatabase(t, targetDataDir, port, targetDB, "tgt", title)
	}
	if createSource {
		for i := 0; i < sourceIssues; i++ {
			title := fmt.Sprintf("source issue %d", i+1)
			seedRuntimeMatrixDatabase(t, targetDataDir, port, sourceDB, "src", title)
		}
	}

	return &runtimeMatrixRedirectedRepo{
		RepoDir:        repoDir,
		SourceBeadsDir: sourceBeadsDir,
		TargetBeadsDir: targetBeadsDir,
		SourceDB:       sourceDB,
		TargetDB:       targetDB,
		Port:           port,
		Cleanup: func() {
			_ = serverCmd.Process.Kill()
			_ = serverCmd.Wait()
			_ = logFile.Close()
		},
	}
}

func seedRuntimeMatrixDatabase(t *testing.T, dataDir string, port int, database, prefix, title string) {
	t.Helper()

	restoreEnv := runtimeMatrixUnsetProcessEnv(t, "BEADS_TEST_MODE", "BEADS_DOLT_PORT", "BEADS_DOLT_SERVER_PORT")
	defer restoreEnv()

	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            dataDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		Database:        database,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("open seed store %q: %v", database, err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		t.Fatalf("set issue_prefix for %q: %v", database, err)
	}
	if title == "" {
		return
	}

	issue := &types.Issue{
		Title:     title,
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("create seed issue in %q: %v", database, err)
	}
}

func runtimeMatrixUnsetProcessEnv(t *testing.T, keys ...string) func() {
	t.Helper()

	type envState struct {
		value string
		ok    bool
	}
	saved := make(map[string]envState, len(keys))
	for _, key := range keys {
		value, ok := os.LookupEnv(key)
		saved[key] = envState{value: value, ok: ok}
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}

	return func() {
		for _, key := range keys {
			state := saved[key]
			var err error
			if state.ok {
				err = os.Setenv(key, state.value)
			} else {
				err = os.Unsetenv(key)
			}
			if err != nil {
				t.Fatalf("restore %s: %v", key, err)
			}
		}
	}
}

func writeRuntimeMatrixJSONL(t *testing.T, beadsDir string, issues []types.Issue) {
	t.Helper()

	path := filepath.Join(beadsDir, "issues.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create issues.jsonl: %v", err)
	}
	defer func() { _ = file.Close() }()

	enc := json.NewEncoder(file)
	for _, issue := range issues {
		if err := enc.Encode(issue); err != nil {
			t.Fatalf("encode issues.jsonl: %v", err)
		}
	}
}

func decodeDoctorResult(t *testing.T, output string) runtimeMatrixDoctorResult {
	t.Helper()

	jsonOut := extractJSONPayload(t, output)
	var result runtimeMatrixDoctorResult
	if err := json.Unmarshal([]byte(jsonOut), &result); err != nil {
		t.Fatalf("parse doctor json: %v\n%s", err, output)
	}
	return result
}

func decodeIssueObjects(t *testing.T, output string) []map[string]any {
	t.Helper()

	jsonOut := extractJSONPayload(t, output)
	var value any
	if err := json.Unmarshal([]byte(jsonOut), &value); err != nil {
		t.Fatalf("parse issue json: %v\n%s", err, output)
	}

	switch typed := value.(type) {
	case map[string]any:
		return []map[string]any{typed}
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			asMap, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("expected object item, got %T\n%s", item, output)
			}
			items = append(items, asMap)
		}
		return items
	default:
		t.Fatalf("unexpected json root %T\n%s", value, output)
		return nil
	}
}

func extractJSONPayload(t *testing.T, output string) string {
	t.Helper()

	trimmed := strings.TrimSpace(output)
	if json.Valid([]byte(trimmed)) {
		return trimmed
	}

	for idx, r := range output {
		if r != '{' && r != '[' {
			continue
		}
		candidate := strings.TrimSpace(output[idx:])
		if json.Valid([]byte(candidate)) {
			return candidate
		}
	}

	t.Fatalf("no json payload found\n%s", output)
	return ""
}

func stringField(item map[string]any, key string) string {
	if item == nil {
		return ""
	}
	if value, ok := item[key].(string); ok {
		return value
	}
	return ""
}

func assertContainsIssueIDs(t *testing.T, items []map[string]any, wantIDs ...string) {
	t.Helper()

	got := make(map[string]bool, len(items))
	for _, item := range items {
		got[stringField(item, "id")] = true
	}
	for _, want := range wantIDs {
		if !got[want] {
			t.Fatalf("expected issue %q in output, got ids=%v", want, got)
		}
	}
}

func assertTrackedServerRunning(t *testing.T, contextName string, statusOut string) {
	t.Helper()
	if !strings.Contains(statusOut, "Dolt server: running") {
		t.Fatalf("expected tracked server to be running after %s; output:\n%s", contextName, statusOut)
	}
	if strings.Contains(statusOut, "Expected port: 0") {
		t.Fatalf("expected non-stale runtime bookkeeping after %s; output:\n%s", contextName, statusOut)
	}
}

func assertStatusContainsPort(t *testing.T, statusOut string, wantPort int) {
	t.Helper()
	if !strings.Contains(statusOut, fmt.Sprintf("Port: %d", wantPort)) {
		t.Fatalf("status output missing port %d:\n%s", wantPort, statusOut)
	}
}

func extractStatusPort(t *testing.T, statusOut string) int {
	t.Helper()

	for _, line := range strings.Split(statusOut, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Port:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "Port:"))
		port, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("parse port from status %q: %v\n%s", value, err, statusOut)
		}
		return port
	}

	t.Fatalf("status output missing Port line:\n%s", statusOut)
	return 0
}

func assertDoctorCheckStatus(t *testing.T, result runtimeMatrixDoctorResult, name string, wantStatus string) {
	t.Helper()
	for _, check := range result.Checks {
		if check.Name == name {
			if check.Status != wantStatus {
				t.Fatalf("%s status = %q, want %q (message=%q detail=%q)", name, check.Status, wantStatus, check.Message, check.Detail)
			}
			return
		}
	}
	t.Fatalf("doctor result missing check %q", name)
}

func assertDoctorCheckMessageContains(t *testing.T, result runtimeMatrixDoctorResult, name string, want string) {
	t.Helper()
	for _, check := range result.Checks {
		if check.Name == name {
			if !strings.Contains(check.Message, want) {
				t.Fatalf("%s message = %q, want to contain %q", name, check.Message, want)
			}
			return
		}
	}
	t.Fatalf("doctor result missing check %q", name)
}

func openRuntimeMatrixRootDB(t *testing.T, port int) *sql.DB {
	t.Helper()

	db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/?parseTime=true&timeout=5s", port))
	if err != nil {
		t.Fatalf("sql.Open root DB: %v", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatalf("db.Ping root DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
