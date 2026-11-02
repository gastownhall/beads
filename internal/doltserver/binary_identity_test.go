//go:build !windows

package doltserver

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltversion"
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
	if got := CheckServerBinary(beadsDir, state.Port, nil); got.Verdict != BinaryCurrent {
		t.Fatalf("before upgrade: verdict = %v (%v), want current", got.Verdict, got.Reason)
	}

	doltBin := upgradeFakeDolt(t, "2.7.0")
	check := CheckServerBinary(beadsDir, state.Port, nil)
	if check.Verdict != BinaryStale || check.RunningVersion != "2.6.0" || check.CandidateVersion != "2.7.0" || check.PID != state.PID {
		t.Fatalf("after upgrade: check = %+v, want stale 2.6.0 → 2.7.0 for PID %d", check, state.PID)
	}

	res, err := ReplaceStaleServer(beadsDir, state.Port, nil)
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
	if got := CheckServerBinary(beadsDir, state.Port, nil); got.Verdict != BinaryCurrent {
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

			check := CheckServerBinary(beadsDir, state.Port, nil)
			if check.Verdict != BinaryCurrent || check.Refresh {
				t.Fatalf("check = %+v, want current with nothing to refresh", check)
			}
			res, err := ReplaceStaleServer(beadsDir, state.Port, nil)
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

	check := CheckServerBinary(beadsDir, state.Port, nil)
	if check.Verdict != BinaryCurrent || !check.Refresh {
		t.Fatalf("check = %+v, want current with a record to refresh", check)
	}
	res, err := ReplaceStaleServer(beadsDir, state.Port, nil)
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
	if got := CheckServerBinary(beadsDir, state.Port, nil); got.Verdict != BinaryCurrent || got.Refresh {
		t.Errorf("after refresh: check = %+v, want current with nothing to refresh", got)
	}
}

func reportsVersion(v string) ServerVersionFunc {
	return func() (string, error) { return v, nil }
}

// TestLaunchBinaryUnrecordedServerComparedByVersion: a tracked server with no
// launch record (started by a bd that wrote none) is compared by the version
// it reports. A different release than the dolt bd would launch is replaced
// as a recorded stale one is; the same release keeps running, and the dolt it
// was compared with is recorded as observed; an unanswered question changes
// nothing.
func TestLaunchBinaryUnrecordedServerComparedByVersion(t *testing.T) {
	for _, tc := range []struct {
		name          string
		serverVersion ServerVersionFunc
		want          BinaryVerdict
		wantReplaced  bool
		wantRecord    string // provenance of the record afterwards, "" for none
	}{
		{name: "different version", serverVersion: reportsVersion("2.5.9"), want: BinaryStale, wantReplaced: true, wantRecord: binaryProvenanceLaunched},
		{name: "same version", serverVersion: reportsVersion("2.6.0"), want: BinaryCurrent, wantRecord: binaryProvenanceObserved},
		{name: "no answer", serverVersion: func() (string, error) { return "", os.ErrDeadlineExceeded }, want: BinaryUnknown},
		{name: "not asked", want: BinaryUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, launches := fakeStartWorkspace(t)
			stubProcessListLaunchedHere(t)
			state, err := startForTest(t, beadsDir)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if err := os.Remove(binaryRecordPath(beadsDir)); err != nil {
				t.Fatal(err)
			}

			check := CheckServerBinary(beadsDir, state.Port, tc.serverVersion)
			if check.Verdict != tc.want {
				t.Fatalf("check = %+v, want verdict %v", check, tc.want)
			}
			res, err := ReplaceStaleServer(beadsDir, state.Port, tc.serverVersion)
			if res.State != nil && res.State.PID > 0 {
				killAtCleanup(t, res.State.PID)
			}
			if err != nil || res.Replaced != tc.wantReplaced {
				t.Fatalf("ReplaceStaleServer = %+v, %v; want replaced=%v", res, err, tc.wantReplaced)
			}
			wantPID, wantLaunches := state.PID, 1
			if tc.wantReplaced {
				wantPID, wantLaunches = res.State.PID, 2
				if res.State.Port != state.Port || pidAlive(state.PID) {
					t.Errorf("replacement on port %d, old PID alive=%v; want port %d and the old server gone",
						res.State.Port, pidAlive(state.PID), state.Port)
				}
			} else if !pidAlive(state.PID) {
				t.Errorf("server PID %d stopped; want it kept", state.PID)
			}
			if got := launchedPorts(t, launches); len(got) != wantLaunches {
				t.Errorf("launches = %v, want %d", got, wantLaunches)
			}
			rec, ok := readBinaryRecord(beadsDir)
			if tc.wantRecord == "" {
				if ok {
					t.Errorf("record written: %+v", rec)
				}
				return
			}
			if !ok || rec.PID != wantPID || rec.Version != "2.6.0" || rec.Provenance != tc.wantRecord {
				t.Errorf("record = %+v (ok=%v), want PID %d, version 2.6.0, provenance %s", rec, ok, wantPID, tc.wantRecord)
			}
		})
	}
}

