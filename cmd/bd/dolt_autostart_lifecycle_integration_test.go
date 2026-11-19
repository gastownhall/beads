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

	baseEnv := idleLifecycleBaseEnv()
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
	portBytes, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.port"))
	if err != nil {
		t.Fatalf("read the auto-started server's port file: %v", err)
	}
	firstPort := strings.TrimSpace(string(portBytes))

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
	waitForIdleReapedPortClosed(t, firstPort, 10*time.Second)

	// The reap removed the server's state files and the watcher's record.
	// The activity directory itself stays: it may hold live client leases.
	for name, path := range map[string]string{
		"pid file":    pidFile,
		"port file":   filepath.Join(beadsDir, "dolt-server.port"),
		"watcher.pid": filepath.Join(beadsDir, "dolt-server.activity", "watcher.pid"),
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

// idleLifecycleBaseEnv clears the environment that would pin the port,
// share a server or override the idle window for the bd subprocesses of the
// idle-stop end-to-end tests.
func idleLifecycleBaseEnv() []string {
	return []string{
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
}

// waitForIdleReapedPortClosed fails the test unless nothing accepts
// connections on 127.0.0.1:port within timeout: the reaped server is gone.
func waitForIdleReapedPortClosed(t *testing.T, port string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 500*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("the reaped server's port %s still accepts connections %s after its state files were removed", port, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestE2E_DoltStatusAllAndStopAll walks GH#4282's machine-wide surface
// through the real bd binary: two auto-started workspace servers and one
// dolt sql-server bd never started, seen together by `bd dolt status --all`
// (text and --json) from a directory that is no workspace, bd's own listed
// with workspace, PID, port, uptime and idle-stop state and the foreign one
// as not managed by bd; then `bd dolt stop --all` stops and reports both bd
// servers and leaves the foreign one running.
func TestE2E_DoltStatusAllAndStopAll(t *testing.T) {
	// Every bd here is a subprocess on workspaces initialized with
	// --server, so this process's own storage mode (embedded by default)
	// does not matter; the subprocess needs the dolt binary.
	testutil.RequireDoltBinary(t)
	if runtime.GOOS == windowsOS {
		t.Skip("repo-local dolt lifecycle integration test not supported on windows")
	}

	bdBinary := buildLifecycleTestBinary(t)
	env := append(append([]string{}, os.Environ()...), append(idleLifecycleBaseEnv(), "BEADS_DOLT_IDLE_TIMEOUT=")...)

	// Two workspaces, each with an auto-started server of its own.
	type wsFixture struct {
		dir      string
		beadsDir string
		pid      int
		port     string
	}
	var workspaces [2]wsFixture
	for i := range workspaces {
		// bd names a workspace by the path it runs in; resolve the temp
		// dir's symlinks so the listing can be compared with it.
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
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
	foreignPort := strconv.Itoa(reserveFreePort(t))
	foreign := exec.Command("dolt", "sql-server", "-H", "127.0.0.1", "-P", foreignPort)
	foreign.Dir = foreignDir
	if err := foreign.Start(); err != nil {
		t.Fatalf("start the foreign dolt sql-server: %v", err)
	}
	t.Cleanup(func() {
		_ = foreign.Process.Kill()
		_, _ = foreign.Process.Wait()
	})
	waitForPortAccepting(t, foreignPort, 30*time.Second)
	foreignPID := foreign.Process.Pid

	// --all lists everything from a directory that is no workspace.
	outside := t.TempDir()
	allOut, allErr := runBDExecWithBinary(t, bdBinary, outside, env, "dolt", "status", "--all")
	if allErr != nil {
		t.Fatalf("bd dolt status --all from a non-workspace directory failed: %v\n%s", allErr, allOut)
	}
	for i, ws := range workspaces {
		want := fmt.Sprintf("● %s\n      PID %d  Port %s  Uptime ", ws.dir, ws.pid, ws.port)
		if !strings.Contains(allOut, want) {
			t.Errorf("bd dolt status --all does not list workspace %d's server (%q):\n%s", i, want, allOut)
			continue
		}
		line := allOut[strings.Index(allOut, want)+len(want):]
		if nl := strings.Index(line, "\n"); nl >= 0 {
			line = line[:nl]
		}
		if !strings.Contains(line, "Idle stop: after 30m") {
			t.Errorf("workspace %d's server line lacks its idle-stop state: %q", i, line)
		}
	}
	for _, want := range []string{
		"Other Dolt servers (not managed by bd):",
		fmt.Sprintf("○ PID %d  ", foreignPID),
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
	type listedServer struct {
		PID           int    `json:"pid"`
		Managed       bool   `json:"managed"`
		Workspace     string `json:"workspace"`
		Port          int    `json:"port"`
		UptimeSeconds int64  `json:"uptime_seconds"`
		StartedAt     string `json:"started_at"`
		IdleStop      string `json:"idle_stop"`
	}
	var listed struct {
		Servers []listedServer `json:"servers"`
	}
	jsonStart := strings.Index(jsonOut, "{")
	if jsonStart < 0 {
		t.Fatalf("bd dolt status --all --json printed no JSON object:\n%s", jsonOut)
	}
	if err := json.Unmarshal([]byte(jsonOut[jsonStart:]), &listed); err != nil {
		t.Fatalf("parse --all json: %v\n%s", err, jsonOut)
	}
	byPID := map[int]listedServer{}
	for _, s := range listed.Servers {
		byPID[s.PID] = s
	}
	for i, ws := range workspaces {
		s, ok := byPID[ws.pid]
		if !ok || !s.Managed || s.Workspace != ws.dir || strconv.Itoa(s.Port) != ws.port ||
			s.StartedAt == "" || s.IdleStop != "after 30m" {
			t.Errorf("workspace %d's server in --all json = %+v (found %t), want managed at %s on port %s, idle stop after 30m",
				i, s, ok, ws.dir, ws.port)
		}
	}
	if s, ok := byPID[foreignPID]; !ok || s.Managed || s.Workspace != "" {
		t.Errorf("the foreign server in --all json = %+v (found %t), want not managed by bd", s, ok)
	}

	// stop --all stops every server bd manages on the machine. On a shared
	// host that includes other projects' servers, so bd runs here behind a
	// `ps` that lists only this test's three dolt processes (bd discovers
	// servers through `ps` on PATH and nothing else), and the listing under
	// that shim is checked before anything is stopped.
	ownPIDs := []int{workspaces[0].pid, workspaces[1].pid, foreignPID}
	stopEnv := append(append([]string{}, env...), "PATH="+scopedPSDir(t, ownPIDs)+string(os.PathListSeparator)+os.Getenv("PATH"))
	scopedOut, scopedErr := runBDExecWithBinary(t, bdBinary, outside, stopEnv, "dolt", "status", "--all", "--json")
	if scopedErr != nil {
		t.Fatalf("bd dolt status --all --json behind the scoped ps failed: %v\n%s", scopedErr, scopedOut)
	}
	var scoped struct {
		Servers []listedServer `json:"servers"`
	}
	if i := strings.Index(scopedOut, "{"); i < 0 || json.Unmarshal([]byte(scopedOut[i:]), &scoped) != nil {
		t.Fatalf("parse scoped --all json:\n%s", scopedOut)
	}
	seen := map[int]bool{}
	for _, s := range scoped.Servers {
		seen[s.PID] = true
	}
	if len(scoped.Servers) != len(ownPIDs) || !seen[ownPIDs[0]] || !seen[ownPIDs[1]] || !seen[ownPIDs[2]] {
		t.Fatalf("the scoped ps lists %+v, want exactly PIDs %v; not running stop --all", scoped.Servers, ownPIDs)
	}

	stopOut, stopErr := runBDExecWithBinary(t, bdBinary, outside, stopEnv, "dolt", "stop", "--all")
	if stopErr != nil {
		t.Fatalf("bd dolt stop --all failed: %v\n%s", stopErr, stopOut)
	}
	for i, ws := range workspaces {
		if want := fmt.Sprintf("✓ Stopped Dolt server for %s (PID %d)", ws.dir, ws.pid); !strings.Contains(stopOut, want) {
			t.Errorf("bd dolt stop --all does not report workspace %d's stop (%q):\n%s", i, want, stopOut)
		}
		if _, err := os.Stat(filepath.Join(ws.beadsDir, "dolt-server.pid")); err == nil {
			t.Errorf("workspace %d's pid file survived stop --all", i)
		}
		waitForIdleReapedPortClosed(t, ws.port, 10*time.Second)
	}
	if want := fmt.Sprintf("Left running 1 Dolt server not managed by bd (PID %d)", foreignPID); !strings.Contains(stopOut, want) {
		t.Errorf("bd dolt stop --all does not report the foreign server left running (%q):\n%s", want, stopOut)
	}
	waitForPortAccepting(t, foreignPort, 2*time.Second)
}

// scopedPSDir returns a directory holding a `ps` that runs the real one and
// keeps only the rows of pids, so a bd run with it first on PATH discovers
// no dolt server but those.
func scopedPSDir(t *testing.T, pids []int) string {
	t.Helper()
	realPS, err := exec.LookPath("ps")
	if err != nil {
		t.Skipf("no ps on PATH: %v", err)
	}
	keep := make([]string, 0, len(pids))
	for _, pid := range pids {
		keep = append(keep, strconv.Itoa(pid))
	}
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\n%s \"$@\" | awk -v keep=' %s ' 'index(keep, \" \" $1 \" \") > 0'\n",
		strconv.Quote(realPS), strings.Join(keep, " "))
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: the shim must be executable
		t.Fatal(err)
	}
	return dir
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

// waitForPortAccepting fails the test unless 127.0.0.1:port accepts a
// connection within timeout.
func waitForPortAccepting(t *testing.T, port string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing accepts connections on port %s after %s: %v", port, timeout, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
