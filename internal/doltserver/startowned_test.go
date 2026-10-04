//go:build !windows

package doltserver

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These names match fakedolt_test.go, which TestMain dispatches to.
const (
	testFakeDoltEnv         = "BEADS_TEST_FAKE_DOLT"
	testFakeDoltDelayEnv    = "BEADS_TEST_FAKE_DOLT_DELAY"
	testFakeDoltInUseEnv    = "BEADS_TEST_FAKE_DOLT_INUSE_PORTS"
	testFakeDoltExitEnv     = "BEADS_TEST_FAKE_DOLT_EXIT"
	testFakeDoltReadyEnv    = "BEADS_TEST_FAKE_DOLT_READY"
	testFakeDoltLaunchesEnv = "BEADS_TEST_FAKE_DOLT_LAUNCHES"
	// testFakeDoltDelay is the fake dolt's startup time. It is longer than
	// the 200ms "did it exit immediately?" check Start used to rely on, which
	// is the window that let a foreign listener's greeting pass as ready.
	testFakeDoltDelay = "700ms"
)

// foreignGreeter listens on a free loopback port and greets every connection
// the way a MySQL server does, standing in for another process (another dolt)
// that took the port Start chose.
func foreignGreeter(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("\x0a5.7.9-foreign\x00"))
			_ = c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// startFakeSQLServer launches this test binary as a fake `dolt sql-server -P
// port` (see fakeDolt) writing to logPath, and returns it with the log offset
// its output starts at.
func startFakeSQLServer(t *testing.T, port int, logPath string, extraEnv ...string) (*startedServer, int64) {
	return startFakeSQLServerDelay(t, port, logPath, testFakeDoltDelay, extraEnv...)
}

