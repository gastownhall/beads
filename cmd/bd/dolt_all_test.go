package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
)

// threeDoltServers is two bd-managed servers and one another tool runs.
func threeDoltServers() []doltserver.DiscoveredServer {
	started := time.Now().Add(-90 * time.Minute)
	return []doltserver.DiscoveredServer{
		{PID: 3003, Command: "dolt sql-server --config /home/u/city/.gc/dolt/config.yaml"},
		{PID: 2002, Managed: true, ServerDir: "/home/u/proj-b/.beads", Port: 41002, StartedAt: started,
			IdleStop: doltserver.IdleStopState{Reason: doltserver.IdleOffExplicitStart}},
		{PID: 1001, Managed: true, ServerDir: "/home/u/proj-a/.beads", Port: 41001, StartedAt: started,
			IdleStop: doltserver.IdleStopState{On: true, Window: 30 * time.Minute}},
	}
}

// runDoltStatusAllForTest runs `bd dolt status --all` from a directory that
// is no workspace, with discovery answering servers.
func runDoltStatusAllForTest(t *testing.T, servers []doltserver.DiscoveredServer, asJSON bool) string {
	t.Helper()
	isolateBeadsDirForTest(t)
	t.Chdir(t.TempDir())
	origDiscover, origJSON := discoverDoltServers, jsonOutput
	discoverDoltServers = func() ([]doltserver.DiscoveredServer, error) { return servers, nil }
	jsonOutput = asJSON
	t.Cleanup(func() { discoverDoltServers, jsonOutput = origDiscover, origJSON })
	if err := doltStatusCmd.Flags().Set("all", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = doltStatusCmd.Flags().Set("all", "false") })
	return captureStdout(t, func() error { return doltStatusCmd.RunE(doltStatusCmd, nil) })
}

func TestDoltStatusAll_ListsManagedAndOtherServers(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		out := runDoltStatusAllForTest(t, threeDoltServers(), false)
		for _, want := range []string{
			"Dolt servers managed by bd: 2",
			"● /home/u/proj-a\n      PID 1001  Port 41001  Uptime 1h30m0s  Idle stop: after 30m",
			"● /home/u/proj-b\n      PID 2002  Port 41002  Uptime 1h30m0s  Idle stop: off: started with bd dolt start",
			"Other Dolt servers (not managed by bd): 1",
			"○ PID 3003  dolt sql-server --config /home/u/city/.gc/dolt/config.yaml",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		out := runDoltStatusAllForTest(t, threeDoltServers(), true)
		var got struct {
			Servers []doltServerListEntry `json:"servers"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("output is not JSON: %v\n%s", err, out)
		}
		if len(got.Servers) != 3 {
			t.Fatalf("servers = %+v, want 3", got.Servers)
		}
		a, b, other := got.Servers[0], got.Servers[1], got.Servers[2]
		if !a.Managed || a.Workspace != "/home/u/proj-a" || a.PID != 1001 || a.Port != 41001 ||
			a.IdleStop != "after 30m" || a.StartedAt == nil || a.UptimeSeconds < 5400 {
			t.Errorf("first server = %+v, want proj-a (PID 1001, port 41001, up 90m, idle stop after 30m)", a)
		}
		if !b.Managed || b.Workspace != "/home/u/proj-b" || b.IdleStop != "off: started with bd dolt start" {
			t.Errorf("second server = %+v, want proj-b with idle stop off", b)
		}
		if other.Managed || other.PID != 3003 || other.Workspace != "" || !strings.Contains(other.Command, "/city/") {
			t.Errorf("third server = %+v, want PID 3003 not managed by bd", other)
		}
	})
}

// TestRenderLocalDoltStatus_IdleStopLine: single-workspace status names the
// running server's idle-stop setting.
func TestRenderLocalDoltStatus_IdleStopLine(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	orig := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = orig })
	state := &doltserver.State{Running: true, PID: 12345, Port: 28231, DataDir: "/tmp/data"}
	out := captureStdout(t, func() error {
		renderLocalDoltStatus(state, t.TempDir())
		return nil
	})
	if want := "Idle stop: off: no watcher (started by an older bd)"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}

// runDoltStopAllForTest runs `bd dolt stop --all` from a directory that is
// no workspace, with discovery answering servers and stops going to stop.
func runDoltStopAllForTest(t *testing.T, servers []doltserver.DiscoveredServer, stop func(serverDir string, force bool) error) (string, error) {
	t.Helper()
	isolateBeadsDirForTest(t)
	t.Chdir(t.TempDir())
	origDiscover, origStop, origJSON := discoverDoltServers, stopDoltServerDir, jsonOutput
	discoverDoltServers = func() ([]doltserver.DiscoveredServer, error) { return servers, nil }
	stopDoltServerDir = stop
	jsonOutput = false
	t.Cleanup(func() { discoverDoltServers, stopDoltServerDir, jsonOutput = origDiscover, origStop, origJSON })
	if err := doltStopCmd.Flags().Set("all", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = doltStopCmd.Flags().Set("all", "false") })
	var runErr error
	out := captureStdout(t, func() error {
		runErr = doltStopCmd.RunE(doltStopCmd, nil)
		return nil
	})
	return out, runErr
}

func TestDoltStopAll_StopsOnlyBdManagedServers(t *testing.T) {
	t.Run("all stop", func(t *testing.T) {
		var stopped []string
		out, err := runDoltStopAllForTest(t, threeDoltServers(), func(serverDir string, force bool) error {
			stopped = append(stopped, serverDir)
			return nil
		})
		if err != nil {
			t.Fatalf("stop --all = %v, want success\n%s", err, out)
		}
		if got := strings.Join(stopped, ","); got != "/home/u/proj-a/.beads,/home/u/proj-b/.beads" {
			t.Errorf("stopped server dirs %q, want exactly the two bd-managed ones", got)
		}
		for _, want := range []string{
			"✓ Stopped Dolt server for /home/u/proj-a (PID 1001)",
			"✓ Stopped Dolt server for /home/u/proj-b (PID 2002)",
			"Left running 1 Dolt server not managed by bd (PID 3003)",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("a failed stop is reported and fails the command", func(t *testing.T) {
		var stopped []string
		out, err := runDoltStopAllForTest(t, threeDoltServers(), func(serverDir string, force bool) error {
			stopped = append(stopped, serverDir)
			if strings.Contains(serverDir, "proj-a") {
				return errors.New("lifecycle lock busy")
			}
			return nil
		})
		if err == nil {
			t.Fatalf("stop --all succeeded with a failed stop, want a non-zero exit\n%s", out)
		}
		if len(stopped) != 2 {
			t.Errorf("stopped %v, want both bd-managed servers attempted", stopped)
		}
		for _, want := range []string{
			"✗ Could not stop Dolt server for /home/u/proj-a (PID 1001): lifecycle lock busy",
			"✓ Stopped Dolt server for /home/u/proj-b (PID 2002)",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})
}
