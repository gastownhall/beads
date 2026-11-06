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
	"sync"
	"sync/atomic"
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

// With no port recorded or configured, IsRunning stops a listed dolt
// sql-server for a restart, but not one this user may not signal (EPERM): no
// signal would reach it. It reports the server not running and keeps the pid
// file, which a start replaces.
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

// startUnknownPortServer tracks a live child as a confirmed dolt sql-server
// whose port no source resolves: no port file, no configured port, no shared
// server. It returns the child's exit channel and PID.
func startUnknownPortServer(t *testing.T, dir string) (chan error, int) {
	t.Helper()
	exited := startStaleServerFiles(t, dir)
	if err := os.Remove(portPath(dir)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")
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
	return exited, pid
}

// assertUnknownPortTrackingKept checks that the pid file still names pid and
// that no port file was written.
func assertUnknownPortTrackingKept(t *testing.T, dir string, pid int) {
	t.Helper()
	data, err := os.ReadFile(pidPath(dir))
	if err != nil {
		t.Errorf("expected the pid file to be kept: %v", err)
	} else if got := strings.TrimSpace(string(data)); got != strconv.Itoa(pid) {
		t.Errorf("pid file = %q, want %d", got, pid)
	}
	if _, err := os.Stat(portPath(dir)); !os.IsNotExist(err) {
		t.Errorf("expected no port file, stat err = %v", err)
	}
}

// GH#6614: IsRunning backs `bd dolt status`. A confirmed dolt sql-server whose
// port cannot be resolved is uncertain tracking, not proof the server is safe
// to stop: observing it reports the live PID with port zero, and neither
// signals the process nor deletes its pid file.
func TestIsRunningReportsUnknownPortServerWithoutSignaling(t *testing.T) {
	t.Run("no port file", func(t *testing.T) {
		dir := t.TempDir()
		exited, pid := startUnknownPortServer(t, dir)

		state, err := IsRunning(dir)
		if err != nil {
			t.Fatalf("IsRunning error: %v", err)
		}
		if !state.Running || state.PID != pid || state.Port != 0 {
			t.Errorf("IsRunning = %+v, want Running with PID %d and port 0", state, pid)
		}
		expectNotSignaled(t, exited)
		assertUnknownPortTrackingKept(t, dir, pid)
	})

	t.Run("malformed port file", func(t *testing.T) {
		dir := t.TempDir()
		exited, pid := startUnknownPortServer(t, dir)
		if err := os.WriteFile(portPath(dir), []byte("not-a-port"), 0600); err != nil {
			t.Fatal(err)
		}

		state, err := IsRunning(dir)
		if err != nil {
			t.Fatalf("IsRunning error: %v", err)
		}
		if !state.Running || state.PID != pid || state.Port != 0 {
			t.Errorf("IsRunning = %+v, want Running with PID %d and port 0", state, pid)
		}
		expectNotSignaled(t, exited)
		if data, err := os.ReadFile(portPath(dir)); err != nil || string(data) != "not-a-port" {
			t.Errorf("port file = %q, %v; want it kept unchanged", data, err)
		}
	})

	t.Run("configured port resolves", func(t *testing.T) {
		dir := t.TempDir()
		exited, pid := startUnknownPortServer(t, dir)
		t.Setenv("BEADS_DOLT_SERVER_PORT", "14601")

		state, err := IsRunning(dir)
		if err != nil {
			t.Fatalf("IsRunning error: %v", err)
		}
		if !state.Running || state.PID != pid || state.Port != 14601 {
			t.Errorf("IsRunning = %+v, want Running with PID %d and port 14601", state, pid)
		}
		expectNotSignaled(t, exited)
		assertUnknownPortTrackingKept(t, dir, pid)
	})
}

// isolateLifecycleConfig gives the test its own HOME and a fresh config, so
// dolt.auto-start and BEADS_DOLT_SERVER_MODE come only from the test.
func isolateLifecycleConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Chdir(t.TempDir())
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
}

