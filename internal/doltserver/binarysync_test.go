//go:build !windows

package doltserver

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// GH#6064: the binary witness sidecar and the store-open hook that acts on
// it. These tests drive the real Start/stop/start lifecycle with the fake
// dolt on PATH, so a replacement is a real stop of a real child and a real
// relaunch, not a stubbed one. The hook's one notice and its advisories go
// to stderr; captureStderr (sweep_suite_test.go) is how the tests read them.

// readBinaryRecordForTest returns the parsed binary witness record, failing
// the test when there is none.
func readBinaryRecordForTest(t *testing.T, beadsDir string) *BinaryRecord {
	t.Helper()
	b, err := os.ReadFile(ServerBinaryPath(beadsDir)) //nolint:gosec // G304: test-owned state dir
	if err != nil {
		t.Fatalf("reading the binary witness record: %v", err)
	}
	var rec BinaryRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("parsing the binary witness record %s: %v", ServerBinaryPath(beadsDir), err)
	}
	return &rec
}

// replaceFakeDolt simulates a dolt upgrade: the executable on PATH that the
// tracked server was launched from is replaced (a different file identity)
// and reports the given version. Like a real `brew upgrade`, the old file
// identity is gone; the path still names a dolt.
func replaceFakeDolt(t *testing.T, version string) {
	t.Helper()
	shim, err := exec.LookPath("dolt")
	if err != nil {
		t.Fatalf("finding the fake dolt on PATH: %v", err)
	}
	// Different content (and therefore size and mtime) than installFakeDolt's
	// shim, so the recorded file identity no longer matches.
	replacement := "#!/bin/sh\n# replaced by an upgrade\nexec " + shellQuote(os.Args[0]) + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(replacement), 0o700); err != nil { //nolint:gosec // G306: the shim must stay executable
		t.Fatalf("replacing the fake dolt: %v", err)
	}
	// A distinct mtime even if a same-size rewrite ever lands here.
	newTime := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(shim, newTime, newTime); err != nil {
		t.Fatalf("stamping the replaced fake dolt: %v", err)
	}
	t.Setenv(testFakeDoltVersionEnv, version)
}

// stopTrackedServerAfterTest makes whatever server the state files name at
// cleanup time die with the test, however many replacements happened in
// between (startForTest only registers the PID it saw).
func stopTrackedServerAfterTest(t *testing.T, beadsDir string) {
	t.Helper()
	t.Cleanup(func() { _ = Stop(beadsDir) })
}

// TestSyncServerBinary_ReplacesStaleTrackedServer is AC-1: a tracked server
// whose recorded dolt executable was replaced by a newer one is replaced at
// store-open time — before any application query could run against it — with
// its port preserved and one stderr notice naming both versions. The launch
// must also have published the binary witness record the classification
// reads (provenance launched, tracked PID, file identity, known version).
func TestSyncServerBinary_ReplacesStaleTrackedServer(t *testing.T) {
	beadsDir, launches := fakeStartWorkspace(t)
	stopTrackedServerAfterTest(t, beadsDir)

	state, err := startForTest(t, beadsDir)
	if err != nil {
		t.Fatalf("starting the tracked server: %v", err)
	}

	// The start published the launch evidence.
	rec := readBinaryRecordForTest(t, beadsDir)
	if rec.PID != state.PID {
		t.Errorf("launch record names PID %d, want the tracked PID %d", rec.PID, state.PID)
	}
	if rec.DoltVersion != "2.6.0" || !rec.KnownVersion || rec.Provenance != BinaryProvenanceLaunched {
		t.Errorf("launch record = %+v, want dolt 2.6.0 (known), provenance %q", rec, BinaryProvenanceLaunched)
	}
	if rec.RealPath == "" || rec.Size == 0 || rec.ModTimeNanos == 0 {
		t.Errorf("launch record = %+v, want the executable's resolved path and file identity", rec)
	}

	// Upgrade dolt under the running server.
	replaceFakeDolt(t, "2.7.0")

	var syncErr error
	stderr := captureStderr(t, func() {
		syncErr = SyncServerBinary(BinarySyncRequest{
			BeadsDir:     beadsDir,
			Host:         "127.0.0.1",
			Port:         state.Port,
			AllowRestart: true,
		})
	})
	if syncErr != nil {
		t.Fatalf("SyncServerBinary on a stale tracked server: %v", syncErr)
	}

	// The replacement is exactly one relaunch, on the same port.
	ports := launchedPorts(t, launches)
	if len(ports) != 2 {
		t.Fatalf("the fake dolt launched %d time(s) (%v), want 2: the original start and the replacement", len(ports), ports)
	}
	if ports[0] != ports[1] {
		t.Errorf("the replacement moved the port: %s -> %s, want it preserved", ports[0], ports[1])
	}
	if got := readPortFile(beadsDir); got != state.Port {
		t.Errorf("tracked port after replacement = %d, want %d", got, state.Port)
	}

	// A new server generation, recorded as such.
	newRec := readBinaryRecordForTest(t, beadsDir)
	if newRec.PID == state.PID {
		t.Fatalf("the tracked PID was not replaced: still %d", state.PID)
	}
	if newRec.DoltVersion != "2.7.0" || newRec.Provenance != BinaryProvenanceLaunched {
		t.Errorf("record after replacement = %+v, want dolt 2.7.0, provenance %q", newRec, BinaryProvenanceLaunched)
	}

	// The old generation is gone.
	if isProcessAlive(state.PID) {
		t.Errorf("the replaced server (PID %d) is still running", state.PID)
	}

	// One notice, naming the old and the new version.
	for _, want := range []string{
		"replaced the tracked dolt sql-server",
		"2.6.0",
		"2.7.0",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the replacement notice is missing %q; stderr:\n%s", want, stderr)
		}
	}
}