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
	testFakeDoltEnv      = "BEADS_TEST_FAKE_DOLT"
	testFakeDoltDelayEnv = "BEADS_TEST_FAKE_DOLT_DELAY"
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
func startFakeSQLServer(t *testing.T, port int, logPath string) (*startedServer, int64) {
	return startFakeSQLServerDelay(t, port, logPath, testFakeDoltDelay)
}

func startFakeSQLServerDelay(t *testing.T, port int, logPath, delay string) (*startedServer, int64) {
	t.Helper()
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	_, _ = logFile.WriteString("earlier run: Port " + fmt.Sprint(port+1) + " already in use.\n")
	cmd := exec.Command(os.Args[0], "sql-server", "-H", "127.0.0.1", "-P", fmt.Sprint(port), "--loglevel=warning")
	cmd.Env = append(os.Environ(), testFakeDoltEnv+"=1", testFakeDoltDelayEnv+"="+delay)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	off := logSize(logFile)
	srv, err := launchServer(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.kill()
		<-srv.exited
	})
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
// must not report ready; it ends with ErrPortInUse once the child says so.
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

// TestStart_RecoversWhenEphemeralPortIsTaken drives the real Start with a
// fake dolt whose first ephemeral port is held by a foreign greeter that took
// it between allocation and bind. Start must not adopt the foreign listener;
// it must move to a fresh port and come up there. Before the fix it returned
// success on the foreign port.
func TestStart_RecoversWhenEphemeralPortIsTaken(t *testing.T) {
	installFakeDolt(t)
	t.Setenv("BEADS_DOLT_READY_TIMEOUT", "20")
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

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Integration runs do not isolate HOME. Never let this test reach a
	// shared or explicitly configured server: it must take the per-project
	// ephemeral path or not run at all.
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	if IsSharedServerMode() {
		t.Skip("operator config enables dolt.shared-server")
	}
	if cfg := DefaultConfig(beadsDir); cfg.Port != 0 || cfg.Mode != ServerModeOwned {
		t.Skipf("workspace resolves port %d (%s), mode %v; this test needs the ephemeral owned path", cfg.Port, cfg.PortSource, cfg.Mode)
	}
	state, err := Start(beadsDir)
	if state != nil && state.PID > 0 {
		pid := state.PID
		t.Cleanup(func() {
			if p, findErr := os.FindProcess(pid); findErr == nil {
				_ = p.Kill()
			}
		})
	}
	if err != nil {
		log, _ := os.ReadFile(logPath(beadsDir))
		t.Fatalf("Start: %v\nlog:\n%s", err, log)
	}
	if state.Port == foreign {
		t.Fatalf("Start adopted port %d, which a foreign process holds", foreign)
	}
	if got := calls.Load(); got < 2 {
		t.Errorf("allocateEphemeralPort called %d times, want a second port after the conflict", got)
	}
	if got := readPortFile(beadsDir); got != state.Port {
		t.Errorf("port file = %d, want %d", got, state.Port)
	}
	if runtime.GOOS == "linux" {
		if owned, known := listenerOwnership(state.PID, state.Port); !owned || !known {
			t.Errorf("listener on %d is not owned by the started server (PID %d)", state.Port, state.PID)
		}
	}
	log, _ := os.ReadFile(logPath(beadsDir))
	if !strings.Contains(string(log), fmt.Sprintf("Port %d already in use.", foreign)) {
		t.Errorf("log does not show the first attempt losing port %d:\n%s", foreign, log)
	}
}

// TestListenerOwnership_UnknownWithoutProcEntry: a pid /proc cannot show
// (here, one that does not exist) yields known == false, so Start falls back
// to the greeting instead of waiting out its timeout.
func TestListenerOwnership_UnknownWithoutProcEntry(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux /proc only")
	}
	port := foreignGreeter(t)
	if owned, known := listenerOwnership(1<<30, port); owned || known {
		t.Errorf("listenerOwnership(no such pid) = (%v, %v), want (false, false)", owned, known)
	}
	if owned, known := listenerOwnership(os.Getpid(), port); !owned || !known {
		t.Errorf("listenerOwnership(self, own listener) = (%v, %v), want (true, true)", owned, known)
	}
}
