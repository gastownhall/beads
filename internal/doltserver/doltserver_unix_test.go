//go:build !windows

package doltserver

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

// startStaleServerFiles writes a pid file naming an unrelated process (a
// sleep child, as if the PID had been reused) and a port file. The returned
// channel receives the child's exit.
func startStaleServerFiles(t *testing.T, dir string) chan error {
	t.Helper()
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	child := exec.Command("sleep", "300")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-exited
	})
	if err := os.WriteFile(pidPath(dir), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writePortFile(dir, 14599); err != nil {
		t.Fatal(err)
	}
	return exited
}

func expectNotSignaled(t *testing.T, exited chan error) {
	t.Helper()
	select {
	case err := <-exited:
		exited <- err // let the cleanup finish
		t.Errorf("the unrelated process was signaled: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestStopDoesNotSignalUnverifiedProcess(t *testing.T) {
	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	stubProcessList(t, func() ([]int, error) { return nil, errProcessListUnreadable })

	err := Stop(dir)
	if err == nil || errors.Is(err, ErrServerNotRunning) {
		t.Errorf("Stop = %v, want an error refusing the unverified PID", err)
	} else {
		// Where ps never runs, the refusal is the operator's way out: it names
		// the state files to remove if the PID is not a dolt sql-server.
		for _, path := range []string{pidPath(dir), portPath(dir)} {
			if !strings.Contains(err.Error(), path) {
				t.Errorf("refusal does not name %s: %v", path, err)
			}
		}
	}
	expectNotSignaled(t, exited)
	for _, path := range []string{pidPath(dir), portPath(dir)} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to be kept: %v", filepath.Base(path), err)
		}
	}
}

func TestStopDoesNotSignalReusedPIDWhenListBecomesReadable(t *testing.T) {
	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	// The first read (inside IsRunning) fails, the second (in stopLocked) works
	// and does not contain the PID.
	calls := 0
	stubProcessList(t, func() ([]int, error) {
		calls++
		if calls == 1 {
			return nil, errProcessListUnreadable
		}
		return nil, nil
	})

	if err := Stop(dir); !errors.Is(err, ErrServerNotRunning) {
		t.Errorf("Stop = %v, want ErrServerNotRunning for a reused PID", err)
	}
	expectNotSignaled(t, exited)
	for _, path := range []string{pidPath(dir), portPath(dir)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, stat err = %v", filepath.Base(path), err)
		}
	}
}

// With no port recorded for the unverified PID, no other bd process reaches a
// server through its pid file: Stop removes it, as before GH#7214, but never
// signals the PID.
func TestStopWithoutPortDoesNotSignalUnverifiedProcess(t *testing.T) {
	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	if err := os.Remove(portPath(dir)); err != nil {
		t.Fatal(err)
	}
	stubProcessList(t, func() ([]int, error) { return nil, errProcessListUnreadable })

	if err := Stop(dir); !errors.Is(err, ErrServerNotRunning) {
		t.Errorf("Stop = %v, want ErrServerNotRunning", err)
	}
	expectNotSignaled(t, exited)
	if _, err := os.Stat(pidPath(dir)); !os.IsNotExist(err) {
		t.Errorf("expected the pid file to be removed, stat err = %v", err)
	}
}

// Restart over the same state signals nothing on its way to the start.
func TestRestartWithoutPortDoesNotSignalUnverifiedProcess(t *testing.T) {
	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	if err := os.Remove(portPath(dir)); err != nil {
		t.Fatal(err)
	}
	stubProcessList(t, func() ([]int, error) { return nil, errProcessListUnreadable })
	t.Setenv("PATH", t.TempDir()) // no dolt binary: the start after the stop fails

	if _, err := Restart(dir); err == nil {
		t.Error("Restart succeeded without a dolt binary")
	}
	expectNotSignaled(t, exited)
}