// TestLaunchBinaryConcurrentDetectionsReplaceOnce: bd processes that each
// found the same stale server replace it once between them; the rest find
// the replacement current under the lifecycle lock and keep it.
func TestLaunchBinaryConcurrentDetectionsReplaceOnce(t *testing.T) {
	beadsDir, launches := fakeStartWorkspace(t)
	stubProcessListLaunchedHere(t)
	state, err := startForTest(t, beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	upgradeFakeDolt(t, "2.7.0")
	if got := CheckServerBinary(beadsDir, state.Port, nil); got.Verdict != BinaryStale {
		t.Fatalf("check = %+v, want stale", got)
	}

	const callers = 3
	results := make([]BinaryReplacement, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = ReplaceStaleServer(beadsDir, state.Port, nil)
		}()
	}
	close(start)
	wg.Wait()

	replaced := 0
	for i, res := range results {
		if res.State != nil && res.State.PID > 0 {
			killAtCleanup(t, res.State.PID)
		}
		if errs[i] != nil {
			t.Errorf("caller %d: %v", i, errs[i])
		}
		if res.Replaced {
			replaced++
		}
	}
	if replaced != 1 {
		t.Errorf("%d callers replaced the server, want 1", replaced)
	}
	if got := launchedPorts(t, launches); len(got) != 2 {
		t.Errorf("launches = %v, want the original and one replacement", got)
	}
}

// trackUnrelatedProcess points beadsDir's pid file at a live process that is
// not a dolt sql-server, as when the tracked server died and its PID was
// reused, and returns that process.
func trackUnrelatedProcess(t *testing.T, beadsDir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if err := os.WriteFile(pidPath(beadsDir), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// childExited reports whether this process's child pid has exited (signal 0
// cannot tell: it reaches an unreaped child too).
func childExited(pid int) bool {
	var ws syscall.WaitStatus
	got, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	return err == nil && got == pid
}

// TestLaunchBinaryUnconfirmedProcessNotSignaled: ReplaceStaleServer stops only
// a PID confirmed to be a dolt sql-server. A tracked PID the process list
// shows is something else is left alone and nothing is launched; one the
// process list cannot vouch for is refused with an error, unsignaled.
func TestLaunchBinaryUnconfirmedProcessNotSignaled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readable bool
	}{
		{name: "unrelated process", readable: true},
		{name: "process list unreadable", readable: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, launches := fakeStartWorkspace(t)
			stubProcessListLaunchedHere(t)
			state, err := startForTest(t, beadsDir)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			other := trackUnrelatedProcess(t, beadsDir)
			if !tc.readable {
				stubProcessList(t, func() ([]int, error) { return nil, errProcessListUnreadable })
			}
			older := reportsVersion("2.5.9")
			if got := CheckServerBinary(beadsDir, state.Port, older); got.Verdict != BinaryStale || got.PID != other.Process.Pid {
				t.Fatalf("check = %+v, want stale for PID %d", got, other.Process.Pid)
			}

			res, err := ReplaceStaleServer(beadsDir, state.Port, older)
			if res.State != nil && res.State.PID > 0 {
				killAtCleanup(t, res.State.PID)
			}
			if res.Replaced {
				t.Errorf("ReplaceStaleServer replaced PID %d, which is not a confirmed dolt sql-server", other.Process.Pid)
			}
			if tc.readable && err != nil {
				t.Errorf("ReplaceStaleServer = %v, want the unrelated process left alone without an error", err)
			}
			if !tc.readable && err == nil {
				t.Error("ReplaceStaleServer = nil error, want a refusal for an unconfirmed PID")
			}
			if childExited(other.Process.Pid) || !pidAlive(state.PID) {
				t.Errorf("tracked PID exited=%v, fake server alive=%v; want both untouched",
					childExited(other.Process.Pid), pidAlive(state.PID))
			}
			if got := launchedPorts(t, launches); len(got) != 1 {
				t.Errorf("launches = %v, want none beyond the original", got)
			}
		})
	}
}

