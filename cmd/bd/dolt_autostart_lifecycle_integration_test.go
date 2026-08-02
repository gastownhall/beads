//go:build cgo && integration

package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestE2E_AutoStartedRepoLocalServerPersistsAcrossCommands(t *testing.T) {
	if !usesSQLServer() {
		t.Skip("skipping: bd dolt status not supported in embedded mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("repo-local dolt lifecycle integration test not supported on windows")
	}

	bdBinary := buildLifecycleTestBinary(t)
	tmpDir := t.TempDir()
	if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	_ = runCommandInDir(tmpDir, "git", "config", "user.email", "test@example.com")
	_ = runCommandInDir(tmpDir, "git", "config", "user.name", "Test User")
	_ = runCommandInDir(tmpDir, "git", "config", "remote.origin.url", "https://github.com/test/repo.git")

	env := append(os.Environ(),
		"BEADS_TEST_MODE=",
		"GT_ROOT=",
		"BEADS_DOLT_AUTO_START=",
		"BEADS_DOLT_SERVER_PORT=",
		"BEADS_DOLT_PORT=",
		"BEADS_DOLT_SHARED_SERVER=",
		"GIT_TERMINAL_PROMPT=0",
		"SSH_ASKPASS=",
		"GIT_ASKPASS=",
	)

	initOut, initErr := runBDExecWithBinary(t, bdBinary, tmpDir, env, "init", "--backend", "dolt", "--server", "--prefix", "test", "--quiet")
	if initErr != nil {
		lower := strings.ToLower(initOut)
		if strings.Contains(lower, "dolt") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "not available") || strings.Contains(lower, "unknown")) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	createOut, createErr := runBDExecWithBinary(t, bdBinary, tmpDir, env, "create", "auto-start persists", "--json")
	if createErr != nil {
		t.Fatalf("bd create failed: %v\n%s", createErr, createOut)
	}
	var created map[string]any
	createJSON := createOut
	if jsonStart := strings.Index(createOut, "{"); jsonStart >= 0 {
		createJSON = createOut[jsonStart:]
	}
	if err := json.Unmarshal([]byte(createJSON), &created); err != nil {
		t.Fatalf("parse create json: %v\n%s", err, createOut)
	}
	issueID, _ := created["id"].(string)
	if issueID == "" {
		t.Fatalf("expected created issue id, got: %#v", created["id"])
	}

	statusOut, _ := runBDExecWithBinary(t, bdBinary, tmpDir, env, "dolt", "status")
	if strings.Contains(statusOut, "Dolt server: running") {
		stopOut, stopErr := runBDExecWithBinary(t, bdBinary, tmpDir, env, "dolt", "stop")
		if stopErr != nil {
			t.Fatalf("bd dolt stop failed: %v\n%s", stopErr, stopOut)
		}
	}

	statusOut, statusErr := runBDExecWithBinary(t, bdBinary, tmpDir, env, "dolt", "status")
	if statusErr != nil {
		t.Fatalf("bd dolt status before auto-start failed: %v\n%s", statusErr, statusOut)
	}
	if !strings.Contains(statusOut, "Dolt server: not running") {
		t.Fatalf("expected stopped baseline before show; output:\n%s", statusOut)
	}

	showOut, showErr := runBDExecWithBinary(t, bdBinary, tmpDir, env, "show", issueID, "--json")
	if showErr != nil {
		t.Fatalf("bd show failed: %v\n%s", showErr, showOut)
	}

	statusOut, statusErr = runBDExecWithBinary(t, bdBinary, tmpDir, env, "dolt", "status")
	if statusErr != nil {
		t.Fatalf("bd dolt status after auto-start failed: %v\n%s", statusErr, statusOut)
	}
	if !strings.Contains(statusOut, "Dolt server: running") {
		t.Fatalf("expected auto-started server to remain running after bd show; output:\n%s", statusOut)
	}
	if strings.Contains(statusOut, "Expected port: 0") {
		t.Fatalf("expected live tracked server after bd show, got stale endpoint bookkeeping:\n%s", statusOut)
	}

	portBytes, err := os.ReadFile(filepath.Join(tmpDir, ".beads", "dolt-server.port"))
	if err != nil {
		t.Fatalf("read dolt-server.port: %v", err)
	}
	port := strings.TrimSpace(string(portBytes))
	if port == "" {
		t.Fatal("expected non-empty dolt-server.port after auto-start")
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 500*time.Millisecond)
	if err != nil {
		t.Fatalf("expected auto-started server to keep listening on %s: %v", port, err)
	}
	_ = conn.Close()

	stopOut, stopErr := runBDExecWithBinary(t, bdBinary, tmpDir, env, "dolt", "stop")
	if stopErr != nil {
		t.Fatalf("bd dolt stop cleanup failed: %v\n%s", stopErr, stopOut)
	}
}

func buildLifecycleTestBinary(t *testing.T) string {
	t.Helper()
	// Shared once-per-process binary (honors BEADS_TEST_BD_BINARY) instead of
	// a per-test go build — the in-test link steps dominated cmd/bd's wall
	// clock (wy-4mtr0).
	return buildBDForInitTests(t)
}

func runBDExecWithBinary(t *testing.T, bdBinary string, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bdBinary, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}
