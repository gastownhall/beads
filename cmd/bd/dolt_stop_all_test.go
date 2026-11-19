package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

// stubDoltStopAll wires the --all stop to a canned process table and a
// recording stop path, restoring both at cleanup (the same seams
// dolt_status_all's tests use).
func stubDoltStopAll(t *testing.T, servers []doltserver.DiscoveredServer, stop func(serverDir string, force bool) error) {
	t.Helper()
	origDiscover, origStop := discoverServers, stopDoltServer
	t.Cleanup(func() { discoverServers, stopDoltServer = origDiscover, origStop })
	discoverServers = func() ([]doltserver.DiscoveredServer, error) { return servers, nil }
	stopDoltServer = stop
}

// TestDoltStopAll_StopsBdServersOnlyAndReportsEach pins GH#4282's stop
// half: every bd-managed server on the machine is stopped through the stop
// path — and only those — while each foreign server is left running and
// reported, whatever its workspace.
func TestDoltStopAll_StopsBdServersOnlyAndReportsEach(t *testing.T) {
	servers := []doltserver.DiscoveredServer{
		{PID: 41001, ServerDir: "/w/a/.beads", BDManaged: true},
		{PID: 41009},                                        // foreign, unattributed
		{PID: 41003, ServerDir: "/w/c/.beads"},              // attributed, but the pid file names another process
		{PID: 41002, ServerDir: "/w/b/.beads", BDManaged: true},
	}
	var calls []doltserver.DiscoveredServer
	forcePassed := map[string]bool{}
	out := captureStdout(t, func() error {
		stubDoltStopAll(t, servers, func(serverDir string, force bool) error {
			calls = append(calls, doltserver.DiscoveredServer{ServerDir: serverDir})
			forcePassed[serverDir] = force
			return nil
		})
		return runDoltStopAll(false)
	})
	if len(calls) != 2 || calls[0].ServerDir != "/w/a/.beads" || calls[1].ServerDir != "/w/b/.beads" {
		t.Errorf("stop --all stopped %v, want bd-managed /w/a/.beads and /w/b/.beads only, in discovery order", calls)
	}
	if forcePassed["/w/a/.beads"] {
		t.Errorf("stop --all passed --force to the stop path without --force")
	}
	for _, want := range []string{
		"Stopped dolt server for /w/a/.beads (PID 41001)",
		"Stopped dolt server for /w/b/.beads (PID 41002)",
		"Left alone (not managed by bd): PID 41009",
		"Left alone (not managed by bd): PID 41003, workspace /w/c/.beads",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stop --all report is missing %q:\n%s", want, out)
		}
	}
}

// TestDoltStopAll_ReportsFailuresAndExitsNonZero pins that a stop that
// fails is reported and fails the command, after the other stops were
// still made and reported.
func TestDoltStopAll_ReportsFailuresAndExitsNonZero(t *testing.T) {
	servers := []doltserver.DiscoveredServer{
		{PID: 41001, ServerDir: "/w/a/.beads", BDManaged: true},
		{PID: 41002, ServerDir: "/w/b/.beads", BDManaged: true},
	}
	stubDoltStopAll(t, servers, func(serverDir string, force bool) error {
		if serverDir == "/w/b/.beads" {
			return errors.New("lifecycle lock: permission denied")
		}
		return nil
	})
	out, err := captureStdoutWithError(t, func() error { return runDoltStopAll(false) })
	if err == nil {
		t.Errorf("stop --all exited 0 though one stop failed:\n%s", out)
	}
	for _, want := range []string{
		"Stopped dolt server for /w/a/.beads (PID 41001)",
		"Could not stop dolt server for /w/b/.beads (PID 41002): lifecycle lock: permission denied",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stop --all report is missing %q:\n%s", want, out)
		}
	}
}

// TestDoltStopAll_ServerGoneBeforeTheStopIsStillSuccess pins the race with
// a server that stops between discovery and the stop — including by its own
// idle watch: the stop path's idempotent cleanup still runs, the server is
// reported as already stopped, and the command succeeds.
func TestDoltStopAll_ServerGoneBeforeTheStopIsStillSuccess(t *testing.T) {
	servers := []doltserver.DiscoveredServer{
		{PID: 41001, ServerDir: "/w/a/.beads", BDManaged: true},
	}
	stubDoltStopAll(t, servers, func(serverDir string, force bool) error {
		return doltserver.ErrServerNotRunning
	})
	out := captureStdout(t, func() error { return runDoltStopAll(false) })
	if !strings.Contains(out, "Already stopped: /w/a/.beads (PID 41001; state files cleaned up)") {
		t.Errorf("stop --all report of a vanished server:\n%s", out)
	}
}