// TestLaunchBinaryFailedReplacement covers each way a replacement can fail.
// A dolt bd cannot launch is refused before anything is stopped: the server
// and its record stay. A stop that fails keeps the live server's pid, port and
// record files, so the next attempt finds it stale and fails again. A start
// that fails leaves no server running, says so, and keeps the SQL port for
// the next start.
func TestLaunchBinaryFailedReplacement(t *testing.T) {
	t.Run("preflight", func(t *testing.T) {
		beadsDir, launches := fakeStartWorkspace(t)
		stubProcessListLaunchedHere(t)
		state, err := startForTest(t, beadsDir)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		upgradeFakeDolt(t, "2.5.1")

		res, err := ReplaceStaleServer(beadsDir, state.Port, nil)
		if !errors.Is(err, doltversion.ErrBelowRequiredMin) || res.Check.Verdict != BinaryCannotReplace || res.Replaced || res.Stopped {
			t.Fatalf("ReplaceStaleServer = %+v, %v; want a below-minimum refusal", res, err)
		}
		if !pidAlive(state.PID) || len(launchedPorts(t, launches)) != 1 {
			t.Errorf("server alive=%v, launches=%v; want it kept, launched once", pidAlive(state.PID), launchedPorts(t, launches))
		}
		if rec, ok := readBinaryRecord(beadsDir); !ok || rec.PID != state.PID || rec.Version != "2.6.0" {
			t.Errorf("record = %+v (ok=%v), want the original launch record", rec, ok)
		}
	})

	t.Run("stop", func(t *testing.T) {
		beadsDir, launches := fakeStartWorkspace(t)
		stubProcessListLaunchedHere(t)
		state, err := startForTest(t, beadsDir)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		upgradeFakeDolt(t, "2.7.0")
		orig := stopServerProcess
		stopServerProcess = func(int, time.Duration) error { return errors.New("operation not permitted") }
		t.Cleanup(func() { stopServerProcess = orig })

		for attempt := 1; attempt <= 2; attempt++ {
			res, err := ReplaceStaleServer(beadsDir, state.Port, nil)
			if err == nil || !strings.Contains(err.Error(), "operation not permitted") || res.Replaced || res.Stopped {
				t.Fatalf("attempt %d: ReplaceStaleServer = %+v, %v; want the stop failure", attempt, res, err)
			}
			for _, p := range []string{pidPath(beadsDir), portPath(beadsDir), binaryRecordPath(beadsDir)} {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("attempt %d: %s gone after a failed stop: %v", attempt, filepath.Base(p), err)
				}
			}
			if got := CheckServerBinary(beadsDir, state.Port, nil); got.Verdict != BinaryStale || got.PID != state.PID {
				t.Errorf("attempt %d: check = %+v, want the server still found stale", attempt, got)
			}
		}
		if !pidAlive(state.PID) || len(launchedPorts(t, launches)) != 1 {
			t.Errorf("server alive=%v, launches=%v; want it kept, launched once", pidAlive(state.PID), launchedPorts(t, launches))
		}
	})

	t.Run("start", func(t *testing.T) {
		beadsDir, _ := fakeStartWorkspace(t)
		stubProcessListLaunchedHere(t)
		state, err := startForTest(t, beadsDir)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		upgradeFakeDolt(t, "2.7.0")
		t.Setenv(testFakeDoltExitEnv, "1")

		res, err := ReplaceStaleServer(beadsDir, state.Port, nil)
		if res.State != nil && res.State.PID > 0 {
			killAtCleanup(t, res.State.PID)
		}
		if err == nil || !strings.Contains(err.Error(), "server remains stopped") || res.Replaced || !res.Stopped {
			t.Fatalf("ReplaceStaleServer = %+v, %v; want a failed start reported as stopped", res, err)
		}
		if pidAlive(state.PID) {
			t.Errorf("old server PID %d still running", state.PID)
		}
		if b, err := os.ReadFile(portPath(beadsDir)); err != nil || strings.TrimSpace(string(b)) != strconv.Itoa(state.Port) {
			t.Errorf("port file = %q, %v; want port %d kept", b, err, state.Port)
		}
	})
}

// TestLaunchBinaryUntrackedServerNotCompared: a server this workspace does
// not track (no pid file of bd's for the port) is not compared: neither the
// dolt on PATH nor the server is asked anything.
func TestLaunchBinaryUntrackedServerNotCompared(t *testing.T) {
	beadsDir := t.TempDir()
	port := foreignGreeter(t)
	orig := lookPathDolt
	lookPathDolt = func() (string, error) {
		t.Error("the dolt on PATH was looked up for an untracked server")
		return "", errors.New("not looked up")
	}
	t.Cleanup(func() { lookPathDolt = orig })
	asked := func() (string, error) {
		t.Error("an untracked server was asked its version")
		return "", errors.New("not asked")
	}

	if got := CheckServerBinary(beadsDir, port, asked); got.Verdict != BinaryNotTracked {
		t.Errorf("check = %+v, want not tracked", got)
	}
	res, err := ReplaceStaleServer(beadsDir, port, asked)
	if err != nil || res.Replaced {
		t.Errorf("ReplaceStaleServer = %+v, %v; want nothing done", res, err)
	}
	if _, err := os.Stat(binaryRecordPath(beadsDir)); !os.IsNotExist(err) {
		t.Errorf("record written for an untracked server: %v", err)
	}
}
