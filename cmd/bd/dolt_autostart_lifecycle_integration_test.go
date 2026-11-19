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

	"github.com/steveyegge/beads/internal/doltserver"
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

// TestE2E_AutoStartedServerIsReapedAfterIdleWindow is GH#4282's acceptance,
// end to end through the real bd binary: an auto-started repo-local server
// whose idle window is 3s stops itself once no bd command holds it, its
// state files go, and the next bd command auto-starts a fresh server.
func TestE2E_AutoStartedServerIsReapedAfterIdleWindow(t *testing.T) {
	// Every bd here is a subprocess on a workspace initialized with
	// --server, so this process's own storage mode (embedded by default)
	// does not matter; the subprocess needs the dolt binary.
	testutil.RequireDoltBinary(t)
	if runtime.GOOS == windowsOS {
		t.Skip("repo-local dolt lifecycle integration test not supported on windows")
	}

	bdBinary := buildLifecycleTestBinary(t)
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	// Stops the auto-started repo-local server if an assertion fails before
	// the test's own final stop.
	stopDoltServerCleanup(t, beadsDir)
	if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	_ = runCommandInDir(tmpDir, "git", "config", "user.email", "test@example.com")
	_ = runCommandInDir(tmpDir, "git", "config", "user.name", "Test User")
	_ = runCommandInDir(tmpDir, "git", "config", "remote.origin.url", "https://github.com/test/repo.git")

	baseEnv := []string{
		"BEADS_TEST_MODE=",
		"GT_ROOT=",
		"BEADS_DOLT_AUTO_START=",
		"BEADS_DOLT_SERVER_PORT=",
		"BEADS_DOLT_PORT=",
		"BEADS_DOLT_SHARED_SERVER=",
		"GIT_TERMINAL_PROMPT=0",
		"SSH_ASKPASS=",
		"GIT_ASKPASS=",
	}
	// The server this test starts watches with a 3s window so the reap is
	// observable; the post-reap commands run without the override so the
	// replacement server a later bd command starts keeps the 30m default
	// and survives the test's remaining steps.
	envIdle := append(append([]string{}, os.Environ()...), append(baseEnv, "BEADS_DOLT_IDLE_TIMEOUT=3s")...)
	envCalm := append(append([]string{}, os.Environ()...), append(baseEnv, "BEADS_DOLT_IDLE_TIMEOUT=")...)

	initOut, initErr := runBDExecWithBinary(t, bdBinary, tmpDir, envIdle, "init", "--backend", "dolt", "--server", "--prefix", "reap", "--quiet")
	if initErr != nil {
		lower := strings.ToLower(initOut)
		if strings.Contains(lower, "dolt") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "not available") || strings.Contains(lower, "unknown")) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	listOut, listErr := runBDExecWithBinary(t, bdBinary, tmpDir, envIdle, "list")
	if listErr != nil {
		t.Fatalf("bd list failed: %v\n%s", listErr, listOut)
	}

	pidFile := filepath.Join(beadsDir, "dolt-server.pid")
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read the auto-started server's pid file: %v", err)
	}
	firstPID, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse pid file %q: %v", pidBytes, err)
	}

	// The bd commands have exited, so nothing holds the server: the watcher
	// stops it within the 3s window plus a poll. 20s is generous.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(pidFile); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			statusOut, _ := runBDExecWithBinary(t, bdBinary, tmpDir, envCalm, "dolt", "status")
			t.Fatalf("auto-started server still running 20s after its idle window; status:\n%s", statusOut)
		}
		time.Sleep(250 * time.Millisecond)
	}

	// The reap cleaned up every state file, the activity directory included.
	for name, path := range map[string]string{
		"pid file":     pidFile,
		"port file":    filepath.Join(beadsDir, "dolt-server.port"),
		"activity dir": filepath.Join(beadsDir, "dolt-server.activity"),
	} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s still exists after the idle reap", name)
		}
	}

	// The next bd command auto-starts a fresh server and succeeds.
	secondOut, secondErr := runBDExecWithBinary(t, bdBinary, tmpDir, envCalm, "list")
	if secondErr != nil {
		t.Fatalf("bd list after the idle reap failed: %v\n%s", secondErr, secondOut)
	}
	pidBytes, err = os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read the re-started server's pid file: %v", err)
	}
	secondPID, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse pid file %q: %v", pidBytes, err)
	}
	if secondPID == firstPID {
		t.Errorf("bd list after the reap reused the reaped server's PID %d", secondPID)
	}

	stopOut, stopErr := runBDExecWithBinary(t, bdBinary, tmpDir, envCalm, "dolt", "stop")
	if stopErr != nil {
		t.Fatalf("bd dolt stop cleanup failed: %v\n%s", stopErr, stopOut)
	}
}