// GH#6614: where bd does not manage the server's lifecycle (auto-start off,
// or the server declared external), a live dolt server with no resolvable
// port is neither adopted at port 0 nor replaced: Start and EnsureRunning
// refuse, naming the PID and the missing port, and leave the process and its
// files alone.
func TestUnknownPortRecoveryRefusesUnmanagedServer(t *testing.T) {
	policies := []struct {
		name  string
		apply func(t *testing.T)
	}{
		{"env auto-start off", func(t *testing.T) { t.Setenv("BEADS_DOLT_AUTO_START", "0") }},
		{"config auto-start off", func(t *testing.T) {
			config.Set("dolt.auto-start", "false")
			t.Cleanup(func() { config.Set("dolt.auto-start", "") })
		}},
		{"external server mode", func(t *testing.T) { t.Setenv("BEADS_DOLT_SERVER_MODE", "1") }},
	}
	entries := []struct {
		name string
		call func(dir string) error
	}{
		{"Start", func(dir string) error { _, err := Start(dir); return err }},
		{"EnsureRunningDetailed", func(dir string) error { _, _, err := EnsureRunningDetailed(dir); return err }},
	}
	for _, policy := range policies {
		for _, entry := range entries {
			t.Run(policy.name+"/"+entry.name, func(t *testing.T) {
				isolateLifecycleConfig(t)
				dir := t.TempDir()
				exited, pid := startUnknownPortServer(t, dir)
				policy.apply(t)
				if policy.name == "external server mode" {
					if mode := ResolveServerMode(dir); mode != ServerModeExternal {
						t.Fatalf("precondition: server mode %v, want external", mode)
					}
				} else if !IsAutoStartDisabled() {
					t.Fatal("precondition: auto-start is not disabled")
				}
				t.Setenv("PATH", t.TempDir()) // no dolt binary: a launch would fail

				err := entry.call(dir)
				if err == nil {
					t.Fatal("succeeded against a server whose port is unknown")
				}
				msg := err.Error()
				if strings.Contains(msg, "not installed") {
					t.Fatalf("reached the launch instead of refusing: %v", err)
				}
				for _, want := range []string{"PID " + strconv.Itoa(pid), "port is unknown"} {
					if !strings.Contains(msg, want) {
						t.Errorf("error does not mention %q: %v", want, err)
					}
				}
				expectNotSignaled(t, exited)
				assertUnknownPortTrackingKept(t, dir, pid)
			})
		}
	}
}

// observeFastPath re-stubs the process list (still confirming pid) so the
// returned channel closes on the first read: EnsureRunningDetailed's
// lock-free IsRunning has then observed the unknown-port server.
func observeFastPath(t *testing.T, pid int) <-chan struct{} {
	t.Helper()
	observed := make(chan struct{})
	var once sync.Once
	stubProcessList(t, func() ([]int, error) {
		once.Do(func() { close(observed) })
		return []int{pid}, nil
	})
	return observed
}

// ensureUnderHeldLock starts EnsureRunningDetailed while the test holds the
// lifecycle lock and returns once its fast path has observed the server.
func ensureUnderHeldLock(t *testing.T, dir string, observed <-chan struct{}) (*os.File, chan ensureResult) {
	t.Helper()
	lockF, err := acquireLifecycleLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseLifecycleLock(lockF) })
	done := make(chan ensureResult, 1)
	go func() {
		port, startedByUs, err := EnsureRunningDetailed(dir)
		done <- ensureResult{port, startedByUs, err}
	}()
	select {
	case <-observed:
	case r := <-done:
		t.Fatalf("EnsureRunningDetailed returned before taking the lock: %+v", r)
	case <-time.After(10 * time.Second):
		t.Fatal("EnsureRunningDetailed never read the process list")
	}
	return lockF, done
}

type ensureResult struct {
	port        int
	startedByUs bool
	err         error
}

func awaitEnsure(t *testing.T, done chan ensureResult) ensureResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(30 * time.Second):
		t.Fatal("EnsureRunningDetailed did not return after the lock was released")
		return ensureResult{}
	}
}

// GH#6614: with auto-start enabled in an owned workspace, a live server whose
// port is unknown is replaced, but only under the lifecycle lock: the
// lock-free fast path leaves it running, and the locked start path stops it
// before going on to launch a replacement (here, failing for want of a dolt
// binary).
func TestUnknownPortRecoveryReplacesUnderLock(t *testing.T) {
	t.Run("EnsureRunningDetailed", func(t *testing.T) {
		isolateLifecycleConfig(t)
		dir := t.TempDir()
		exited, pid := startUnknownPortServer(t, dir)
		observed := observeFastPath(t, pid)
		t.Setenv("PATH", t.TempDir()) // no dolt binary: the launch fails

		lockF, done := ensureUnderHeldLock(t, dir, observed)
		expectNotSignaled(t, exited)
		assertUnknownPortTrackingKept(t, dir, pid)
		select {
		case r := <-done:
			t.Fatalf("EnsureRunningDetailed returned while the lock was held: %+v", r)
		default:
		}
		releaseLifecycleLock(lockF)

		r := awaitEnsure(t, done)
		if r.err == nil || !strings.Contains(r.err.Error(), "not installed") {
			t.Errorf("EnsureRunningDetailed = %+v, want the launch's missing-binary error", r)
		}
		expectStopped(t, exited)
		assertStateFilesRemoved(t, dir)
	})

	t.Run("Start", func(t *testing.T) {
		isolateLifecycleConfig(t)
		dir := t.TempDir()
		exited, _ := startUnknownPortServer(t, dir)
		t.Setenv("PATH", t.TempDir()) // no dolt binary: the launch fails

		if _, err := Start(dir); err == nil || !strings.Contains(err.Error(), "not installed") {
			t.Errorf("Start = %v, want the launch's missing-binary error", err)
		}
		expectStopped(t, exited)
		assertStateFilesRemoved(t, dir)
	})
}