func startFakeSQLServerDelay(t *testing.T, port int, logPath, delay string, extraEnv ...string) (*startedServer, int64) {
	t.Helper()
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	_, _ = logFile.WriteString("earlier run: Port " + fmt.Sprint(port+1) + " already in use.\n")
	cmd := exec.Command(os.Args[0], "sql-server", "-H", "127.0.0.1", "-P", fmt.Sprint(port), "--loglevel=warning")
	cmd.Env = append(append(os.Environ(), testFakeDoltEnv+"=1", testFakeDoltDelayEnv+"="+delay), extraEnv...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	off := logSize(logFile)
	srv, err := launchServer(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.killAndWait)
	return srv, off
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	p, err := allocateEphemeralPort("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestAwaitOwnedListener_ForeignListenerIsNotReady: when another process
// greets on the port before the launched server reaches its bind, the wait
// must not report ready; it ends with ErrPortInUse (at once when ownership
// is proven foreign, else once the child says so).
// The old check (greeting only, after a 200ms exit check) accepted the
// foreign greeting immediately.
func TestAwaitOwnedListener_ForeignListenerIsNotReady(t *testing.T) {
	for _, tc := range []struct {
		name  string
		owner func(pid, port int) (bool, bool)
	}{
		// The platform check; on Linux this is the real /proc lookup.
		{name: "platform ownership check", owner: nil},
		// Where ownership is unknown, the child's own port-in-use report
		// still ends the wait, as long as it arrives before a greeting is
		// accepted. Model a known-not-owned answer so the test is
		// deterministic on every unix.
		{name: "ownership says foreign", owner: func(int, int) (bool, bool) { return false, true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.owner == nil && runtime.GOOS != "linux" {
				t.Skip("listener ownership is only provable on linux")
			}
			port := foreignGreeter(t)
			logPath := filepath.Join(t.TempDir(), "dolt-server.log")
			srv, off := startFakeSQLServer(t, port, logPath)

			err := awaitOwnedListener(srv, startupProbe{
				host: "127.0.0.1", port: port, logPath: logPath, logOffset: off,
				timeout: 20 * time.Second, owner: tc.owner,
			})
			if !errors.Is(err, ErrPortInUse) {
				t.Fatalf("awaitOwnedListener = %v, want ErrPortInUse (a foreign greeting must not count as ready)", err)
			}
		})
	}
}

// TestAwaitOwnedListener_ImmediatePortInUseExit pins the fast-exit race the
// proxied launcher hit (#7184's follow-up): a dolt that finds its port taken
// can exit within milliseconds, before Start looks at it at all. Start runs
// nothing fallible between launching the child and this wait, and the wait
// drains the child's log after seeing it exit, so the exit is still
// classified as ErrPortInUse (recoverable), never as a generic failure.
func TestAwaitOwnedListener_ImmediatePortInUseExit(t *testing.T) {
	for i := 0; i < 5; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0") // mute holder, like the port hog
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		logPath := filepath.Join(t.TempDir(), "dolt-server.log")
		srv, off := startFakeSQLServerDelay(t, port, logPath, "0s")
		<-srv.exited // the child is gone before the wait starts
		err = awaitOwnedListener(srv, startupProbe{
			host: "127.0.0.1", port: port, logPath: logPath, logOffset: off, timeout: 20 * time.Second,
		})
		_ = ln.Close()
		if !errors.Is(err, ErrPortInUse) {
			t.Fatalf("attempt %d: awaitOwnedListener = %v, want ErrPortInUse", i, err)
		}
	}
}

// TestAwaitOwnedListener_OwnListenerIsReady is the positive control: the
// launched server's own listener is accepted, and an earlier run's
// port-in-use line in the same log (before the offset) is ignored.
func TestAwaitOwnedListener_OwnListenerIsReady(t *testing.T) {
	port := freeLoopbackPort(t)
	logPath := filepath.Join(t.TempDir(), "dolt-server.log")
	srv, off := startFakeSQLServer(t, port, logPath)
	if err := awaitOwnedListener(srv, startupProbe{
		host: "127.0.0.1", port: port, logPath: logPath, logOffset: off, timeout: 20 * time.Second,
	}); err != nil {
		t.Fatalf("awaitOwnedListener on the child's own listener: %v", err)
	}
	if runtime.GOOS == "linux" {
		if owned, known := listenerOwnership(srv.pid, port); !owned || !known {
			t.Errorf("listenerOwnership(child) = (%v, %v), want (true, true)", owned, known)
		}
		other := foreignGreeter(t)
		if owned, known := listenerOwnership(srv.pid, other); owned || !known {
			t.Errorf("listenerOwnership(child, foreign port) = (%v, %v), want (false, true)", owned, known)
		}
	}
}

// TestAwaitOwnedListener_ChildExitIsReported: a child that exits without a
// port conflict ends the wait promptly instead of running out the timeout.
// The old check could not see this at all past its first 200ms: the child
// was released unreaped, and a zombie still answers kill(pid, 0).
func TestAwaitOwnedListener_ChildExitIsReported(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "dolt-server.log")
	cmd := exec.Command(os.Args[0], "sql-server") // no port: the fake exits 2
	cmd.Env = append(os.Environ(), testFakeDoltEnv+"=1")
	srv, err := launchServer(cmd)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = awaitOwnedListener(srv, startupProbe{
		host: "127.0.0.1", port: freeLoopbackPort(t), logPath: logPath, timeout: 20 * time.Second,
	})
	if err == nil || errors.Is(err, ErrPortInUse) || !strings.Contains(err.Error(), "exited before accepting connections") {
		t.Fatalf("awaitOwnedListener = %v, want an exited-before-ready error", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("exit took %s to notice", time.Since(start))
	}
}

// TestAwaitOwnedListener_ProvenForeignEndsWaitAtOnce: when /proc proves the
// greeting came from another process, the wait does not sit out a slow
// child's startup (which under load can outlast the ready timeout and turn a
// movable port into a hard failure); it returns ErrPortInUse straight away.
func TestAwaitOwnedListener_ProvenForeignEndsWaitAtOnce(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("listener ownership is only provable on linux")
	}
	port := foreignGreeter(t)
	logPath := filepath.Join(t.TempDir(), "dolt-server.log")
	srv, off := startFakeSQLServerDelay(t, port, logPath, "60s")
	start := time.Now()
	err := awaitOwnedListener(srv, startupProbe{
		host: "127.0.0.1", port: port, logPath: logPath, logOffset: off, timeout: 30 * time.Second,
	})
	if !errors.Is(err, ErrPortInUse) {
		t.Fatalf("awaitOwnedListener = %v, want ErrPortInUse", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %s; a proven-foreign listener must end the wait without waiting for the child", d)
	}
}

// TestAwaitOwnedListener_ReadsLogBeforeExitIsSeen: the child's port-in-use
// line counts as soon as it is in the log, even before bd has observed the
// child's exit (the reaper goroutine can lag it under load). Here the
// "child" never exits at all.
func TestAwaitOwnedListener_ReadsLogBeforeExitIsSeen(t *testing.T) {
	port := foreignGreeter(t)
	logPath := filepath.Join(t.TempDir(), "dolt-server.log")
	if err := os.WriteFile(logPath, []byte(fmt.Sprintf("Port %d already in use.\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := &startedServer{pid: os.Getpid(), exited: make(chan struct{})}
	err := awaitOwnedListener(srv, startupProbe{
		host: "127.0.0.1", port: port, logPath: logPath, timeout: 5 * time.Second,
		owner: func(int, int) (bool, bool) { return false, false }, // unknown: the greeting alone would pass
	})
	if !errors.Is(err, ErrPortInUse) {
		t.Fatalf("awaitOwnedListener = %v, want ErrPortInUse", err)
	}
}

// TestAwaitOwnedListener_ReadyLine covers the debug-mode proof: with a log
// level that emits dolt's ready line, the line proves ownership when /proc
// cannot, and without it an unproven greeting is never accepted.
func TestAwaitOwnedListener_ReadyLine(t *testing.T) {
	unknown := func(int, int) (bool, bool) { return false, false }
	t.Run("ready line proves ownership", func(t *testing.T) {
		port := freeLoopbackPort(t)
		logPath := filepath.Join(t.TempDir(), "dolt-server.log")
		srv, off := startFakeSQLServer(t, port, logPath, testFakeDoltReadyEnv+"=1")
		if err := awaitOwnedListener(srv, startupProbe{
			host: "127.0.0.1", port: port, logPath: logPath, logOffset: off,
			readyLineLogged: true, timeout: 20 * time.Second, owner: unknown,
		}); err != nil {
			t.Fatalf("awaitOwnedListener with the ready line logged: %v", err)
		}
	})
	t.Run("no ready line, unknown ownership: not accepted", func(t *testing.T) {
		port := freeLoopbackPort(t)
		logPath := filepath.Join(t.TempDir(), "dolt-server.log")
		srv, off := startFakeSQLServerDelay(t, port, logPath, "0s")
		err := awaitOwnedListener(srv, startupProbe{
			host: "127.0.0.1", port: port, logPath: logPath, logOffset: off,
			readyLineLogged: true, timeout: 8 * time.Second, owner: unknown,
		})
		if err == nil || !strings.Contains(err.Error(), DoltReadyLine) {
			t.Fatalf("awaitOwnedListener = %v, want a timeout naming the missing ready line", err)
		}
	})
}

// installFakeDolt puts a `dolt` on PATH that runs this test binary as
// fakeDolt.
func installFakeDolt(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	shim := "#!/bin/sh\nexec " + shellQuote(os.Args[0]) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "dolt"), []byte(shim), 0o700); err != nil { //nolint:gosec // G306: the shim must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(testFakeDoltEnv, "1")
	t.Setenv(testFakeDoltDelayEnv, testFakeDoltDelay)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fakeStartWorkspace prepares a per-project workspace for the real Start
// with the fake dolt on PATH and the user config isolated, and returns its
// beads dir and the file the fake records each launch's port in.
func fakeStartWorkspace(t *testing.T) (beadsDir, launches string) {
	t.Helper()
	installFakeDolt(t)
	isolateUserConfig(t)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_READY_TIMEOUT", "20")
	launches = filepath.Join(t.TempDir(), "launches")
	t.Setenv(testFakeDoltLaunchesEnv, launches)
	beadsDir = filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	return beadsDir, launches
}

func launchedPorts(t *testing.T, launches string) []string {
	t.Helper()
	b, err := os.ReadFile(launches)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

// startForTest runs Start and makes sure whatever it left running dies with
// the test.
func startForTest(t *testing.T, beadsDir string) (*State, error) {
	t.Helper()
	state, err := Start(beadsDir)
	if state != nil && state.PID > 0 {
		pid := state.PID
		t.Cleanup(func() {
			if p, findErr := os.FindProcess(pid); findErr == nil {
				_ = p.Kill()
			}
		})
	}
	return state, err
}

func assertNoStateFiles(t *testing.T, beadsDir string) {
	t.Helper()
	for _, p := range []string{pidPath(beadsDir), portPath(beadsDir)} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s left behind after a failed start", filepath.Base(p))
		}
	}
}

// TestStart_RecoversWhenEphemeralPortIsTaken drives the real Start with a
// fake dolt whose first ephemeral port is held by a foreign greeter that took
// it between allocation and bind. Start must not adopt the foreign listener;
// it must move to a fresh port and come up there. Before the fix it returned
// success on the foreign port.
func TestStart_RecoversWhenEphemeralPortIsTaken(t *testing.T) {
	beadsDir, launches := fakeStartWorkspace(t)
	if cfg := DefaultConfig(beadsDir); cfg.Port != 0 || cfg.Mode != ServerModeOwned {
		t.Fatalf("workspace resolves port %d (%s), mode %v; want the ephemeral owned path", cfg.Port, cfg.PortSource, cfg.Mode)
	}
	foreign := foreignGreeter(t)

	var calls atomic.Int32
	orig := allocateEphemeralPort
	allocateEphemeralPort = func(host string) (int, error) {
		if calls.Add(1) == 1 {
			return foreign, nil
		}
		return orig(host)
	}
	t.Cleanup(func() { allocateEphemeralPort = orig })

	state, err := startForTest(t, beadsDir)
	if err != nil {
		log, _ := os.ReadFile(logPath(beadsDir))
		t.Fatalf("Start: %v\nlog:\n%s", err, log)
	}
	if state.Port == foreign {
		t.Fatalf("Start adopted port %d, which a foreign process holds", foreign)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("allocateEphemeralPort called %d times, want 2 (the taken port, then a fresh one)", got)
	}
	// On Linux the first child can be killed before it records its launch:
	// /proc proves the port foreign as soon as the greeting arrives.
	if got := launchedPorts(t, launches); len(got) == 0 || got[len(got)-1] != fmt.Sprint(state.Port) {
		t.Errorf("launches = %v, want the last one on the final port %d", got, state.Port)
	}
	if got := readPortFile(beadsDir); got != state.Port {
		t.Errorf("port file = %d, want %d", got, state.Port)
	}
	if runtime.GOOS == "linux" {
		if owned, known := listenerOwnership(state.PID, state.Port); !owned || !known {
			t.Errorf("listener on %d is not owned by the started server (PID %d)", state.Port, state.PID)
		}
	}
}

// TestStart_RecordsServerWhileStarting: the PID and port files exist, with
// the real port, while Start waits for the server. A bd interrupted during
// that wait must leave the server findable by the next bd, bd dolt stop and
// killall, since it holds the database lock.
func TestStart_RecordsServerWhileStarting(t *testing.T) {
	beadsDir, _ := fakeStartWorkspace(t)
	t.Setenv(testFakeDoltDelayEnv, "3s")

	type seen struct{ pid, port int }
	during := make(chan seen, 1)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			b, err := os.ReadFile(pidPath(beadsDir))
			if err != nil {
				continue
			}
			var pid int
			_, _ = fmt.Sscan(string(b), &pid)
			if port := readPortFile(beadsDir); pid > 0 && port > 0 {
				during <- seen{pid, port}
				return
			}
		}
	}()
	state, err := startForTest(t, beadsDir)
	close(stop)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case got := <-during:
		if got.pid != state.PID || got.port != state.Port {
			t.Errorf("state files during startup = pid %d port %d, want pid %d port %d", got.pid, got.port, state.PID, state.Port)
		}
	default:
		t.Fatal("no PID+port files appeared while Start waited for the server")
	}
}

// TestStart_RemovesStateFilesAfterFailedStart: a server that never comes up
// is killed and its PID and port files are removed, on the notReady path.
func TestStart_RemovesStateFilesAfterFailedStart(t *testing.T) {
	beadsDir, _ := fakeStartWorkspace(t)
	t.Setenv(testFakeDoltDelayEnv, "1s")
	t.Setenv(testFakeDoltExitEnv, "3")

	sawFiles := make(chan struct{}, 1)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			if _, err := os.Stat(pidPath(beadsDir)); err == nil && readPortFile(beadsDir) > 0 {
				sawFiles <- struct{}{}
				return
			}
		}
	}()
	_, err := startForTest(t, beadsDir)
	close(stop)
	if err == nil || !strings.Contains(err.Error(), "exited before accepting connections") {
		t.Fatalf("Start = %v, want an exited-before-ready failure", err)
	}
	select {
	case <-sawFiles:
	default:
		t.Error("state files never appeared during the failed attempt")
	}
	assertNoStateFiles(t, beadsDir)
}

