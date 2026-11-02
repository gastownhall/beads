//go:build !windows

package doltserver

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// TestSyncServerBinary_ConcurrentDetectionReplacesOnce is AC-4: when two
// opens detect the same stale generation, the revalidation each caller
// runs under the lifecycle lock means the replacement happens at most once
// — the caller that lost the race finds the server current and stands down
// instead of restarting the fresh one.
func TestSyncServerBinary_ConcurrentDetectionReplacesOnce(t *testing.T) {
	beadsDir, launches := fakeStartWorkspace(t)
	stopTrackedServerAfterTest(t, beadsDir)
	state, err := startForTest(t, beadsDir)
	if err != nil {
		t.Fatalf("starting the tracked server: %v", err)
	}
	replaceFakeDolt(t, "2.7.0")

	const callers = 2
	var barrier, done sync.WaitGroup
	barrier.Add(callers)
	done.Add(callers)
	errs := make([]error, callers)
	for i := range errs {
		go func(i int) {
			defer done.Done()
			barrier.Done() // both callers classify the same stale generation
			barrier.Wait()
			errs[i] = SyncServerBinary(BinarySyncRequest{
				BeadsDir:     beadsDir,
				Host:         "127.0.0.1",
				Port:         state.Port,
				AllowRestart: true,
			})
		}(i)
	}
	done.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}

	// Exactly one replacement: two launches total, one new PID, port kept.
	ports := launchedPorts(t, launches)
	if len(ports) != 2 {
		t.Fatalf("the fake dolt launched %d time(s) (%v), want 2: the original start and one replacement", len(ports), ports)
	}
	if ports[0] != ports[1] {
		t.Errorf("the replacement moved the port: %s -> %s", ports[0], ports[1])
	}
	rec := readBinaryRecordForTest(t, beadsDir)
	if rec.PID == state.PID {
		t.Fatalf("the stale server (PID %d) was never replaced", state.PID)
	}
}

