//go:build cgo && unix

package main

// Fixture for the direct LOCAL server topology: `bd init --server` with no
// port, where bd spawns and owns a `dolt sql-server` under .beads.
//
// This is the one topology in the matrix with no existing named helper — the
// two tests that use it today build it inline behind the `integration` build
// tag (dolt_autostart_lifecycle_integration_test.go,
// migrate_dolt_mode_frontdoor_integration_test.go). This file factors out the
// two pieces those tests had to get right, and nothing else.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// directLocalServerEnv builds a subprocess environment for a bd-owned local
// dolt sql-server.
//
// Neither bdEnv nor bdProxiedEnv works here, for opposite reasons:
//   - bdEnv sets BEADS_DOLT_AUTO_START=0, which stops bd from ever launching
//     the server this topology is about.
//   - the inherited env carries the cmd/bd TestMain's BEADS_TEST_MODE=1, which
//     disables auto-start AND rewrites an unresolved port to 1.
//
// So: strip every BEADS_*/BD_* var and redirect HOME, exactly as the existing
// front-door integration tests do, and add nothing back.
func directLocalServerEnv(home string) []string {
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if strings.HasPrefix(key, "BEADS_") || strings.HasPrefix(key, "BD_") ||
			key == "HOME" || key == "USERPROFILE" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+home, "USERPROFILE="+home)
}

// stopDirectLocalServer tears the workspace's server down through `bd dolt
// stop`, then verifies the PID bd recorded for it is actually gone.
//
// Safety contract: the ONLY process this may stop is the one named in this
// workspace's own .beads/dolt-server.pid. It never scans, never matches by
// process name, and never signals a PID it did not read from that file. If
// the server survives, the test reports it loudly rather than escalating —
// a stray kill on a host running unrelated dolt servers is far worse than a
// noisy test.
func stopDirectLocalServer(t *testing.T, bd, dir, beadsDir string, env []string) {
	t.Helper()

	pid := readRecordedServerPID(t, beadsDir)

	cmd := exec.Command(bd, "dolt", "stop")
	cmd.Dir = dir
	cmd.Env = env
	if stdout, stderr, err := runCommandBuffers(t, cmd); err != nil {
		t.Logf("bd dolt stop (direct local): %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	if pid <= 0 {
		return
	}
	for i := 0; i < 50; i++ {
		if !processStillAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("direct-local dolt sql-server pid %d survived `bd dolt stop`; "+
		"stop it by that PID before re-running. This test deliberately does not "+
		"kill by process name: this host runs unrelated dolt servers.", pid)
}

func readRecordedServerPID(t *testing.T, beadsDir string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.pid"))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	return pid
}

func processStillAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