// TestStart_PinnedPortIsNotMoved: an operator-configured port that another
// process takes after Start's pre-check fails with ErrPortInUse after a
// single launch, names where the port came from, and leaves no state files.
func TestStart_PinnedPortIsNotMoved(t *testing.T) {
	beadsDir, launches := fakeStartWorkspace(t)
	port := freeLoopbackPort(t)
	t.Setenv("BEADS_DOLT_SERVER_PORT", fmt.Sprint(port))
	t.Setenv(testFakeDoltInUseEnv, "all") // the holder appears after reclaimPort

	_, err := startForTest(t, beadsDir)
	if !errors.Is(err, ErrPortInUse) {
		t.Fatalf("Start = %v, want ErrPortInUse", err)
	}
	if got := launchedPorts(t, launches); len(got) != 1 || got[0] != fmt.Sprint(port) {
		t.Errorf("launches = %v, want exactly one, on the pinned port %d", got, port)
	}
	for _, want := range []string{"Environment variable (BEADS_DOLT_SERVER_PORT)", "bd dolt set port"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
	assertNoStateFiles(t, beadsDir)
}

// TestStart_PortFilePortMovesOutsideSharedMode: bd's own port-file record is
// a port bd chose, so outside shared-server mode it moves like an ephemeral
// one when another process holds it (GH#4052).
func TestStart_PortFilePortMovesOutsideSharedMode(t *testing.T) {
	beadsDir, launches := fakeStartWorkspace(t)
	recorded := freeLoopbackPort(t)
	if err := writePortFile(beadsDir, recorded); err != nil {
		t.Fatal(err)
	}
	if cfg := DefaultConfig(beadsDir); cfg.Port != recorded || cfg.PortSource != PortSourcePortFile {
		t.Fatalf("DefaultConfig = port %d source %q, want %d from the port file", cfg.Port, cfg.PortSource, recorded)
	}
	t.Setenv(testFakeDoltInUseEnv, fmt.Sprint(recorded))

	state, err := startForTest(t, beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if state.Port == recorded {
		t.Fatalf("Start stayed on the taken port %d", recorded)
	}
	if got := launchedPorts(t, launches); len(got) != 2 || got[0] != fmt.Sprint(recorded) {
		t.Errorf("launches = %v, want the recorded port %d and then a fresh one", got, recorded)
	}
	if got := readPortFile(beadsDir); got != state.Port {
		t.Errorf("port file = %d, want the new port %d", got, state.Port)
	}
}

// TestPinnedPortRemedy pins the wording per port source.
func TestPinnedPortRemedy(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		src  PortSource
		want []string
	}{
		{PortSourceEnv, []string{"BEADS_DOLT_SERVER_PORT", "bd dolt set port"}},
		{PortSourceConfigYaml, []string{"dolt.port", "bd dolt set port"}},
		{PortSourceCallerExplicit, []string{"--server-port"}},
		{PortSourceSharedServerDefault, []string{"shared-server default port", "BEADS_DOLT_SERVER_PORT"}},
		{PortSourcePortFile, []string{"shared server's recorded port", portPath(dir)}},
	} {
		got := pinnedPortRemedy(&Config{PortSource: tc.src}, dir, 3308)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q does not mention %q", tc.src, got, w)
			}
		}
	}
}
