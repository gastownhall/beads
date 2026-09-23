package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestProxiedDoltStartRefusalContract pins the parts downstream consumers
// branch on. The code is frozen; the message is informative and may be
// reworded, but it has to keep naming the mode and pointing somewhere useful,
// because "start refused" with no onward path is what sends an operator
// looking for a manual workaround.
func TestProxiedDoltStartRefusalContract(t *testing.T) {
	err := proxiedDoltStartRefusal()
	if err.Code != "proxy.dolt_start.conflict" {
		t.Errorf("code = %q, want %q", err.Code, "proxy.dolt_start.conflict")
	}
	if err.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", err.ExitCode)
	}
	if err.Mutates {
		t.Error("mutates = true; a refused start changes nothing")
	}
	for _, want := range []string{"proxied-server mode", "bd dolt status", "bd dolt stop"} {
		if !strings.Contains(err.Message, want) {
			t.Errorf("message does not mention %q: %q", want, err.Message)
		}
	}
}

func TestExternalDoltEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  configfile.ExternalDoltConfig
		want string
	}{
		{"tcp", configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: 3310}, "127.0.0.1:3310"},
		{"ipv6", configfile.ExternalDoltConfig{Host: "::1", Port: 3310}, "[::1]:3310"},
		{"socket wins", configfile.ExternalDoltConfig{Socket: "/tmp/dolt.sock"}, "unix:/tmp/dolt.sock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := externalDoltEndpoint(tc.cfg); got != tc.want {
				t.Errorf("externalDoltEndpoint = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCollectProxiedDoltStatusQuiescentRoot covers the case an operator hits
// most: nothing is running yet. The answer has to be "not running" WITHOUT
// starting anything — a status command that spawns the topology it is
// reporting on would defeat the idle reaper it is reporting about.
func TestCollectProxiedDoltStatusQuiescentRoot(t *testing.T) {
	beadsDir := t.TempDir()
	root := filepath.Join(beadsDir, "dolt")
	t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", root)

	status, err := collectProxiedDoltStatus(beadsDir)
	if err != nil {
		t.Fatalf("collectProxiedDoltStatus: %v", err)
	}
	if status.Mode != "proxied-server" {
		t.Errorf("mode = %q, want %q", status.Mode, "proxied-server")
	}
	if status.Root != root {
		t.Errorf("root = %q, want %q", status.Root, root)
	}
	if status.Running || status.BackendRunning {
		t.Errorf("reported something running for an empty root: %+v", status)
	}
	if !status.BackendManaged {
		t.Error("backend_managed = false with no external sidecar; managed-local is the default topology")
	}
	if entries, _ := filepath.Glob(filepath.Join(root, "*")); len(entries) != 0 {
		t.Errorf("reading status created %v under the proxied root", entries)
	}
}

func TestCollectProxiedDoltStatusExternalTopology(t *testing.T) {
	beadsDir := t.TempDir()
	root := filepath.Join(beadsDir, "dolt")
	t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", root)
	if err := configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{
		IdleTimeout: 45 * time.Second,
		External:    &configfile.ExternalDoltConfig{Host: "10.0.0.5", Port: 3306},
	}); err != nil {
		t.Fatalf("SaveProxiedServerClientInfo: %v", err)
	}

	status, err := collectProxiedDoltStatus(beadsDir)
	if err != nil {
		t.Fatalf("collectProxiedDoltStatus: %v", err)
	}
	if status.BackendManaged {
		t.Error("backend_managed = true for an external topology; bd spawns no dolt there")
	}
	if status.BackendEndpoint != "10.0.0.5:3306" {
		t.Errorf("backend_endpoint = %q, want %q", status.BackendEndpoint, "10.0.0.5:3306")
	}
	if status.IdleTimeout != "45s" {
		t.Errorf("idle_timeout = %q, want %q", status.IdleTimeout, "45s")
	}
}

func TestRenderProxiedDoltStatus(t *testing.T) {
	running := proxiedDoltStatus{
		Mode: "proxied-server", Root: "/w/.beads/dolt", Running: true,
		ProxyPID: 111, ProxyPort: 40001,
		BackendManaged: true, BackendRunning: true, BackendPID: 222, BackendPort: 40002,
		IdleTimeout: "30s",
	}

	t.Run("text reports both processes", func(t *testing.T) {
		defer restoreJSONOutput(t)()
		jsonOutput = false
		out := captureStdout(t, func() error { renderProxiedDoltStatus(running); return nil })
		for _, want := range []string{
			"Dolt server: running (proxied-server)",
			"Proxy PID:  111",
			"Proxy port: 40001",
			"Root:       /w/.beads/dolt",
			"Backend:    running (dolt PID 222, port 40002)",
			"Idle timeout: 30s",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
		}
	})

	t.Run("text says how to start it when down", func(t *testing.T) {
		defer restoreJSONOutput(t)()
		jsonOutput = false
		down := running
		down.Running, down.ProxyPID, down.ProxyPort = false, 0, 0
		down.BackendRunning, down.BackendPID, down.BackendPort = false, 0, 0
		out := captureStdout(t, func() error { renderProxiedDoltStatus(down); return nil })
		for _, want := range []string{
			"Dolt server: not running (proxied-server)",
			"Backend:    not running",
			"The proxy starts on demand",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
		}
	})

	t.Run("text marks an external backend as somebody else's", func(t *testing.T) {
		defer restoreJSONOutput(t)()
		jsonOutput = false
		external := running
		external.BackendManaged, external.BackendRunning = false, false
		external.BackendEndpoint = "10.0.0.5:3306"
		out := captureStdout(t, func() error { renderProxiedDoltStatus(external); return nil })
		if !strings.Contains(out, "Backend:    external at 10.0.0.5:3306 (not managed by bd)") {
			t.Errorf("external backend not described:\n%s", out)
		}
		if strings.Contains(out, "Backend:    not running") {
			t.Errorf("external backend reported as down; bd has no record to say that from:\n%s", out)
		}
	})

	t.Run("json carries the process records", func(t *testing.T) {
		defer restoreJSONOutput(t)()
		jsonOutput = true
		out := captureStdout(t, func() error { renderProxiedDoltStatus(running); return nil })
		var got proxiedDoltStatus
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
			t.Fatalf("decode %q: %v", out, err)
		}
		if got != running {
			t.Errorf("round-trip mismatch:\ngot  %+v\nwant %+v", got, running)
		}
	})
}

func restoreJSONOutput(t *testing.T) func() {
	t.Helper()
	orig := jsonOutput
	return func() { jsonOutput = orig }
}