// A port published while EnsureRunningDetailed waits for the lock (the
// starting process wrote the pid file, then the port file) is adopted: the
// server is not stopped and bd does not count it as started by this call.
func TestUnknownPortRecoveryAdoptsPublishedPort(t *testing.T) {
	isolateLifecycleConfig(t)
	t.Setenv("BEADS_DOLT_REMOTESAPI_PORT", "")
	dir := t.TempDir()
	exited, pid := startUnknownPortServer(t, dir)
	observed := observeFastPath(t, pid)
	t.Setenv("PATH", t.TempDir()) // no dolt binary: a launch would fail

	lockF, done := ensureUnderHeldLock(t, dir, observed)
	if err := writePortFile(dir, 14602); err != nil {
		t.Fatal(err)
	}
	releaseLifecycleLock(lockF)

	r := awaitEnsure(t, done)
	if r.err != nil || r.port != 14602 || r.startedByUs {
		t.Errorf("EnsureRunningDetailed = %+v, want port 14602 adopted, not started by us", r)
	}
	expectNotSignaled(t, exited)
	if got := readPortFile(dir); got != 14602 {
		t.Errorf("port file = %d, want 14602", got)
	}
}

// When stopping the unknown-port server fails, the start path stops there: it
// neither adopts port 0 nor launches a replacement, and it does not discard a
// stop error that is joined with ErrServerNotRunning.
func TestUnknownPortRecoveryAbortsOnStopError(t *testing.T) {
	// Start reads the process list in startLocked's IsRunning (1), then in
	// stopLocked's IsRunning (2) and stopLocked's own verification (3).
	for _, tc := range []struct {
		name       string
		failFrom   int64
		wantErr    string
		wantPIDRef bool
	}{
		// stopLocked cannot confirm the PID is dolt, so it refuses to signal it.
		{name: "verification fails", failFrom: 3, wantErr: "not stopping PID", wantPIDRef: true},
		// stopLocked's IsRunning no longer sees a running server: it cleans up
		// and returns ErrServerNotRunning.
		{name: "server no longer seen", failFrom: 2, wantErr: ErrServerNotRunning.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateLifecycleConfig(t)
			dir := t.TempDir()
			exited, pid := startUnknownPortServer(t, dir)
			var reads atomic.Int64
			stubProcessList(t, func() ([]int, error) {
				if reads.Add(1) >= tc.failFrom {
					return nil, errProcessListUnreadable
				}
				return []int{pid}, nil
			})
			t.Setenv("PATH", t.TempDir()) // no dolt binary: a launch would fail

			state, err := Start(dir)
			if err == nil {
				t.Fatalf("Start = %+v, want the stop error", state)
			}
			if strings.Contains(err.Error(), "not installed") {
				t.Fatalf("Start went on to launch after the stop failed: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Start = %v, want an error containing %q", err, tc.wantErr)
			}
			expectNotSignaled(t, exited)
			if tc.wantPIDRef {
				assertUnknownPortTrackingKept(t, dir, pid)
			}
		})
	}
}

// expectStopped waits for the child to exit after being signaled.
func expectStopped(t *testing.T, exited chan error) {
	t.Helper()
	select {
	case err := <-exited:
		exited <- err // let the cleanup finish
	case <-time.After(10 * time.Second):
		t.Error("the server was not stopped")
	}
}

func assertStateFilesRemoved(t *testing.T, dir string) {
	t.Helper()
	for _, path := range []string{pidPath(dir), portPath(dir)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, stat err = %v", filepath.Base(path), err)
		}
	}
}