// TestDoltStopAll_NoServers pins the quiet machine.
func TestDoltStopAll_NoServers(t *testing.T) {
	stopped := false
	stubDoltStopAll(t, nil, func(serverDir string, force bool) error {
		stopped = true
		return nil
	})
	out := captureStdout(t, func() error { return runDoltStopAll(false) })
	if stopped {
		t.Errorf("stop --all stopped a server on an empty machine")
	}
	if !strings.Contains(out, "No dolt sql-servers running on this machine") {
		t.Errorf("stop --all on an empty machine = %q", out)
	}
}

// TestDoltStopAll_JSON pins the --json shape for agents: every discovered
// server has an entry, with what bd did to it and why.
func TestDoltStopAll_JSON(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	servers := []doltserver.DiscoveredServer{
		{PID: 41001, ServerDir: "/w/a/.beads", BDManaged: true},
		{PID: 41002, ServerDir: "/w/b/.beads", BDManaged: true},
		{PID: 41009},
	}
	stubDoltStopAll(t, servers, func(serverDir string, force bool) error {
		if serverDir == "/w/b/.beads" {
			return doltserver.ErrServerNotRunning
		}
		return nil
	})
	out := captureStdout(t, func() error { return runDoltStopAll(false) })
	var result doltStopAllResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse stop --all json: %v\n%s", err, out)
	}
	if len(result.Servers) != 3 {
		t.Fatalf("stop --all json reports %d servers, want 3:\n%s", len(result.Servers), out)
	}
	if s := result.Servers[0]; !s.Stopped || s.Workspace != "/w/a/.beads" || s.PID != 41001 {
		t.Errorf("stopped server entry = %+v", s)
	}
	if s := result.Servers[1]; s.Stopped || !s.AlreadyStopped || s.Workspace != "/w/b/.beads" {
		t.Errorf("vanished server entry = %+v", s)
	}
	if s := result.Servers[2]; s.Stopped || s.LeftAlone != "not managed by bd" || s.PID != 41009 {
		t.Errorf("foreign server entry = %+v", s)
	}
}

// TestDoltStopAll_GoesThroughTheBdDoltStopPath pins that stop --all stops
// through doltserver.StopWithForce — the same function the workspace-local
// `bd dolt stop` calls — and exercises that real path against a server
// directory with no live server: the stop path's idempotent cleanup and
// not-running classification handle it.
func TestDoltStopAll_GoesThroughTheBdDoltStopPath(t *testing.T) {
	if got := reflect.ValueOf(stopDoltServer).Pointer(); got != reflect.ValueOf(doltserver.StopWithForce).Pointer() {
		t.Errorf("stop --all must stop through doltserver.StopWithForce, the bd dolt stop path")
	}
	// A discovered bd-managed serverDir with no state: the real stop path
	// classifies it as not running and cleans up, exactly as it would for
	// a server that stopped between discovery and the stop.
	dir := t.TempDir()
	origDiscover := discoverServers
	t.Cleanup(func() { discoverServers = origDiscover })
	discoverServers = func() ([]doltserver.DiscoveredServer, error) {
		return []doltserver.DiscoveredServer{{PID: 41001, ServerDir: dir, BDManaged: true}}, nil
	}
	out := captureStdout(t, func() error { return runDoltStopAll(false) })
	if !strings.Contains(out, "Already stopped: "+dir) {
		t.Errorf("the real stop path did not classify the server directory:\n%s", out)
	}
}

// TestDoltStopAll_FlagRunsTheStopFromAnyDirectory pins the flag wiring:
// `bd dolt stop --all` routes to the machine-wide stop before the active
// workspace is even looked up.
func TestDoltStopAll_FlagRunsTheStopFromAnyDirectory(t *testing.T) {
	if err := doltStopCmd.Flags().Set("all", "true"); err != nil {
		t.Fatalf("bd dolt stop has no --all flag: %v", err)
	}
	t.Cleanup(func() { _ = doltStopCmd.Flags().Set("all", "false") })

	called := false
	stubDoltStopAll(t, []doltserver.DiscoveredServer{
		{PID: 41007, ServerDir: "/w/a/.beads", BDManaged: true},
	}, func(serverDir string, force bool) error {
		called = true
		return nil
	})
	out := captureStdout(t, func() error { return doltStopCmd.RunE(doltStopCmd, nil) })
	if !called {
		t.Errorf("bd dolt stop --all did not stop the discovered server:\n%s", out)
	}
	if !strings.Contains(out, "Stopped dolt server for /w/a/.beads (PID 41007)") {
		t.Errorf("bd dolt stop --all report:\n%s", out)
	}
}