// stubSignalZero replaces the signal-0 check for the test.
func stubSignalZero(t *testing.T, signal func(pid int) error) {
	t.Helper()
	orig := signalZero
	signalZero = signal
	t.Cleanup(func() { signalZero = orig })
}

// errNotPermitted is what signal 0 returns for a process that belongs to
// another account.
var errNotPermitted = os.NewSyscallError("kill", syscall.EPERM)

// GH#7214: kill(pid, 0) answering EPERM means the process exists but belongs
// to another account, so IsRunning must count it as alive and keep its state.
func TestIsRunningKeepsStateForProcessItMayNotSignal(t *testing.T) {
	dir := t.TempDir()
	writeTrackedStateForLiveProcess(t, dir, 14567)
	stubSignalZero(t, func(int) error { return errNotPermitted })
	stubProcessList(t, func() ([]int, error) { return []int{os.Getpid()}, nil })

	state, err := IsRunning(dir)
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if !state.Running || state.PID != os.Getpid() || state.Port != 14567 {
		t.Errorf("IsRunning = %+v, want Running with PID %d and port 14567", state, os.Getpid())
	}
	assertStateFilesKept(t, dir, 14567)
}

// With no port recorded or configured, IsRunning reports a listed dolt
// sql-server as running with an unresolved port — except one this user may
// not signal (EPERM): no signal would reach it, and no caller could stop it
// either, so it is reported not running and its pid file kept for a start to
// replace.
func TestIsRunningDoesNotStopProcessItMayNotSignalWithoutPort(t *testing.T) {
	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	if err := os.Remove(portPath(dir)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	if port := DefaultConfig(dir).Port; port != 0 {
		t.Fatalf("precondition: DefaultConfig resolved port %d, want none", port)
	}
	data, err := os.ReadFile(pidPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	stubSignalZero(t, func(int) error { return errNotPermitted })
	stubProcessList(t, func() ([]int, error) { return []int{pid}, nil })

	state, err := IsRunning(dir)
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if state.Running {
		t.Errorf("IsRunning = %+v, want Running=false with no port", state)
	}
	expectNotSignaled(t, exited)
	if _, err := os.Stat(pidPath(dir)); err != nil {
		t.Errorf("expected the pid file to be kept: %v", err)
	}
}

// The schema-migration ownership proof stays strict: a recorded PID recycled
// to another account's process answers EPERM, and must not prove that this
// workspace manages the server on the port.
func TestManagesLiveServerOnPortRejectsPIDItMayNotSignal(t *testing.T) {
	dir := t.TempDir()
	writeTrackedStateForLiveProcess(t, dir, 3307)
	if !ManagesLiveServerOnPort(dir, 3307) {
		t.Fatal("precondition: a live PID this user may signal is proof")
	}
	stubSignalZero(t, func(int) error { return errNotPermitted })
	if ManagesLiveServerOnPort(dir, 3307) {
		t.Error("a PID this user may not signal (EPERM) must not prove ownership")
	}
}

func TestAliveAfterSignalZero(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"signal delivered", nil, true},
		{"bare EPERM", syscall.EPERM, true},
		{"kill EPERM", os.NewSyscallError("kill", syscall.EPERM), true},
		{"pidfd_send_signal EPERM", os.NewSyscallError("pidfd_send_signal", syscall.EPERM), true},
		{"process done", os.ErrProcessDone, false},
		{"kill ESRCH", os.NewSyscallError("kill", syscall.ESRCH), false},
		{"bare ESRCH", syscall.ESRCH, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := aliveAfterSignalZero(tt.err); got != tt.want {
				t.Errorf("aliveAfterSignalZero(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestParseDoltProcessPIDs(t *testing.T) {
	tests := []struct {
		name     string
		snapshot string
		want     []int
	}{
		{
			name:     "ordinary command",
			snapshot: "  101 S /usr/local/bin/dolt sql-server --port 3306\n",
			want:     []int{101},
		},
		{
			name:     "profiled command",
			snapshot: "102 Sl dolt --prof cpu --prof-path /tmp/dolt-pprof sql-server\n",
			want:     []int{102},
		},
		{
			name:     "ordered loose false positive",
			snapshot: "103 S some-tool says dolt then sql-server\n",
			want:     []int{103},
		},
		{
			name:     "sql server before dolt is rejected",
			snapshot: "104 S sql-server then dolt\n",
		},
		{
			name:     "case sensitive command matching",
			snapshot: "105 S Dolt sql-server\n106 S dolt SQL-SERVER\n",
		},
		{
			name:     "zombie and dead states including modifiers are rejected",
			snapshot: "107 Z dolt sql-server\n108 Z+ dolt sql-server\n109 X dolt sql-server\n110 X< dolt sql-server\n",
		},
		{
			name:     "valid state prefix with modifier is accepted",
			snapshot: "111 S+ dolt sql-server\n",
			want:     []int{111},
		},
		{
			name: "malformed and invalid rows are rejected",
			snapshot: "\n" +
				"not-a-pid S dolt sql-server\n" +
				"0 S dolt sql-server\n" +
				"-1 S dolt sql-server\n" +
				"112\n" +
				"113 S\n" +
				"114\tdolt sql-server\n" +
				"115 S \n",
		},
		{
			name:     "preserves source order",
			snapshot: "202 S dolt sql-server\n201 S dolt sql-server\n203 R dolt sql-server\n",
			want:     []int{202, 201, 203},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseDoltProcessPIDs([]byte(tt.snapshot)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseDoltProcessPIDs() = %v, want %v", got, tt.want)
			}
		})
	}
}

// GH#6614: IsRunning backs `bd dolt status`. A confirmed dolt sql-server whose
// port cannot be resolved (no port file, no configured port) is uncertain
// tracking, not proof the server is safe to stop: observing it must neither
// signal the process nor delete its pid file.
func TestIsRunningDoesNotStopConfirmedServerWithUnknownPort(t *testing.T) {
	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	if err := os.Remove(portPath(dir)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
	if got := DefaultConfig(dir).Port; got != 0 {
		t.Fatalf("precondition: DefaultConfig resolved port %d, want 0", got)
	}
	data, err := os.ReadFile(pidPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	// The process list confirms the tracked PID is a dolt sql-server.
	stubProcessList(t, func() ([]int, error) { return []int{pid}, nil })

	state, err := IsRunning(dir)
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if !state.Running || state.PID != pid || state.Port != 0 {
		t.Errorf("IsRunning = %+v, want Running with PID %d and Port 0", state, pid)
	}
	expectNotSignaled(t, exited)
	if _, err := os.Stat(pidPath(dir)); err != nil {
		t.Errorf("expected the pid file to be kept: %v", err)
	}
}

// GH#6614: explicit Stop keeps lifecycle authority over a live server whose
// port cannot be resolved: it stops the confirmed process, cleans tracking
// and succeeds, rather than returning ErrServerNotRunning after IsRunning
// killed the server. The working-set flush is skipped with a warning — there
// is no port to dial.
func TestStopStopsUnknownPortServer(t *testing.T) {
	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	if err := os.Remove(portPath(dir)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
	if got := DefaultConfig(dir).Port; got != 0 {
		t.Fatalf("precondition: DefaultConfig resolved port %d, want 0", got)
	}
	data, err := os.ReadFile(pidPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	stubProcessList(t, func() ([]int, error) { return []int{pid}, nil })

	var stopErr error
	stderr := captureStderr(t, func() { stopErr = Stop(dir) })
	if stopErr != nil {
		t.Fatalf("Stop = %v, want nil for a live server with an unknown port", stopErr)
	}
	if !strings.Contains(stderr, "flush") || !strings.Contains(stderr, "unknown") {
		t.Errorf("expected a warning that the flush is unavailable for an unknown port, stderr:\n%s", stderr)
	}
	select {
	case err := <-exited:
		exited <- err // let the cleanup finish
	case <-time.After(5 * time.Second):
		t.Fatal("the tracked server did not exit after Stop")
	}
	for _, path := range []string{pidPath(dir), portPath(dir)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, stat err = %v", filepath.Base(path), err)
		}
	}
}

// unknownPortServer is the GH#6614 state: tracking names a live, signalable
// process the (stubbed) process list confirms as a dolt sql-server, but no
// port can be resolved for it. The disposable child stands in for the server.
type unknownPortServer struct {
	dir    string
	pid    int
	exited chan error
}

// startUnknownPortServer isolates config and env so DefaultConfig resolves no
// port, writes the tracked state, and removes the port file. The caller
// installs its own process-list stub; the PATH is emptied after the child is
// started so no launch can succeed and a refusal is distinguishable from a
// missing-binary failure.
func startUnknownPortServer(t *testing.T) *unknownPortServer {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	t.Setenv("BEADS_DOLT_REMOTESAPI_PORT", "0")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)

	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	if err := os.Remove(portPath(dir)); err != nil {
		t.Fatal(err)
	}
	if got := DefaultConfig(dir).Port; got != 0 {
		t.Fatalf("precondition: DefaultConfig resolved port %d, want 0", got)
	}
	data, err := os.ReadFile(pidPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // no dolt binary: any launch fails
	return &unknownPortServer{dir: dir, pid: pid, exited: exited}
}

// expectServerAlive fails when the tracked child has exited, without
// consuming the exit value the cleanup needs.
func expectServerAlive(t *testing.T, srv *unknownPortServer) {
	t.Helper()
	select {
	case err := <-srv.exited:
		srv.exited <- err // let the cleanup finish
		t.Errorf("the tracked server was signaled: %v", err)
	default:
	}
}

// GH#6614: with auto-start permitted on an owned workspace, recovery of a
// live unknown-port server happens under the lifecycle lock. While the lock is
// held the tracked server is untouchable (Ensure's lock-free fast path observes
// it and waits); after release the replacement stops it before the launch is
// attempted, which the isolated PATH turns into the missing-dolt failure.
func TestUnknownPortRecoveryReplacesUnderLock(t *testing.T) {
	srv := startUnknownPortServer(t)
	observed := make(chan struct{}, 1)
	calls := 0
	stubProcessList(t, func() ([]int, error) {
		calls++
		if calls == 1 {
			observed <- struct{}{}
		}
		return []int{srv.pid}, nil
	})

	lockF, err := acquireLifecycleLock(srv.dir)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := EnsureRunningDetailed(srv.dir)
		done <- err
	}()
	<-observed // Ensure's fast path has observed the live unknown-port server
	expectServerAlive(t, srv)
	if _, err := os.Stat(pidPath(srv.dir)); err != nil {
		t.Errorf("expected the pid file to be kept while the lock is held: %v", err)
	}
	releaseLifecycleLock(lockF)

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "dolt is not installed") {
			t.Errorf("EnsureRunningDetailed = %v, want the missing-dolt failure reached after the replacement stop", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("EnsureRunningDetailed did not finish after lock release")
	}
	// The tracked server was stopped before the launch was attempted, and
	// its tracking went with it.
	select {
	case err := <-srv.exited:
		srv.exited <- err // let the cleanup finish
	default:
		t.Error("the tracked server was still alive after the replacement attempt")
	}
	for _, path := range []string{pidPath(srv.dir), portPath(srv.dir)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, stat err = %v", filepath.Base(path), err)
		}
	}
}

// GH#6614: a port published while Ensure waits for the lifecycle lock — the
// second half of another start's PID-then-port publication — is adopted once
// the lock is released: the server stays alive, no replacement is attempted.
func TestUnknownPortRecoveryAdoptsPublishedPort(t *testing.T) {
	srv := startUnknownPortServer(t)
	observed := make(chan struct{}, 1)
	calls := 0
	stubProcessList(t, func() ([]int, error) {
		calls++
		if calls == 1 {
			observed <- struct{}{}
		}
		return []int{srv.pid}, nil
	})

	lockF, err := acquireLifecycleLock(srv.dir)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	var port int
	var startedByUs bool
	go func() {
		port, startedByUs, err = EnsureRunningDetailed(srv.dir)
		done <- err
	}()
	<-observed
	if err := writePortFile(srv.dir, 14599); err != nil {
		t.Fatal(err)
	}
	releaseLifecycleLock(lockF)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EnsureRunningDetailed error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("EnsureRunningDetailed did not finish after lock release")
	}
	if port != 14599 || startedByUs {
		t.Errorf("EnsureRunningDetailed = (%d, %v), want (14599, false)", port, startedByUs)
	}
	expectServerAlive(t, srv)
	if _, err := os.Stat(pidPath(srv.dir)); err != nil {
		t.Errorf("expected the pid file to be kept: %v", err)
	}
	if got := readPortFile(srv.dir); got != 14599 {
		t.Errorf("port file = %d, want 14599", got)
	}
}

// GH#6614: a permitted replacement still aborts when the stop fails — the
// tracked server is never left beside a launched competitor, and a stop or
// cleanup error is propagated, not swallowed.
func TestUnknownPortRecoveryAbortsOnStopError(t *testing.T) {
	t.Run("stop refused, no launch", func(t *testing.T) {
		srv := startUnknownPortServer(t)
		// Reads 1-4 (Ensure's fast path, the locked re-check, startLocked's
		// and stopLocked's IsRunning) confirm the dolt server; read 5 is
		// stopLocked's identity re-check, which refuses when the list cannot
		// be read.
		calls := 0
		stubProcessList(t, func() ([]int, error) {
			calls++
			if calls >= 5 {
				return nil, errProcessListUnreadable
			}
			return []int{srv.pid}, nil
		})

		_, _, err := EnsureRunningDetailed(srv.dir)
		if err == nil {
			t.Fatal("EnsureRunningDetailed succeeded despite the refused stop")
		}
		if !strings.Contains(err.Error(), "not stopping PID") || strings.Contains(err.Error(), "dolt is not installed") {
			t.Errorf("EnsureRunningDetailed = %v, want the stop refusal propagated without a launch", err)
		}
		expectServerAlive(t, srv)
		if _, err := os.Stat(pidPath(srv.dir)); err != nil {
			t.Errorf("expected the pid file to be kept: %v", err)
		}
	})

	t.Run("cleanup error propagates, no launch", func(t *testing.T) {
		srv := startUnknownPortServer(t)
		stubProcessList(t, func() ([]int, error) { return []int{srv.pid}, nil })
		// The lock file must exist before the directory becomes unwritable.
		lockF, err := acquireLifecycleLock(srv.dir)
		if err != nil {
			t.Fatal(err)
		}
		releaseLifecycleLock(lockF)
		if err := os.Chmod(srv.dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(srv.dir, 0o755) })

		_, _, err = EnsureRunningDetailed(srv.dir)
		if err == nil {
			t.Fatal("EnsureRunningDetailed succeeded despite the cleanup failure")
		}
		if !strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "dolt is not installed") {
			t.Errorf("EnsureRunningDetailed = %v, want the cleanup failure propagated without a launch", err)
		}
		// The stop itself succeeded (the server exited) but its cleanup could
		// not remove the tracking, and the failure was not discarded.
		select {
		case err := <-srv.exited:
			srv.exited <- err // let the cleanup finish
		case <-time.After(5 * time.Second):
			t.Fatal("the tracked server did not exit during the replacement stop")
		}
		if _, err := os.Stat(pidPath(srv.dir)); err != nil {
			t.Errorf("expected the unremovable pid file to remain: %v", err)
		}
	})
}
