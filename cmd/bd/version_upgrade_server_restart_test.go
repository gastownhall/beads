//go:build !windows

package main

// GH#6064's flagship test: bd does not keep running a tracked dolt
// sql-server whose executable was replaced by an upgrade. The approved
// trigger is the dolt binary's replacement (not a bd version bump): a
// server runs the binary it was exec'd with for its whole lifetime, so after
// `brew upgrade dolt` an old-generation server keeps serving a store that
// new-generation clients then write to. The store open replaces such a
// server before the application's queries run.
//
// The dolt here is a copy of this test binary installed as `dolt` on PATH
// (see version_upgrade_fake_dolt_test.go), so the whole lifecycle —
// doltserver's ps-based dolt checks included — runs against processes whose
// command line names `dolt`, like the real thing.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
)

// installBdTestDolt puts a copy of this test binary on PATH as `dolt`, and
// returns its path. A copy, not a symlink: the "upgrade" below must be able
// to change the executable's file identity (its mtime) the way a real
// installer replaces the binary.
func installBdTestDolt(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving this test binary: %v", err)
	}
	src, err := os.Open(self) //nolint:gosec // G304: this binary, resolved above
	if err != nil {
		t.Fatalf("opening this test binary: %v", err)
	}
	defer src.Close() //nolint:errcheck // read-only
	doltBin := filepath.Join(dir, "dolt")
	dst, err := os.OpenFile(doltBin, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755) //nolint:gosec // G302: the stand-in dolt must be executable
	if err != nil {
		t.Fatalf("creating the stand-in dolt: %v", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatalf("copying this test binary as the stand-in dolt: %v", err)
	}
	if err := dst.Close(); err != nil {
		t.Fatalf("finishing the stand-in dolt: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(testFakeDoltEnv, "1")
	return doltBin
}

// fakeDoltWorkspace prepares a per-project workspace around the stand-in
// dolt, with the user config isolated, and returns its beads dir, the file
// the fake records each launch's port in, and the dolt's path.
func fakeDoltWorkspace(t *testing.T) (beadsDir, launches, doltBin string) {
	t.Helper()
	doltBin = installBdTestDolt(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	// bd owns this workspace's server: nothing declares it external.
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	for _, name := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_SOCKET"} {
		t.Setenv(name, "")
	}
	t.Setenv("BEADS_DOLT_READY_TIMEOUT", "20")
	t.Setenv(testFakeDoltDelayEnv, "200ms")
	launches = filepath.Join(t.TempDir(), "launches")
	t.Setenv(testFakeDoltLaunchesEnv, launches)
	beadsDir = filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	return beadsDir, launches, doltBin
}

func launchedPortsForTest(t *testing.T, launches string) []string {
	t.Helper()
	b, err := os.ReadFile(launches) //nolint:gosec // G304: test-owned path
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

// serverBinaryRecordForTest is the part of the launch record
// (doltserver.BinaryRecordFileName) these tests read.
type serverBinaryRecordForTest struct {
	Schema     int    `json:"schema"`
	PID        int    `json:"pid"`
	Version    string `json:"version"`
	Provenance string `json:"provenance"`
}

func readServerBinaryRecordForTest(t *testing.T, beadsDir string) serverBinaryRecordForTest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(beadsDir, doltserver.BinaryRecordFileName)) //nolint:gosec // G304: test-owned workspace
	if err != nil {
		t.Fatalf("reading the launch record: %v", err)
	}
	var rec serverBinaryRecordForTest
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("parsing the launch record: %v", err)
	}
	return rec
}

// TestVersionUpgradeStopsServerStartedByPreviousVersion: a tracked dolt
// sql-server whose executable has since been replaced by an upgrade is
// replaced by the lifecycle the store open calls (doltserver's
// ReplaceStaleServer): a new PID on the same port, the old server gone and
// the replacement recorded. The dolt is a real process whose command line
// names `dolt`, so doltserver's process-list check is not stubbed.
func TestVersionUpgradeStopsServerStartedByPreviousVersion(t *testing.T) {
	isolateBeadsDirForTest(t)
	beadsDir, launches, doltBin := fakeDoltWorkspace(t)
	t.Cleanup(func() { _ = doltserver.Stop(beadsDir) })

	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("starting the tracked server from dolt 2.6.0: %v", err)
	}
	if state.PID <= 0 || state.Port <= 0 {
		t.Fatalf("start reported PID %d, port %d", state.PID, state.Port)
	}

	// The start recorded the launch: the tracked PID and the executable's
	// version.
	rec := readServerBinaryRecordForTest(t, beadsDir)
	if rec.Schema != 1 || rec.PID != state.PID || rec.Version != "2.6.0" || rec.Provenance != "launched" {
		t.Fatalf("launch record = %+v, want schema 1, PID %d, dolt 2.6.0, provenance launched", rec, state.PID)
	}
	if got := doltserver.CheckServerBinary(beadsDir, state.Port, nil); got.Verdict != doltserver.BinaryCurrent {
		t.Fatalf("before the upgrade: check = %+v, want current", got)
	}

	// Upgrade dolt under the running server: the executable at the recorded
	// path is replaced (a new file identity), reporting a new version.
	newTime := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(doltBin, newTime, newTime); err != nil {
		t.Fatalf("replacing the dolt executable's identity: %v", err)
	}
	t.Setenv(testFakeDoltVersionEnv, "2.7.0")

	res, err := doltserver.ReplaceStaleServer(beadsDir, state.Port, nil)
	if err != nil {
		t.Fatalf("replacing the server after the upgrade: %v", err)
	}
	if !res.Replaced || res.State == nil || res.Check.RunningVersion != "2.6.0" || res.Check.CandidateVersion != "2.7.0" {
		t.Fatalf("ReplaceStaleServer = %+v, want a replacement of dolt 2.6.0 by 2.7.0", res)
	}

	// One relaunch, on the same port.
	ports := launchedPortsForTest(t, launches)
	if len(ports) != 2 {
		t.Fatalf("the fake dolt launched %d time(s) (%v), want 2: the original start and the replacement", len(ports), ports)
	}
	if ports[0] != ports[1] || res.State.Port != state.Port {
		t.Errorf("the replacement moved the port: %s -> %s (state %d), want %d", ports[0], ports[1], res.State.Port, state.Port)
	}

	// A new server generation, recorded as such.
	rec = readServerBinaryRecordForTest(t, beadsDir)
	if rec.PID == state.PID || rec.PID != res.State.PID {
		t.Fatalf("record PID = %d, want the replacement's %d (old %d)", rec.PID, res.State.PID, state.PID)
	}
	if rec.Version != "2.7.0" || rec.Provenance != "launched" {
		t.Errorf("record after replacement = %+v, want dolt 2.7.0, provenance launched", rec)
	}
	// The old generation is gone: the PID the previous dolt's server ran
	// under no longer names a process.
	if p, err := os.FindProcess(state.PID); err == nil {
		if err := p.Signal(syscall.Signal(0)); err == nil {
			t.Errorf("the server started by the previous dolt (PID %d) is still running", state.PID)
		}
	}
	if got := doltserver.CheckServerBinary(beadsDir, state.Port, nil); got.Verdict != doltserver.BinaryCurrent {
		t.Errorf("after the replacement: check = %+v, want current", got)
	}
}
