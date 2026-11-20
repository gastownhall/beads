//go:build cgo

package doctor

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/testutil"
)

func TestRunDoltPerformanceDiagnostics_RequiresServer(t *testing.T) {
	// Server-only mode: diagnostics require a running dolt sql-server.
	// Without a server, RunDoltPerformanceDiagnostics should return an error.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write config pointing to a nonexistent database
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerHost = "127.0.0.1"
	cfg.DoltServerPort = doctorTestServerPort()
	cfg.DoltDatabase = "doctest_perf_nonexistent"
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	_, err := RunDoltPerformanceDiagnostics(tmpDir, false)
	if err == nil {
		t.Fatal("expected error when no dolt server is running")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "not running") && !strings.Contains(errStr, "not reachable") && !strings.Contains(errStr, "database not found") {
		t.Errorf("expected server/database error, got: %v", err)
	}
}

func TestRunDoltPerformanceDiagnosticsDeclineNonDoltBackend(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := &configfile.Config{Backend: configfile.BackendSQLite, SQLitePath: "beads.db"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save SQLite config: %v", err)
	}

	_, err := RunDoltPerformanceDiagnostics(tmpDir, false)
	if err == nil {
		t.Fatal("expected Dolt-only diagnostics to decline a non-Dolt workspace")
	}
	message := err.Error()
	if !strings.Contains(message, "Dolt") || !strings.Contains(message, `"sqlite"`) {
		t.Fatalf("diagnostic error should explain the backend mismatch: %v", err)
	}
}

// TestRunDoltPerformanceDiagnosticsRemoteHost pins the endpoint bd doctor
// --perf dials for a server on another machine: the configured host, which the
// store dials, on DialConfig's port, never 127.0.0.1. When nothing answers
// there it gives the remote-host advice, not `bd dolt start`, which refuses a
// remote host.
func TestRunDoltPerformanceDiagnosticsRemoteHost(t *testing.T) {
	tmpDir, port := perfServerWorkspace(t, "db.invalid")

	_, err := RunDoltPerformanceDiagnostics(tmpDir, false)
	if err == nil {
		t.Fatal("performance diagnostics succeeded with no server at db.invalid")
	}
	msg := err.Error()
	for _, want := range []string{net.JoinHostPort("db.invalid", strconv.Itoa(port)), fmt.Sprintf("nc -zv db.invalid %d", port)} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not contain %q:\n%s", want, msg)
		}
	}
	for _, banned := range []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), "bd dolt start", "externally managed"} {
		if strings.Contains(msg, banned) {
			t.Errorf("error contains %q:\n%s", banned, msg)
		}
	}
}