// TestE2E_DoltStatusAllAndStopAll walks GH#4282's machine-wide lifecycle:
// two auto-started workspace servers and one sql-server bd never started,
// seen together through `bd dolt status --all` from a directory that is no
// workspace at all, bd's own listed with their full state and the foreign
// one reported as not managed by bd.
func TestE2E_DoltStatusAllAndStopAll(t *testing.T) {
	// Every bd here is a subprocess on workspaces initialized with
	// --server, so this process's own storage mode (embedded by default)
	// does not matter; the subprocess needs the dolt binary.
	testutil.RequireDoltBinary(t)
	if runtime.GOOS == windowsOS {
		t.Skip("repo-local dolt lifecycle integration test not supported on windows")
	}

	bdBinary := buildLifecycleTestBinary(t)

	baseEnv := []string{
		"BEADS_TEST_MODE=",
		"GT_ROOT=",
		"BEADS_DOLT_AUTO_START=",
		"BEADS_DOLT_SERVER_PORT=",
		"BEADS_DOLT_PORT=",
		"BEADS_DOLT_SHARED_SERVER=",
		"BEADS_DOLT_IDLE_TIMEOUT=",
		"GIT_TERMINAL_PROMPT=0",
		"SSH_ASKPASS=",
		"GIT_ASKPASS=",
	}
	env := append(append([]string{}, os.Environ()...), baseEnv...)

	// Two workspaces, each with an auto-started server of its own.
	type wsFixture struct {
		dir      string
		beadsDir string
		pid      int
		port     string
	}
	var workspaces [2]wsFixture
	for i := range workspaces {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		stopDoltServerCleanup(t, beadsDir)
		if err := runCommandInDir(dir, "git", "init"); err != nil {
			t.Fatalf("git init failed: %v", err)
		}
		_ = runCommandInDir(dir, "git", "config", "user.email", "test@example.com")
		_ = runCommandInDir(dir, "git", "config", "user.name", "Test User")
		_ = runCommandInDir(dir, "git", "config", "remote.origin.url", "https://github.com/test/repo.git")
		if out, err := runBDExecWithBinary(t, bdBinary, dir, env, "init", "--backend", "dolt", "--server", "--prefix", "all", "--quiet"); err != nil {
			t.Skipf("dolt backend not available: %s", out)
		}
		if out, err := runBDExecWithBinary(t, bdBinary, dir, env, "list"); err != nil {
			t.Fatalf("bd list in workspace %d failed: %v\n%s", i, err, out)
		}
		pidBytes, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.pid"))
		if err != nil {
			t.Fatalf("read workspace %d pid file: %v", i, err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
		if err != nil {
			t.Fatalf("parse workspace %d pid file: %v", i, err)
		}
		portBytes, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.port"))
		if err != nil {
			t.Fatalf("read workspace %d port file: %v", i, err)
		}
		workspaces[i] = wsFixture{dir: dir, beadsDir: beadsDir, pid: pid, port: strings.TrimSpace(string(portBytes))}
	}

	// A dolt sql-server bd never started, in a directory bd would never
	// attribute to itself: no --config, a cwd that is no server's.
	foreignDir := t.TempDir()
	if err := runCommandInDir(foreignDir, "dolt", "init"); err != nil {
		t.Fatalf("dolt init for the foreign server: %v", err)
	}
	foreignPort := reserveFreePort(t)
	foreign := exec.Command("dolt", "sql-server", "-H", "127.0.0.1", "-P", strconv.Itoa(foreignPort))
	foreign.Dir = foreignDir
	foreign.Stdout, foreign.Stderr = nil, nil
	if err := foreign.Start(); err != nil {
		t.Fatalf("start the foreign dolt sql-server: %v", err)
	}
	t.Cleanup(func() {
		_ = foreign.Process.Kill()
		_, _ = foreign.Process.Wait()
	})

	// --all lists everything from a directory that is no workspace.
	outside := t.TempDir()
	allOut, allErr := runBDExecWithBinary(t, bdBinary, outside, env, "dolt", "status", "--all")
	if allErr != nil {
		t.Fatalf("bd dolt status --all from a non-workspace directory failed: %v\n%s", allErr, allOut)
	}
	for i, ws := range workspaces {
		for _, want := range []string{
			"Dolt server: running (bd-managed)",
			"Workspace: " + ws.beadsDir,
			"PID:       " + strconv.Itoa(ws.pid),
			"Port:      " + ws.port,
			"Idle stop: stops after",
		} {
			if !strings.Contains(allOut, want) {
				t.Errorf("bd dolt status --all does not list workspace %d's server (%q):\n%s", i, want, allOut)
			}
		}
	}
	for _, want := range []string{
		"Dolt server: running (not managed by bd)",
		"PID:       " + strconv.Itoa(foreign.Process.Pid),
	} {
		if !strings.Contains(allOut, want) {
			t.Errorf("bd dolt status --all does not list the foreign server (%q):\n%s", want, allOut)
		}
	}

	// --all --json reports the same facts for agents.
	jsonOut, jsonErr := runBDExecWithBinary(t, bdBinary, outside, env, "dolt", "status", "--all", "--json")
	if jsonErr != nil {
		t.Fatalf("bd dolt status --all --json failed: %v\n%s", jsonErr, jsonOut)
	}
	var servers []doltserver.DiscoveredServer
	if jsonStart := strings.Index(jsonOut, "["); jsonStart >= 0 {
		if err := json.Unmarshal([]byte(jsonOut[jsonStart:]), &servers); err != nil {
			t.Fatalf("parse --all json: %v\n%s", err, jsonOut)
		}
	}
	byPID := map[int]doltserver.DiscoveredServer{}
	for _, s := range servers {
		byPID[s.PID] = s
	}
	for i, ws := range workspaces {
		s, ok := byPID[ws.pid]
		if !ok || !s.BDManaged || s.ServerDir != ws.beadsDir {
			t.Errorf("workspace %d's server in --all json = %+v (found %t), want bd-managed at %s", i, s, ok, ws.beadsDir)
		}
	}
	if s, ok := byPID[foreign.Process.Pid]; !ok || s.BDManaged {
		t.Errorf("the foreign server in --all json = %+v (found %t), want not bd-managed", s, ok)
	}
}

// reserveFreePort reserves an ephemeral TCP port for a test server.
func reserveFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}
