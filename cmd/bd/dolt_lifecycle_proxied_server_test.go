//go:build cgo

package main

// The external-tcp half of the `bd dolt status` / `bd dolt start` coverage.
// It lives in the TestProxiedServer* lane, which runs on every PR, so the
// corruption guard is enforced there and not only in the path-filtered
// managed-local lane.
//
// The distinction this topology exercises: bd runs a proxy but spawns no dolt,
// so "the backend is not running" is a claim bd has no standing to make.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

func TestProxiedServerDoltLifecycle(t *testing.T) {
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "extdolt")
	t.Cleanup(func() {
		serverDir := doltserver.ResolveServerDir(p.beadsDir)
		if _, err := os.Stat(filepath.Join(serverDir, doltserver.PIDFileName)); err != nil {
			return
		}
		t.Logf("stray classic dolt-server.pid under %s; stopping it", serverDir)
		if err := doltserver.Stop(serverDir); err != nil {
			t.Logf("doltserver.Stop(%s): %v", serverDir, err)
		}
	})
	bdProxiedCreate(t, bd, p.dir, "external lifecycle sentinel")

	proxyPid := readProxyPidFileOrFail(t, p)

	t.Run("status reports the proxy and disclaims the backend", func(t *testing.T) {
		stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, []string{"BEADS_JSON=1"}, "--json", "dolt", "status")
		if err != nil {
			t.Fatalf("bd dolt status failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		var got struct {
			Mode            string `json:"mode"`
			Running         bool   `json:"running"`
			ProxyPID        int    `json:"proxy_pid"`
			BackendManaged  bool   `json:"backend_managed"`
			BackendRunning  bool   `json:"backend_running"`
			BackendEndpoint string `json:"backend_endpoint"`
		}
		if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); jsonErr != nil {
			t.Fatalf("decode dolt status JSON: %v\nstdout:\n%s", jsonErr, stdout)
		}
		if got.Mode != "proxied-server" || !got.Running {
			t.Errorf("status=%+v, want mode=proxied-server running=true", got)
		}
		if got.ProxyPID != proxyPid {
			t.Errorf("proxy_pid=%d, want %d", got.ProxyPID, proxyPid)
		}
		if got.BackendManaged {
			t.Error("backend_managed=true on an external topology; bd spawns no dolt there")
		}
		if got.BackendRunning {
			t.Error("backend_running=true on an external topology; bd has no record to claim that from")
		}
		if !strings.HasPrefix(got.BackendEndpoint, "127.0.0.1:") {
			t.Errorf("backend_endpoint=%q, want the configured external endpoint", got.BackendEndpoint)
		}
	})

	t.Run("start is refused", func(t *testing.T) {
		stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, []string{"BEADS_JSON=1"}, "--json", "dolt", "start")
		if err == nil {
			t.Errorf("bd dolt start succeeded on an external proxied workspace:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		var got struct {
			Code    string `json:"code"`
			Mutates bool   `json:"mutates"`
		}
		if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); jsonErr != nil {
			t.Fatalf("refusal is not typed JSON (%v)\nstdout:\n%s\nstderr:\n%s", jsonErr, stdout, stderr)
		}
		if got.Code != proxyDoltStartConflictCode || got.Mutates {
			t.Errorf("refusal=%+v, want code=%q mutates=false", got, proxyDoltStartConflictCode)
		}
		for _, name := range []string{doltserver.PIDFileName, doltserver.PortFileName} {
			if _, statErr := os.Stat(filepath.Join(p.beadsDir, name)); statErr == nil {
				t.Errorf("%s exists after a refused start", name)
			}
		}
	})

	if out, err := bdProxiedRun(t, bd, p.dir, "create", "--json", "post-refusal write"); err != nil {
		t.Fatalf("workspace unusable after the lifecycle probes: %v\n%s", err, out)
	}
}

func readProxyPidFileOrFail(t *testing.T, p proxiedProject) int {
	t.Helper()
	running, pid := proxy.IsRunning(p.proxyRoot)
	if !running {
		t.Fatalf("expected a live proxy under %s after a create", p.proxyRoot)
	}
	return pid
}
