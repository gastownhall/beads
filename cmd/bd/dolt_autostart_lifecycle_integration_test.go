//go:build cgo && integration

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/testutil"
)

func TestE2E_AutoStartedRepoLocalServerPersistsAcrossCommands(t *testing.T) {
	// Every bd here is a subprocess on a workspace initialized with
	// --server, so this process's own storage mode (embedded by default)
	// does not matter; the subprocess needs the dolt binary.
	testutil.RequireDoltBinary(t)
	if runtime.GOOS == windowsOS {
		t.Skip("repo-local dolt lifecycle integration test not supported on windows")
	}

	bdBinary := buildLifecycleTestBinary(t)
	tmpDir := t.TempDir()
	// Stops the auto-started repo-local server if an assertion fails
	// before the test's own final stop.
	stopDoltServerCleanup(t, filepath.Join(tmpDir, ".beads"))
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

// TestE2E_NestedServerInitStartsItsOwnServer runs bd init --server in a
// project nested in a workspace whose config.yaml names dolt.port, the port
// of that workspace's running server, with no port named for the new
// project. Init loads that config.yaml before the project's .beads exists;
// on main its store took that port, created the project's database on the
// enclosing workspace's server and pinned the port in the project's port
// file. It must instead start a server of the project's own, on a port of
// its own, as init does for a project nested in nothing, and leave the
// enclosing workspace's server without the project's database. With a
// user-level dolt.port that nothing answers on, that port is the project's
// own, as it is for a project nested in nothing: init starts the project's
// server on it.
func TestE2E_NestedServerInitStartsItsOwnServer(t *testing.T) {
	testutil.RequireDoltBinary(t)
	if runtime.GOOS == windowsOS {
		t.Skip("repo-local dolt lifecycle integration test not supported on windows")
	}

	bdBinary := buildLifecycleTestBinary(t)
	t.Run("no port of its own", func(t *testing.T) {
		runNestedServerInit(t, bdBinary, 0)
	})
	t.Run("user-level dolt.port nothing answers on", func(t *testing.T) {
		runNestedServerInit(t, bdBinary, freeLoopbackPort(t))
	})
}

// runNestedServerInit is TestE2E_NestedServerInitStartsItsOwnServer with
// userPort, when nonzero, as the user-level config.yaml dolt.port.
func runNestedServerInit(t *testing.T, bdBinary string, userPort int) {
	root := t.TempDir()
	home := t.TempDir()
	town := filepath.Join(root, "town")
	rig := filepath.Join(town, "rig")
	townBeads, rigBeads := filepath.Join(town, ".beads"), filepath.Join(rig, ".beads")
	stopDoltServerCleanup(t, townBeads)
	stopDoltServerCleanup(t, rigBeads)
	for _, dir := range []string{town, rig} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := runCommandInDir(dir, "git", "init"); err != nil {
			t.Fatalf("git init %s failed: %v", dir, err)
		}
	}

	env := append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"BEADS_DIR=",
		"BEADS_CEILING_DIRECTORIES="+root,
		"BEADS_TEST_MODE=",
		"GT_ROOT=",
		"BEADS_DOLT_AUTO_START=",
		"BEADS_DOLT_SERVER_PORT=",
		"BEADS_DOLT_PORT=",
		"BEADS_DOLT_SERVER_HOST=",
		"BEADS_DOLT_SERVER_SOCKET=",
		"BEADS_DOLT_SERVER_MODE=",
		"BEADS_DOLT_SHARED_SERVER=",
		"BD_NON_INTERACTIVE=1",
		"BD_DISABLE_METRICS=1",
		"GIT_TERMINAL_PROMPT=0",
	)

	if out, err := runBDExecWithBinary(t, bdBinary, town, env, "init", "--server", "--prefix", "town", "--quiet", "--skip-hooks", "--skip-agents"); err != nil {
		t.Fatalf("bd init in the town failed: %v\n%s", err, out)
	}
	townPort, err := os.ReadFile(filepath.Join(townBeads, "dolt-server.port"))
	if err != nil {
		t.Fatalf("read the town's dolt-server.port: %v", err)
	}
	// The town pins its server's port, as a Gas Town town does.
	if err := os.WriteFile(filepath.Join(townBeads, "config.yaml"), []byte("dolt:\n  port: "+strings.TrimSpace(string(townPort))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if userPort != 0 {
		userConfig := filepath.Join(home, ".config", "bd", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(userConfig), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(userConfig, []byte(fmt.Sprintf("dolt:\n  port: %d\n", userPort)), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runBDExecWithBinary(t, bdBinary, rig, env, "init", "--server", "--prefix", "rig", "--quiet", "--skip-hooks", "--skip-agents")
	if err != nil {
		t.Fatalf("bd init in the nested rig failed: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(townBeads, "dolt", "rig")); err == nil {
		t.Errorf("bd init in the nested rig created its database on the town's server (%s exists):\n%s", filepath.Join(townBeads, "dolt", "rig"), out)
	}
	rigPort, err := os.ReadFile(filepath.Join(rigBeads, "dolt-server.port"))
	if err != nil {
		t.Fatalf("read the rig's dolt-server.port: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(rigPort)) == strings.TrimSpace(string(townPort)) {
		t.Errorf("the rig's port file names the town's port %s:\n%s", strings.TrimSpace(string(townPort)), out)
	}
	if userPort != 0 && strings.TrimSpace(string(rigPort)) != strconv.Itoa(userPort) {
		t.Errorf("the rig's port file names %s, want the user-level dolt.port %d:\n%s", strings.TrimSpace(string(rigPort)), userPort, out)
	}
	if _, err := os.Stat(filepath.Join(rigBeads, "dolt", "rig")); err != nil {
		t.Errorf("the rig's database is not in the rig's own server data directory: %v\n%s", err, out)
	}

	if out, err := runBDExecWithBinary(t, bdBinary, rig, env, "create", "made in the rig", "--json"); err != nil {
		t.Fatalf("bd create in the rig failed: %v\n%s", err, out)
	}
	listOut, err := runBDExecWithBinary(t, bdBinary, town, env, "list", "--json")
	if err != nil {
		t.Fatalf("bd list in the town failed: %v\n%s", err, listOut)
	}
	if strings.Contains(listOut, "made in the rig") {
		t.Errorf("the town lists the rig's issue:\n%s", listOut)
	}
}
