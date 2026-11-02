//go:build !windows

package doltserver

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
// TestSyncServerBinary_RetainsCurrentServer is AC-2: an unchanged
// executable — even when a different opener's PATH would resolve another
// dolt — and a same-version replacement never restart the tracked server.
// The PID is retained without a restart notice; only the same-version
// replacement refreshes the record, so the next open does not re-probe it.
func TestSyncServerBinary_RetainsCurrentServer(t *testing.T) {
	sync := func(t *testing.T, beadsDir string, port int) (error, string) {
		t.Helper()
		var err error
		stderr := captureStderr(t, func() {
			err = SyncServerBinary(BinarySyncRequest{
				BeadsDir:     beadsDir,
				Host:         "127.0.0.1",
				Port:         port,
				AllowRestart: true,
			})
		})
		return err, stderr
	}

	t.Run("unchanged executable", func(t *testing.T) {
		beadsDir, launches := fakeStartWorkspace(t)
		stopTrackedServerAfterTest(t, beadsDir)
		state, err := startForTest(t, beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		err, stderr := sync(t, beadsDir, state.Port)
		if err != nil {
			t.Fatalf("SyncServerBinary on an unchanged executable: %v", err)
		}
		if got := launchedPorts(t, launches); len(got) != 1 {
			t.Errorf("launched %v, want only the original start", got)
		}
		if pid := readTrackedPID(beadsDir); pid != state.PID {
			t.Errorf("tracked PID = %d, want the retained %d", pid, state.PID)
		}
		if stderr != "" {
			t.Errorf("an unchanged executable produced output:\n%s", stderr)
		}
		rec := readBinaryRecordForTest(t, beadsDir)
		if rec.Provenance != BinaryProvenanceLaunched {
			t.Errorf("record provenance = %q, want the original %q (nothing to refresh)", rec.Provenance, BinaryProvenanceLaunched)
		}
	})

	t.Run("different opener PATH with unchanged recorded executable", func(t *testing.T) {
		beadsDir, launches := fakeStartWorkspace(t)
		stopTrackedServerAfterTest(t, beadsDir)
		state, err := startForTest(t, beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		// Another dolt earlier on PATH reports a different version. The
		// server's executable never changed, so no opener's PATH matters:
		// comparing the PATH dolt to the server would restart a perfectly
		// current server.
		otherDir := t.TempDir()
		other := "#!/bin/sh\necho \"dolt version 9.9.9\"\n"
		if err := os.WriteFile(filepath.Join(otherDir, "dolt"), []byte(other), 0o700); err != nil { //nolint:gosec // G306: the shim must be executable
			t.Fatal(err)
		}
		t.Setenv("PATH", otherDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		err, stderr := sync(t, beadsDir, state.Port)
		if err != nil {
			t.Fatalf("SyncServerBinary with another dolt on PATH: %v", err)
		}
		if got := launchedPorts(t, launches); len(got) != 1 {
			t.Errorf("launched %v, want only the original start (no restart)", got)
		}
		if pid := readTrackedPID(beadsDir); pid != state.PID {
			t.Errorf("tracked PID = %d, want the retained %d", pid, state.PID)
		}
		if stderr != "" {
			t.Errorf("an unchanged executable produced output:\n%s", stderr)
		}
	})

	t.Run("same-version replacement refreshes the record", func(t *testing.T) {
		beadsDir, launches := fakeStartWorkspace(t)
		stopTrackedServerAfterTest(t, beadsDir)
		state, err := startForTest(t, beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		oldRec := readBinaryRecordForTest(t, beadsDir)

		// The executable is replaced (a new file identity) by one that
		// reports the same version: not skew, so no restart.
		replaceFakeDolt(t, "2.6.0")

		err, stderr := sync(t, beadsDir, state.Port)
		if err != nil {
			t.Fatalf("SyncServerBinary after a same-version replacement: %v", err)
		}
		if got := launchedPorts(t, launches); len(got) != 1 {
			t.Errorf("launched %v, want only the original start (no restart)", got)
		}
		if pid := readTrackedPID(beadsDir); pid != state.PID {
			t.Errorf("tracked PID = %d, want the retained %d", pid, state.PID)
		}
		if stderr != "" {
			t.Errorf("a same-version replacement produced output:\n%s", stderr)
		}
		// But the observation is recorded, so the next open takes the fast
		// path instead of re-probing the replacement.
		newRec := readBinaryRecordForTest(t, beadsDir)
		if newRec.PID != state.PID {
			t.Errorf("refreshed record names PID %d, want the retained %d", newRec.PID, state.PID)
		}
		if newRec.DoltVersion != "2.6.0" || !newRec.KnownVersion {
			t.Errorf("refreshed record = %+v, want dolt 2.6.0 (known)", newRec)
		}
		if newRec.Provenance != BinaryProvenanceObserved {
			t.Errorf("refreshed record provenance = %q, want %q", newRec.Provenance, BinaryProvenanceObserved)
		}
		if newRec.ModTimeNanos == oldRec.ModTimeNanos || newRec.Size == oldRec.Size {
			t.Errorf("refreshed record kept the replaced executable's identity: old %+v, new %+v", oldRec, newRec)
		}
	})
}

// TestSyncServerBinary_LegacyServerBootstrapsByProbe is AC-3: a legacy
// tracked server (launched before the sidecar existed, so only the PID and
// port files name it) is classified by asking it for its version — bounded,
// with the open's credentials — and comparing with the launch candidate.
// Different versions trigger the same replacement a replaced executable
// does; equal versions retain the PID and record the observed identity; a
// probe that cannot answer is unknown and never actionable.
func TestSyncServerBinary_LegacyServerBootstrapsByProbe(t *testing.T) {
	// startLegacy starts a tracked server and erases the binary witness
	// record, leaving exactly the pre-sidecar state.
	startLegacy := func(t *testing.T) (beadsDir, launches string, state *State) {
		t.Helper()
		beadsDir, launches = fakeStartWorkspace(t)
		stopTrackedServerAfterTest(t, beadsDir)
		state, err := startForTest(t, beadsDir)
		if err != nil {
			t.Fatalf("starting the tracked server: %v", err)
		}
		if err := os.Remove(ServerBinaryPath(beadsDir)); err != nil {
			t.Fatalf("erasing the binary witness record (the legacy state): %v", err)
		}
		return beadsDir, launches, state
	}
	stubProbe := func(t *testing.T, version string, ok bool) {
		t.Helper()
		orig := probeServerDoltVersion
		t.Cleanup(func() { probeServerDoltVersion = orig })
		probeServerDoltVersion = func(BinarySyncRequest) (string, bool) {
			return version, ok
		}
	}
	sync := func(t *testing.T, beadsDir string, port int) (error, string) {
		t.Helper()
		var err error
		stderr := captureStderr(t, func() {
			err = SyncServerBinary(BinarySyncRequest{
				BeadsDir:     beadsDir,
				Host:         "127.0.0.1",
				Port:         port,
				AllowRestart: true,
			})
		})
		return err, stderr
	}

	t.Run("different versions replace the server", func(t *testing.T) {
		beadsDir, launches, state := startLegacy(t)
		// The server answers 2.5.0; the dolt on PATH is 2.6.0.
		stubProbe(t, "2.5.0", true)

		err, stderr := sync(t, beadsDir, state.Port)
		if err != nil {
			t.Fatalf("SyncServerBinary on a legacy server running an older dolt: %v", err)
		}
		if got := launchedPorts(t, launches); len(got) != 2 || got[0] != got[1] {
			t.Fatalf("launched %v, want one replacement on the same port", got)
		}
		rec := readBinaryRecordForTest(t, beadsDir)
		if rec.PID == state.PID {
			t.Fatalf("the legacy server (PID %d) was not replaced", state.PID)
		}
		if rec.DoltVersion != "2.6.0" || rec.Provenance != BinaryProvenanceLaunched {
			t.Errorf("record after replacement = %+v, want dolt 2.6.0, provenance %q", rec, BinaryProvenanceLaunched)
		}
		for _, want := range []string{"replaced the tracked dolt sql-server", "2.5.0", "2.6.0"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("the replacement notice is missing %q; stderr:\n%s", want, stderr)
			}
		}
	})

	t.Run("equal versions retain the PID and record the observation", func(t *testing.T) {
		beadsDir, launches, state := startLegacy(t)
		stubProbe(t, "2.6.0", true)

		err, stderr := sync(t, beadsDir, state.Port)
		if err != nil {
			t.Fatalf("SyncServerBinary on a legacy server running the same dolt: %v", err)
		}
		if got := launchedPorts(t, launches); len(got) != 1 {
			t.Errorf("launched %v, want only the original start (no restart)", got)
		}
		if stderr != "" {
			t.Errorf("a current legacy server produced output:\n%s", stderr)
		}
		rec := readBinaryRecordForTest(t, beadsDir)
		if rec.PID != state.PID {
			t.Errorf("observed record names PID %d, want the retained %d", rec.PID, state.PID)
		}
		if rec.DoltVersion != "2.6.0" || !rec.KnownVersion {
			t.Errorf("observed record = %+v, want dolt 2.6.0 (known)", rec)
		}
		if rec.Provenance != BinaryProvenanceObserved {
			t.Errorf("observed record provenance = %q, want %q (the probe proves correlation, not launch)", rec.Provenance, BinaryProvenanceObserved)
		}
	})

	t.Run("a probe that cannot answer is unknown", func(t *testing.T) {
		beadsDir, launches, state := startLegacy(t)
		stubProbe(t, "", false)

		err, stderr := sync(t, beadsDir, state.Port)
		if err != nil {
			t.Fatalf("an unanswerable probe must not fail the open: %v", err)
		}
		if got := launchedPorts(t, launches); len(got) != 1 {
			t.Errorf("launched %v, want only the original start (no restart)", got)
		}
		if pid := readTrackedPID(beadsDir); pid != state.PID {
			t.Errorf("tracked PID = %d, want the retained %d", pid, state.PID)
		}
		if stderr != "" {
			t.Errorf("an unknown verdict produced output:\n%s", stderr)
		}
		if _, statErr := os.Stat(ServerBinaryPath(beadsDir)); !os.IsNotExist(statErr) {
			t.Errorf("an unknown verdict must not write a record: %v", statErr)
		}
	})
}