// TestSyncServerBinary_IgnoresAnotherPIDsRecord is AC-4's safety half: a
// record naming a PID other than the tracked one is ignored (it is evidence
// about another generation, not this server), and nothing is signaled on
// the strength of it.
func TestSyncServerBinary_IgnoresAnotherPIDsRecord(t *testing.T) {
	beadsDir, launches := fakeStartWorkspace(t)
	stopTrackedServerAfterTest(t, beadsDir)
	state, err := startForTest(t, beadsDir)
	if err != nil {
		t.Fatalf("starting the tracked server: %v", err)
	}

	// A record left behind by a previous generation names a PID the
	// workspace does not track.
	b, err := json.Marshal(&BinaryRecord{
		PID:        state.PID + 10000, // not the tracked PID
		RealPath:   "/nonexistent/dolt",
		Provenance: BinaryProvenanceLaunched,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ServerBinaryPath(beadsDir), b, 0o600); err != nil {
		t.Fatal(err)
	}

	var syncErr error
	stderr := captureStderr(t, func() {
		syncErr = SyncServerBinary(BinarySyncRequest{
			BeadsDir:     beadsDir,
			Host:         "127.0.0.1",
			Port:         state.Port,
			AllowRestart: true,
		})
	})
	// The record is not usable evidence, so the legacy probe decides — and
	// the fake server speaks no MySQL, so the probe cannot answer and the
	// verdict is unknown: nothing restarted, nothing signaled, no output.
	if syncErr != nil {
		t.Fatalf("SyncServerBinary with another PID's record: %v", syncErr)
	}
	if got := launchedPorts(t, launches); len(got) != 1 {
		t.Errorf("launched %v, want only the original start (nothing restarted)", got)
	}
	if !isProcessAlive(state.PID) {
		t.Error("the tracked server was stopped, but nothing proved it stale")
	}
	if pid := readTrackedPID(beadsDir); pid != state.PID {
		t.Errorf("tracked PID = %d, want the retained %d", pid, state.PID)
	}
	if stderr != "" {
		t.Errorf("an unknown verdict produced output:\n%s", stderr)
	}
}

// TestSyncServerBinary_FailedReplacement is AC-5: a replacement this open
// initiated that cannot complete fails the open before any application
// query runs — and each failure mode leaves the workspace in the state its
// error names: a below-floor replacement never stops the running server, a
// stop whose exit is unconfirmed keeps the tracked evidence, and a failed
// start leaves the server stopped with the port retained.
func TestSyncServerBinary_FailedReplacement(t *testing.T) {
	startServer := func(t *testing.T) (beadsDir, launches string, state *State) {
		t.Helper()
		beadsDir, launches = fakeStartWorkspace(t)
		stopTrackedServerAfterTest(t, beadsDir)
		state, err := startForTest(t, beadsDir)
		if err != nil {
			t.Fatalf("starting the tracked server: %v", err)
		}
		return beadsDir, launches, state
	}

	t.Run("below-floor replacement preserves the server", func(t *testing.T) {
		beadsDir, launches, state := startServer(t)
		// The upgrade installs a dolt older than the one bd requires.
		replaceFakeDolt(t, "2.5.1")

		var syncErr error
		stderr := captureStderr(t, func() {
			syncErr = SyncServerBinary(BinarySyncRequest{
				BeadsDir:     beadsDir,
				Host:         "127.0.0.1",
				Port:         state.Port,
				AllowRestart: true,
			})
		})
		if syncErr == nil {
			t.Fatal("a replacement bd would refuse to launch must fail the open")
		}
		for _, want := range []string{"cannot be launched", "running server is unchanged"} {
			if !strings.Contains(syncErr.Error(), want) {
				t.Errorf("error is missing %q: %v", want, syncErr)
			}
		}
		if !isProcessAlive(state.PID) {
			t.Error("the running server was stopped for a replacement bd would refuse to launch")
		}
		if got := launchedPorts(t, launches); len(got) != 1 {
			t.Errorf("launched %v, want only the original start (nothing relaunched)", got)
		}
		if pid := readTrackedPID(beadsDir); pid != state.PID {
			t.Errorf("tracked PID = %d, want the preserved %d", pid, state.PID)
		}
		if stderr != "" {
			t.Errorf("the refusal produced unexpected output:\n%s", stderr)
		}
	})

	t.Run("failed stop retains the evidence", func(t *testing.T) {
		beadsDir, launches, state := startServer(t)
		replaceFakeDolt(t, "2.7.0")
		// The stop cannot confirm the server's exit.
		origStop := gracefulStop
		t.Cleanup(func() { gracefulStop = origStop })
		gracefulStop = func(int, time.Duration) error {
			return errors.New("exit could not be confirmed")
		}

		var syncErr error
		captureStderr(t, func() {
			syncErr = SyncServerBinary(BinarySyncRequest{
				BeadsDir:     beadsDir,
				Host:         "127.0.0.1",
				Port:         state.Port,
				AllowRestart: true,
			})
		})
		if syncErr == nil {
			t.Fatal("a replacement whose stop failed must fail the open")
		}
		// The server is possibly still alive, so its evidence must stay:
		// removing the state files would orphan a live server bd can no
		// longer find, and would erase what the next bd needs to classify.
		for _, path := range []string{pidPath(beadsDir), portPath(beadsDir), ServerBinaryPath(beadsDir)} {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("stop failure removed %s, want the evidence retained: %v", filepath.Base(path), err)
			}
		}
		if !isProcessAlive(state.PID) {
			t.Errorf("the tracked server (PID %d) is gone, but its exit was never confirmed", state.PID)
		}
		if got := launchedPorts(t, launches); len(got) != 1 {
			t.Errorf("launched %v, want only the original start (nothing relaunched)", got)
		}
	})

	t.Run("failed start reports the stopped state and the retained port", func(t *testing.T) {
		beadsDir, launches, state := startServer(t)
		replaceFakeDolt(t, "2.7.0")
		// The replacement's launch fails: the new dolt exits during startup.
		t.Setenv(testFakeDoltExitEnv, "1")

		var syncErr error
		captureStderr(t, func() {
			syncErr = SyncServerBinary(BinarySyncRequest{
				BeadsDir:     beadsDir,
				Host:         "127.0.0.1",
				Port:         state.Port,
				AllowRestart: true,
			})
		})
		if syncErr == nil {
			t.Fatal("a replacement whose start failed must fail the open")
		}
		if !strings.Contains(syncErr.Error(), "server remains stopped") {
			t.Errorf("the error must report the stopped state: %v", syncErr)
		}
		// The port the old server had is retained as desired state, so the
		// operator's next start does not move the workspace.
		if got := readPortFile(beadsDir); got != state.Port {
			t.Errorf("port file = %d, want the retained %d", got, state.Port)
		}
		if got := launchedPorts(t, launches); len(got) != 2 {
			t.Errorf("launched %v, want the original start and the failed relaunch", got)
		}
	})
}

