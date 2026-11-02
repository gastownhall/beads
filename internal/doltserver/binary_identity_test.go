//go:build !windows

package doltserver

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// stubProcessListLaunchedHere makes the process list report exactly the
// servers this test process launched, so stop treats the fake dolt as a
// verified dolt sql-server.
func stubProcessListLaunchedHere(t *testing.T) {
	t.Helper()
	stubProcessList(t, func() ([]int, error) {
		var pids []int
		launchedServers.Range(func(k, _ any) bool {
			pids = append(pids, k.(int))
			return true
		})
		return pids, nil
	})
}

// upgradeFakeDolt replaces the dolt on PATH the way a package manager does:
// the file is rewritten (new size and mtime) and reports version.
func upgradeFakeDolt(t *testing.T, version string) string {
	t.Helper()
	doltBin, err := lookPathDolt()
	if err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\n# dolt " + version + "\nexec " + shellQuote(os.Args[0]) + " \"$@\"\n"
	if err := os.WriteFile(doltBin, []byte(shim), 0o700); err != nil { //nolint:gosec // G306: the shim must be executable
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(doltBin, later, later); err != nil {
		t.Fatal(err)
	}
	t.Setenv(testFakeDoltVersionEnv, version)
	return doltBin
}

func killAtCleanup(t *testing.T, pid int) {
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	})
}

func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

// TestLaunchBinaryReplacedRestartsServer: Start records the dolt it launched;
// once that executable is replaced by a supported, different release, the
// server is stale, and ReplaceStaleServer stops it and starts the new release
// on the same port, after which the server is current.
func TestLaunchBinaryReplacedRestartsServer(t *testing.T) {
	beadsDir, launches := fakeStartWorkspace(t)
	stubProcessListLaunchedHere(t)

	state, err := startForTest(t, beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	rec, ok := readBinaryRecord(beadsDir)
	if !ok || rec.PID != state.PID || rec.Version != "2.6.0" || rec.Provenance != binaryProvenanceLaunched {
		t.Fatalf("launch record = %+v (ok=%v), want PID %d, version 2.6.0, provenance launched", rec, ok, state.PID)
	}
	if got := CheckServerBinary(beadsDir, state.Port); got.Verdict != BinaryCurrent {
		t.Fatalf("before upgrade: verdict = %v (%v), want current", got.Verdict, got.Reason)
	}

	doltBin := upgradeFakeDolt(t, "2.7.0")
	check := CheckServerBinary(beadsDir, state.Port)
	if check.Verdict != BinaryStale || check.RunningVersion != "2.6.0" || check.CandidateVersion != "2.7.0" || check.PID != state.PID {
		t.Fatalf("after upgrade: check = %+v, want stale 2.6.0 → 2.7.0 for PID %d", check, state.PID)
	}

	res, err := ReplaceStaleServer(beadsDir, state.Port)
	if res.State != nil && res.State.PID > 0 {
		killAtCleanup(t, res.State.PID)
	}
	if err != nil {
		t.Fatalf("ReplaceStaleServer: %v", err)
	}
	if !res.Replaced || res.State == nil {
		t.Fatalf("ReplaceStaleServer = %+v, want a replacement", res)
	}
	if res.State.Port != state.Port {
		t.Errorf("replacement port = %d, want the same port %d", res.State.Port, state.Port)
	}
	if res.State.PID == state.PID {
		t.Errorf("replacement PID = %d, want a new process", res.State.PID)
	}
	if pidAlive(state.PID) {
		t.Errorf("old server PID %d still running after replacement", state.PID)
	}
	if got := launchedPorts(t, launches); len(got) != 2 || got[1] != strconv.Itoa(state.Port) {
		t.Errorf("launches = %v, want two launches, the second on port %d", got, state.Port)
	}
	rec, ok = readBinaryRecord(beadsDir)
	if !ok || rec.PID != res.State.PID || rec.Version != "2.7.0" || rec.Path != mustAbs(t, doltBin) {
		t.Errorf("record after replacement = %+v (ok=%v), want PID %d, version 2.7.0, path %s", rec, ok, res.State.PID, doltBin)
	}
	if got := CheckServerBinary(beadsDir, state.Port); got.Verdict != BinaryCurrent {
		t.Errorf("after replacement: verdict = %v (%v), want current", got.Verdict, got.Reason)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}
