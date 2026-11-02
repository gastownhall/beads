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

// installOtherDolt puts a second fake dolt, reporting version, first on
// PATH, as when the opener's PATH differs from the one the server was
// started with.
func installOtherDolt(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	doltBin := filepath.Join(dir, "dolt")
	shim := "#!/bin/sh\n# other dolt " + version + "\nexec " + shellQuote(os.Args[0]) + " \"$@\"\n"
	if err := os.WriteFile(doltBin, []byte(shim), 0o700); err != nil { //nolint:gosec // G306: the shim must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(testFakeDoltVersionEnv, version)
	return doltBin
}

// TestLaunchBinaryUnchangedKeepsServer: while the executable a server was
// launched from is unchanged, the server is current and ReplaceStaleServer
// leaves it running, whatever dolt the caller's PATH finds first.
func TestLaunchBinaryUnchangedKeepsServer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T)
	}{
		{name: "same PATH", setup: func(*testing.T) {}},
		{name: "different PATH", setup: func(t *testing.T) { installOtherDolt(t, "2.7.0") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, launches := fakeStartWorkspace(t)
			stubProcessListLaunchedHere(t)
			state, err := startForTest(t, beadsDir)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			before, _ := os.ReadFile(binaryRecordPath(beadsDir))
			tc.setup(t)

			check := CheckServerBinary(beadsDir, state.Port)
			if check.Verdict != BinaryCurrent || check.Refresh {
				t.Fatalf("check = %+v, want current with nothing to refresh", check)
			}
			res, err := ReplaceStaleServer(beadsDir, state.Port)
			if err != nil || res.Replaced {
				t.Fatalf("ReplaceStaleServer = %+v, %v; want the server kept", res, err)
			}
			if !pidAlive(state.PID) || len(launchedPorts(t, launches)) != 1 {
				t.Errorf("server PID %d alive=%v, launches=%v; want it kept and launched once",
					state.PID, pidAlive(state.PID), launchedPorts(t, launches))
			}
			if after, _ := os.ReadFile(binaryRecordPath(beadsDir)); string(after) != string(before) {
				t.Errorf("record rewritten:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// TestLaunchBinarySameVersionReinstallKeepsServer: a reinstall of the same
// release changes the executable's identity but not its version, so the
// server keeps running; the reinstalled executable is recorded as observed,
// after which the server is current without a version probe.
func TestLaunchBinarySameVersionReinstallKeepsServer(t *testing.T) {
	beadsDir, launches := fakeStartWorkspace(t)
	stubProcessListLaunchedHere(t)
	state, err := startForTest(t, beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	doltBin := upgradeFakeDolt(t, "2.6.0")

	check := CheckServerBinary(beadsDir, state.Port)
	if check.Verdict != BinaryCurrent || !check.Refresh {
		t.Fatalf("check = %+v, want current with a record to refresh", check)
	}
	res, err := ReplaceStaleServer(beadsDir, state.Port)
	if err != nil || res.Replaced {
		t.Fatalf("ReplaceStaleServer = %+v, %v; want the server kept", res, err)
	}
	if !pidAlive(state.PID) || len(launchedPorts(t, launches)) != 1 {
		t.Errorf("server PID %d alive=%v, launches=%v; want it kept and launched once",
			state.PID, pidAlive(state.PID), launchedPorts(t, launches))
	}
	want, err := identifyBinary(doltBin)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := readBinaryRecord(beadsDir)
	if !ok || rec.PID != state.PID || !rec.same(want) || rec.Version != "2.6.0" || rec.Provenance != binaryProvenanceObserved {
		t.Fatalf("record = %+v (ok=%v), want PID %d, the reinstalled executable %+v, version 2.6.0, provenance observed",
			rec, ok, state.PID, want)
	}
	if got := CheckServerBinary(beadsDir, state.Port); got.Verdict != BinaryCurrent || got.Refresh {
		t.Errorf("after refresh: check = %+v, want current with nothing to refresh", got)
	}
}
