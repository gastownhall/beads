//go:build cgo && integration && !windows

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// TestDoltRestartCommandSharedServer drives `bd dolt restart` and
// `bd dolt set remotesapi-port` through the built binary against a real
// shared dolt sql-server: the SQL port survives the restart, the configured
// remotesapi listener comes up, and a configuration the new server could not
// start with is refused while the running server keeps serving.
func TestDoltRestartCommandSharedServer(t *testing.T) {
	doltBin := integration.RequireDolt(t)

	tmp := createTempDirWithCleanup(t)
	home := filepath.Join(tmp, "home")
	shared := filepath.Join(tmp, "shared")
	project := filepath.Join(tmp, "project")
	for _, dir := range []string{filepath.Join(home, ".config", "bd"), shared, project, filepath.Join(tmp, "tmp")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "bd", "config.yaml"), []byte("metrics:\n  disabled: true\n  notice_shown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ports := reserveFreeTCPPorts(t, 2)
	sqlPort, rapiPort := ports[0], ports[1]

	// A minimal environment: the ambient one may carry BEADS_*/GT_ROOT
	// values that redirect the child to a real workspace.
	baseEnv := []string{
		"PATH=" + filepath.Dir(doltBin) + ":/usr/bin:/bin",
		"HOME=" + home,
		"USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"TMPDIR=" + filepath.Join(tmp, "tmp"),
		"DOLT_ROOT_PATH=" + home,
		"BEADS_DOLT_SHARED_SERVER=1",
		"BEADS_SHARED_SERVER_DIR=" + shared,
		"BEADS_DOLT_SERVER_PORT=" + strconv.Itoa(sqlPort),
		"BEADS_DOLT_AUTO_START=0",
		"BD_NON_INTERACTIVE=1",
	}
	bd := func(args ...string) (string, error) {
		return runBDExecAllowErrorWithEnv(t, project, baseEnv, args...)
	}
	t.Cleanup(func() {
		_, _ = bd("dolt", "stop")
		waitForPortState(t, sqlPort, false, 20*time.Second)
	})

	if out, err := bd("init", "--shared-server", "--prefix", "rt", "--role", "maintainer", "--skip-hooks", "--skip-agents", "--non-interactive"); err != nil {
		t.Fatalf("bd init: %v\n%s", err, out)
	}
	if out, err := bd("dolt", "start"); err != nil {
		t.Fatalf("bd dolt start: %v\n%s", err, out)
	}
	waitForPortState(t, sqlPort, true, 20*time.Second)
	first, err := doltserver.IsRunning(shared)
	if err != nil || first == nil || !first.Running {
		t.Fatalf("shared server state after start = %+v (err %v), want running", first, err)
	}

	// Set-time refusal: the shared server's own SQL port can never be applied
	// without taking the server down, so it is rejected before it is stored.
	if out, err := bd("dolt", "set", "remotesapi-port", strconv.Itoa(sqlPort)); err == nil || !strings.Contains(out, "equals the shared Dolt server's SQL port") {
		t.Fatalf("set remotesapi-port to the SQL port: err=%v, want refusal; output:\n%s", err, out)
	}
	if out, err := bd("dolt", "set", "remotesapi-port", strconv.Itoa(rapiPort)); err != nil {
		t.Fatalf("bd dolt set remotesapi-port: %v\n%s", err, out)
	}

	restartOut, err := bd("--json", "dolt", "restart")
	if err != nil {
		t.Fatalf("bd dolt restart --json: %v\n%s", err, restartOut)
	}
	var restarted struct {
		Restarted      bool `json:"restarted"`
		WasRunning     bool `json:"was_running"`
		PID            int  `json:"pid"`
		Port           int  `json:"port"`
		RemotesAPIPort int  `json:"remotesapi_port"`
	}
	if jsonErr := json.Unmarshal([]byte(restartOut[strings.Index(restartOut, "{"):]), &restarted); jsonErr != nil {
		t.Fatalf("decode restart JSON: %v\n%s", jsonErr, restartOut)
	}
	if !restarted.Restarted || !restarted.WasRunning || restarted.Port != sqlPort || restarted.RemotesAPIPort != rapiPort || restarted.PID == first.PID {
		t.Fatalf("restart = %+v, want restarted on preserved SQL port %d with remotesapi %d and a new PID (was %d)", restarted, sqlPort, rapiPort, first.PID)
	}
	if !doltserver.ProbeRemotesAPI(rapiPort) {
		t.Fatalf("remotesapi port %d not accepting connections after restart", rapiPort)
	}

	statusOut, err := bd("--json", "dolt", "status")
	if err != nil {
		t.Fatalf("bd dolt status --json: %v\n%s", err, statusOut)
	}
	var status map[string]any
	if jsonErr := json.Unmarshal([]byte(statusOut[strings.Index(statusOut, "{"):]), &status); jsonErr != nil {
		t.Fatalf("decode status JSON: %v\n%s", jsonErr, statusOut)
	}
	if status["remotesapi_reachable"] != true || int(status["remotesapi_port"].(float64)) != rapiPort {
		t.Fatalf("status = %v, want remotesapi %d reachable", status, rapiPort)
	}

	// Restart preflight: an occupied remotesapi port is refused before the
	// running server is stopped, so SQL keeps serving.
	blocker, err := net.Listen("tcp", net.JoinHostPort("", "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	busyPort := blocker.Addr().(*net.TCPAddr).Port
	if out, err := bd("dolt", "set", "remotesapi-port", strconv.Itoa(busyPort)); err != nil {
		t.Fatalf("bd dolt set remotesapi-port (busy): %v\n%s", err, out)
	}
	refusedOut, err := bd("--json", "dolt", "restart")
	if err == nil {
		t.Fatalf("bd dolt restart succeeded with an occupied remotesapi port:\n%s", refusedOut)
	}
	var refused struct {
		Error string `json:"error"`
	}
	if jsonErr := json.Unmarshal([]byte(refusedOut[strings.Index(refusedOut, "{"):]), &refused); jsonErr != nil {
		t.Fatalf("refusal is not JSON under --json (%v):\n%s", jsonErr, refusedOut)
	}
	for _, want := range []string{"server left running", fmt.Sprintf("remotesapi port %d", busyPort), "bd dolt set remotesapi-port"} {
		if !strings.Contains(refused.Error, want) {
			t.Fatalf("refusal %q missing %q", refused.Error, want)
		}
	}
	after, err := doltserver.IsRunning(shared)
	if err != nil || after == nil || !after.Running || after.PID != restarted.PID || after.Port != sqlPort {
		t.Fatalf("shared server after refused restart = %+v (err %v), want PID %d still serving port %d", after, err, restarted.PID, sqlPort)
	}
	if greeted, probeErr := doltserver.ProbeSQLServer("tcp", fmt.Sprintf("127.0.0.1:%d", sqlPort), 2*time.Second); probeErr != nil || !greeted {
		t.Fatalf("SQL port %d stopped serving after a refused restart (greeted=%v, err=%v)", sqlPort, greeted, probeErr)
	}
}

func reserveFreeTCPPorts(t *testing.T, count int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, count)
	ports := make([]int, 0, count)
	for range count {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, ln)
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
	}
	for _, ln := range listeners {
		_ = ln.Close()
	}
	return ports
}

func waitForPortState(t *testing.T, port int, open bool, timeout time.Duration) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
		}
		if (err == nil) == open {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("port %d open=%v not reached within %v", port, open, timeout)
}