// assertAdvisoryOnly checks the AC-6 invariant after an advisory open: the
// server is exactly as it was — alive, once-launched, on its port, with the
// launch record no advisory rewrote — and stderr names every wanted phrase.
func assertAdvisoryOnly(t *testing.T, serverDir string, state *State, launches, stderr string, wants []string) {
	t.Helper()
	if !isProcessAlive(state.PID) {
		t.Errorf("the tracked server (PID %d) was stopped, want it left running", state.PID)
	}
	if ports := launchedPorts(t, launches); len(ports) != 1 {
		t.Errorf("the fake dolt launched %d time(s) (%v), want 1: the original start only", len(ports), ports)
	}
	if got := readPortFile(serverDir); got != state.Port {
		t.Errorf("tracked port = %d, want %d unchanged", got, state.Port)
	}
	if rec := readBinaryRecordForTest(t, serverDir); rec.PID != state.PID || rec.DoltVersion != "2.6.0" || rec.Provenance != BinaryProvenanceLaunched {
		t.Errorf("binary record = %+v, want the original launch record (PID %d, dolt 2.6.0, provenance %q)", rec, state.PID, BinaryProvenanceLaunched)
	}
	for _, want := range wants {
		if !strings.Contains(stderr, want) {
			t.Errorf("the advisory is missing %q; stderr:\n%s", want, stderr)
		}
	}
}