// TestRunDoltPerformanceDiagnosticsCentralConfigHost pins that bd doctor
// --perf dials a host set only in the central server config
// (~/.config/beads/server.json). The commands it times (bd ready, bd list,
// bd show) take that host for their store open (GH#3895), so --perf measures
// the server at db.invalid, not one at 127.0.0.1.
func TestRunDoltPerformanceDiagnosticsCentralConfigHost(t *testing.T) {
	tmpDir, port := perfServerWorkspace(t, "")
	centralPath := filepath.Join(tmpDir, "server.json")
	if err := os.WriteFile(centralPath, []byte(`{"dolt_server_host":"db.invalid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_CENTRAL_CONFIG", centralPath)

	_, err := RunDoltPerformanceDiagnostics(tmpDir, false)
	if err == nil {
		t.Fatal("performance diagnostics succeeded with no server at db.invalid")
	}
	msg := err.Error()
	if want := net.JoinHostPort("db.invalid", strconv.Itoa(port)); !strings.Contains(msg, want) {
		t.Errorf("error does not contain %q:\n%s", want, msg)
	}
	for _, banned := range []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), "bd dolt start"} {
		if strings.Contains(msg, banned) {
			t.Errorf("error contains %q:\n%s", banned, msg)
		}
	}
}

// TestRunDoltPerformanceDiagnosticsIPv6Host pins that bd doctor --perf
// probes and connects to an IPv6 server host configured bare ("::1") or
// bracketed ("[::1]"), the address the store dials, rather than reporting a
// running server as not running. The listener speaks no MySQL, so the
// diagnostics fail, but only after the connection reached it.
func TestRunDoltPerformanceDiagnosticsIPv6Host(t *testing.T) {
	for _, host := range []string{"::1", "[::1]"} {
		t.Run(host, func(t *testing.T) {
			port, accepted := testutil.IPv6LoopbackListener(t)
			tmpDir := perfServerWorkspaceOn(t, host, port)

			metrics, err := RunDoltPerformanceDiagnostics(tmpDir, false)
			if metrics == nil || metrics.ServerStatus != "running" {
				t.Fatalf("server status = %+v, want running; err = %v", metrics, err)
			}
			if err == nil || !strings.Contains(err.Error(), "server diagnostics failed") {
				t.Errorf("err = %v, want the diagnostics' failure against a listener that speaks no MySQL", err)
			}
			for deadline := time.Now().Add(2 * time.Second); accepted() < 2 && time.Now().Before(deadline); {
				time.Sleep(10 * time.Millisecond)
			}
			if n := accepted(); n < 2 {
				t.Errorf("the server at [::1]:%d took %d connections, want the probe's and the diagnostics': %v", port, n, err)
			}
		})
	}
	// With nothing listening, the error names the address it probed, as the
	// advice under it does: [::1]:port, never ::1:port. The advice checks the
	// server there and never names `bd dolt start`: bd's own server listens
	// on 127.0.0.1 only. For the same reason a port file that server left
	// behind names no port at [::1], and the probe stays on the port
	// metadata.json's dolt_server_port declares.
	for _, host := range []string{"::1", "[::1]"} {
		for _, portFile := range []bool{false, true} {
			name := host + " not running"
			if portFile {
				name += ", beside a port file"
			}
			t.Run(name, func(t *testing.T) {
				ln, err := net.Listen("tcp", "[::1]:0")
				if err != nil {
					t.Skipf("IPv6 loopback not available: %v", err)
				}
				other, err := net.Listen("tcp", "[::1]:0")
				if err != nil {
					t.Fatal(err)
				}
				port, stale := ln.Addr().(*net.TCPAddr).Port, other.Addr().(*net.TCPAddr).Port
				_ = ln.Close()
				_ = other.Close()
				tmpDir := perfServerWorkspaceOn(t, host, port)
				if portFile {
					if err := os.WriteFile(filepath.Join(tmpDir, ".beads", "dolt-server.port"), []byte(strconv.Itoa(stale)), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				addr := net.JoinHostPort("::1", strconv.Itoa(port))

				_, err = RunDoltPerformanceDiagnostics(tmpDir, false)
				if err == nil {
					t.Fatalf("performance diagnostics succeeded with nothing listening at %s", addr)
				}
				for _, want := range []string{"not running on " + addr, fmt.Sprintf(`nc -zv ::1 %d  # or curl "%s"`, port, addr)} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error does not contain %q:\n%v", want, err)
					}
				}
				// bd's own server listens on 127.0.0.1 only: bd starts none
				// at [::1], and its port is not the one to check there.
				for _, banned := range []string{"bd dolt start", fmt.Sprintf("nc -zv ::1 %d ", stale)} {
					if strings.Contains(err.Error(), banned) {
						t.Errorf("error contains %q:\n%v", banned, err)
					}
				}
			})
		}
	}
}