// TestSyncServerBinary_AdvisesWhenItMayNotRestart is AC-6: an open without
// lifecycle-mutation permission — a read-only open, or any open of a shared
// server — never mutates the server or its sidecar and never fails, but the
// stale generation is not silently ignored either: one actionable advisory
// names both versions and the remedy. The shared-server advice says what a
// restart interrupts: every connected client and every database it serves.
func TestSyncServerBinary_AdvisesWhenItMayNotRestart(t *testing.T) {
	sync := func(t *testing.T, req BinarySyncRequest) (string, error) {
		t.Helper()
		var err error
		stderr := captureStderr(t, func() {
			err = SyncServerBinary(req)
		})
		return stderr, err
	}

	t.Run("owned server, open without restart permission", func(t *testing.T) {
		beadsDir, launches := fakeStartWorkspace(t)
		stopTrackedServerAfterTest(t, beadsDir)

		state, err := startForTest(t, beadsDir)
		if err != nil {
			t.Fatalf("starting the tracked server: %v", err)
		}
		replaceFakeDolt(t, "2.7.0")

		stderr, err := sync(t, BinarySyncRequest{
			BeadsDir:     beadsDir,
			Host:         "127.0.0.1",
			Port:         state.Port,
			AllowRestart: false,
		})
		if err != nil {
			t.Fatalf("an advisory open must not fail: %v", err)
		}
		assertAdvisoryOnly(t, beadsDir, state, launches, stderr, []string{
			"Warning:", "did not restart it", "bd dolt stop && bd dolt start", "2.6.0", "2.7.0",
		})
	})

	t.Run("shared server names the client impact", func(t *testing.T) {
		beadsDir, launches := fakeStartWorkspace(t)
		// Shared-server mode: the tracked server's state lives in the shared
		// dir, and the fixed shared default port is dialed by every workspace
		// on the host, so the test runs on a private port of its own.
		t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		if err := ln.Close(); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
		sharedDir := resolveServerDir(beadsDir)
		stopTrackedServerAfterTest(t, sharedDir)

		state, err := startForTest(t, sharedDir)
		if err != nil {
			t.Fatalf("starting the shared server: %v", err)
		}
		replaceFakeDolt(t, "2.7.0")

		stderr, err := sync(t, BinarySyncRequest{
			BeadsDir: beadsDir,
			Host:     "127.0.0.1",
			Port:     state.Port,
			// Restart permission alone does not authorize touching a shared
			// server; the advisory must still be the only effect.
			AllowRestart: true,
		})
		if err != nil {
			t.Fatalf("an advisory open must not fail: %v", err)
		}
		assertAdvisoryOnly(t, sharedDir, state, launches, stderr, []string{
			"Warning:", "shared dolt sql-server", "every connected client and every database it serves",
			"bd dolt stop && bd dolt start", "2.6.0", "2.7.0",
		})
	})

	t.Run("unlaunchable replacement stays advisory", func(t *testing.T) {
		beadsDir, launches := fakeStartWorkspace(t)
		stopTrackedServerAfterTest(t, beadsDir)

		state, err := startForTest(t, beadsDir)
		if err != nil {
			t.Fatalf("starting the tracked server: %v", err)
		}
		// The upgrade lands below the required floor, so the replacement
		// cannot be launched: with permission that fails the open (AC-5),
		// without it the running server keeps serving under an advisory.
		replaceFakeDolt(t, "2.5.1")

		stderr, err := sync(t, BinarySyncRequest{
			BeadsDir:     beadsDir,
			Host:         "127.0.0.1",
			Port:         state.Port,
			AllowRestart: false,
		})
		if err != nil {
			t.Fatalf("an advisory open must not fail, even for an unlaunchable replacement: %v", err)
		}
		assertAdvisoryOnly(t, beadsDir, state, launches, stderr, []string{
			"Warning:", "cannot launch the replacement", "used the running server unchanged", "Fix dolt",
		})
	})
}

// TestSyncServerBinary_IgnoresUntrackedEndpoint is AC-7's untracked clause:
// a live server answering on the dialed port is not, by itself, one bd may
// compare or touch — only the workspace's own port/PID files prove tracking.
// An open from a workspace that tracks nothing gets no comparison, no
// lifecycle change and no output, and the unrelated server keeps running.
func TestSyncServerBinary_IgnoresUntrackedEndpoint(t *testing.T) {
	// One launch ledger: the harness process writes every launch to the
	// file the env var names, so both workspaces share it here.
	beadsDir, launches := fakeStartWorkspace(t)
	stopTrackedServerAfterTest(t, beadsDir)

	// A second workspace that tracks nothing, while the first one owns a
	// live server on a private port.
	tracked := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(tracked, 0o750); err != nil {
		t.Fatal(err)
	}
	stopTrackedServerAfterTest(t, tracked)
	state, err := startForTest(t, tracked)
	if err != nil {
		t.Fatalf("starting the tracked server: %v", err)
	}

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
		t.Fatalf("an untracked endpoint must be a silent no-op: %v", syncErr)
	}
	if stderr != "" {
		t.Errorf("an untracked endpoint produced output %q, want none", stderr)
	}
	if !isProcessAlive(state.PID) {
		t.Errorf("the untracked server (PID %d) was signaled, want it untouched", state.PID)
	}
	if ports := launchedPorts(t, launches); len(ports) != 1 {
		t.Errorf("the fake dolt launched %d time(s) (%v), want 1: the original start only", len(ports), ports)
	}
}