// TestRunDoltPerformanceDiagnosticsUnresolvedPort pins bd doctor --perf with
// no port resolved for a server bd does not run: at the IPv6 loopback in
// either spelling, where the port file bd's own 127.0.0.1 server leaves names
// no port, or on another machine. No bd command starts that server, so the
// error gives the store's advice to pin the port, not a check of port 0 or
// `bd dolt start`.
func TestRunDoltPerformanceDiagnosticsUnresolvedPort(t *testing.T) {
	for _, tc := range []struct {
		name     string
		host     string
		portFile bool
	}{
		{"bare IPv6 loopback", "::1", false},
		{"bracketed IPv6 loopback beside a port file", "[::1]", true},
		{"remote host", "db.invalid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := perfServerWorkspaceOn(t, tc.host, 0)
			if tc.portFile {
				if err := os.WriteFile(filepath.Join(tmpDir, ".beads", "dolt-server.port"), []byte("13307"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			_, err := RunDoltPerformanceDiagnostics(tmpDir, false)
			if err == nil {
				t.Fatalf("performance diagnostics succeeded with no port for the server at %s", tc.host)
			}
			want := "bd does not manage the Dolt server at " + tc.host + "; pin its port with BEADS_DOLT_SERVER_PORT or dolt.port"
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error does not contain %q:\n%v", want, err)
			}
			for _, banned := range []string{":0", "nc -zv", "bd dolt start", "13307"} {
				if strings.Contains(err.Error(), banned) {
					t.Errorf("error contains %q:\n%v", banned, err)
				}
			}
		})
	}
}

// TestDatabaseSizeFor pins the database size bd doctor --perf reports: that of
// the workspace's data directory only for a server bd runs. For one at the
// IPv6 loopback, in either spelling, that directory is not the server's data
// (bd's own server listens on 127.0.0.1 only), as for one on another machine.
func TestDatabaseSizeFor(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt", "chunk"), make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]string{
		"127.0.0.1":  "2.00 KB",
		"localhost":  "2.00 KB",
		"::1":        "n/a (server bd does not run)",
		"[::1]":      "n/a (server bd does not run)",
		"db.invalid": "n/a (server on another machine)",
		// One address gets one answer however it is spelled, and a loopback
		// address bd's own server does not listen on is this machine but
		// not bd's server.
		"LOCALHOST":        "2.00 KB",
		"0.0.0.0":          "2.00 KB",
		"::ffff:127.0.0.1": "2.00 KB",
		"0:0:0:0:0:0:0:1":  "n/a (server bd does not run)",
		"127.0.0.2":        "n/a (server bd does not run)",
	} {
		if got := databaseSizeFor(beadsDir, host); got != want {
			t.Errorf("databaseSizeFor(%q) = %q, want %q", host, got, want)
		}
	}
}

// TestBdRunsServerHostOwnership pins doctor's ownership decision (port-0
// advice, health-check skip) to configfile.BdRunsServerAt in every spelling.
// A proxied-server workspace's server is bd's own proxy whatever the host.
func TestBdRunsServerHostOwnership(t *testing.T) {
	server := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer}
	proxied := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeProxiedServer}
	for _, host := range []string{"", "127.0.0.1", "LOCALHOST", "0.0.0.0", "::ffff:127.0.0.1", "::1", "[::1]", "0:0:0:0:0:0:0:1", "127.0.0.2", "db.example.com"} {
		if got, want := bdRunsServer(server, host), configfile.BdRunsServerAt(host); got != want {
			t.Errorf("bdRunsServer(server mode, %q) = %v, want %v", host, got, want)
		}
		if !bdRunsServer(proxied, host) {
			t.Errorf("bdRunsServer(proxied-server mode, %q) = false, want true", host)
		}
	}
}

// perfServerWorkspace writes a server-mode metadata.json naming host, when it
// is not empty, and a free port nothing listens on, with no Dolt server
// variable set, and returns the project directory and the port.
func perfServerWorkspace(t *testing.T, host string) (string, int) {
	t.Helper()
	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	return perfServerWorkspaceOn(t, host, port), port
}

// perfServerWorkspaceOn is perfServerWorkspace naming port, and returns the
// project directory.
func perfServerWorkspaceOn(t *testing.T, host string, port int) string {
	t.Helper()
	for _, name := range []string{"BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SERVER_PORT",
		"BEADS_DOLT_PORT", "BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_SOCKET"} {
		t.Setenv(name, "")
	}
	config.ResetForTesting()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerHost = host
	cfg.DoltServerPort = port
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}
	return tmpDir
}
