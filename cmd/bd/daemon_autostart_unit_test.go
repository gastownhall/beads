package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

func tempSockDir(t *testing.T) string {
	t.Helper()

	base := "/tmp"
	if runtime.GOOS == windowsOS {
		base = os.TempDir()
	} else if _, err := os.Stat(base); err != nil {
		base = os.TempDir()
	}

	d, err := os.MkdirTemp(base, "bd-sock-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func startTestRPCServer(t *testing.T) (socketPath string, cleanup func()) {
	t.Helper()

	tmpDir := tempSockDir(t)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	socketPath = filepath.Join(beadsDir, "bd.sock")
	db := filepath.Join(beadsDir, "test.db")
	store := newTestStore(t, db)

	ctx, cancel := context.WithCancel(context.Background())
	log := newTestLogger()

	server, _, err := startRPCServer(ctx, socketPath, store, tmpDir, db, log)
	if err != nil {
		cancel()
		t.Fatalf("startRPCServer: %v", err)
	}

	cleanup = func() {
		cancel()
		if server != nil {
			_ = server.Stop()
		}
	}

	return socketPath, cleanup
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()
	_ = w.Close()
	os.Stderr = old
	<-done
	_ = r.Close()

	return buf.String()
}

func TestDaemonAutostart_AcquireStartLock_CreatesAndCleansStale(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "bd.sock.startlock")
	pid, err := readPIDFromFile(lockPath)
	if err == nil || pid != 0 {
		// lock doesn't exist yet; expect read to fail.
	}

	if !acquireStartLock(lockPath, filepath.Join(tmpDir, "bd.sock")) {
		t.Fatalf("expected acquireStartLock to succeed")
	}
	got, err := readPIDFromFile(lockPath)
	if err != nil {
		t.Fatalf("readPIDFromFile: %v", err)
	}
	if got != os.Getpid() {
		t.Fatalf("expected lock PID %d, got %d", os.Getpid(), got)
	}

	// Stale lock: dead/unreadable PID should be removed and recreated.
	if err := os.WriteFile(lockPath, []byte("0\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !acquireStartLock(lockPath, filepath.Join(tmpDir, "bd.sock")) {
		t.Fatalf("expected acquireStartLock to succeed on stale lock")
	}
	got, err = readPIDFromFile(lockPath)
	if err != nil {
		t.Fatalf("readPIDFromFile: %v", err)
	}
	if got != os.Getpid() {
		t.Fatalf("expected recreated lock PID %d, got %d", os.Getpid(), got)
	}
}

func TestDaemonAutostart_SocketHealthAndReadiness(t *testing.T) {
	socketPath, cleanup := startTestRPCServer(t)
	defer cleanup()

	if !canDialSocket(socketPath, 500*time.Millisecond) {
		t.Fatalf("expected canDialSocket to succeed")
	}
	if !isDaemonHealthy(socketPath) {
		t.Fatalf("expected isDaemonHealthy to succeed")
	}
	if !waitForSocketReadiness(socketPath, 500*time.Millisecond) {
		t.Fatalf("expected waitForSocketReadiness to succeed")
	}

	missing := filepath.Join(tempSockDir(t), "missing.sock")
	if canDialSocket(missing, 50*time.Millisecond) {
		t.Fatalf("expected canDialSocket to fail")
	}
	if waitForSocketReadiness(missing, 200*time.Millisecond) {
		t.Fatalf("expected waitForSocketReadiness to time out")
	}
}

func TestDaemonAutostart_HandleExistingSocket(t *testing.T) {
	socketPath, cleanup := startTestRPCServer(t)
	defer cleanup()

	if !handleExistingSocket(socketPath) {
		t.Fatalf("expected handleExistingSocket true for running daemon")
	}
}

func TestDaemonAutostart_HandleExistingSocket_StaleCleansUp(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	socketPath := filepath.Join(beadsDir, "bd.sock")
	pidFile := filepath.Join(beadsDir, "daemon.pid")
	if err := os.WriteFile(socketPath, []byte("not-a-socket"), 0o600); err != nil {
		t.Fatalf("WriteFile socket: %v", err)
	}
	if err := os.WriteFile(pidFile, []byte("0\n"), 0o600); err != nil {
		t.Fatalf("WriteFile pid: %v", err)
	}

	if handleExistingSocket(socketPath) {
		t.Fatalf("expected false for stale socket")
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("expected socket removed")
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("expected pidfile removed")
	}
}

func TestDaemonAutostart_TryAutoStartDaemon_EarlyExits(t *testing.T) {
	oldFailures := daemonStartFailures
	oldLast := lastDaemonStartAttempt
	defer func() {
		daemonStartFailures = oldFailures
		lastDaemonStartAttempt = oldLast
	}()

	daemonStartFailures = 1
	lastDaemonStartAttempt = time.Now()
	if tryAutoStartDaemon(filepath.Join(t.TempDir(), "bd.sock")) {
		t.Fatalf("expected tryAutoStartDaemon to skip due to backoff")
	}

	daemonStartFailures = 0
	lastDaemonStartAttempt = time.Time{}
	socketPath, cleanup := startTestRPCServer(t)
	defer cleanup()
	if !tryAutoStartDaemon(socketPath) {
		t.Fatalf("expected tryAutoStartDaemon true when daemon already healthy")
	}
}

func TestDaemonAutostart_MiscHelpers(t *testing.T) {
	if determineSocketPath("/x") != "/x" {
		t.Fatalf("determineSocketPath should be identity")
	}

	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	old := config.GetDuration("flush-debounce")
	defer config.Set("flush-debounce", old)

	config.Set("flush-debounce", 0)
	if got := getDebounceDuration(); got != 5*time.Second {
		t.Fatalf("expected default debounce 5s, got %v", got)
	}
	config.Set("flush-debounce", 2*time.Second)
	if got := getDebounceDuration(); got != 2*time.Second {
		t.Fatalf("expected debounce 2s, got %v", got)
	}
}

func TestDaemonAutostart_EmitVerboseWarning(t *testing.T) {
	old := daemonStatus
	defer func() { daemonStatus = old }()

	daemonStatus.SocketPath = "/tmp/bd.sock"
	for _, tt := range []struct {
		reason      string
		shouldWrite bool
	}{
		{FallbackConnectFailed, true},
		{FallbackHealthFailed, true},
		{FallbackAutoStartDisabled, true},
		{FallbackAutoStartFailed, true},
		{FallbackDaemonUnsupported, true},
		{FallbackWorktreeSafety, false},
		{FallbackFlagNoDaemon, false},
	} {
		t.Run(tt.reason, func(t *testing.T) {
			daemonStatus.FallbackReason = tt.reason
			out := captureStderr(t, emitVerboseWarning)
			if tt.shouldWrite && out == "" {
				t.Fatalf("expected output")
			}
			if !tt.shouldWrite && out != "" {
				t.Fatalf("expected no output, got %q", out)
			}
		})
	}
}

func TestDaemonAutostart_StartDaemonProcess_Stubbed(t *testing.T) {
	oldExec := execCommandFn
	oldWait := waitForSocketReadinessFn
	oldCfg := configureDaemonProcessFn
	defer func() {
		execCommandFn = oldExec
		waitForSocketReadinessFn = oldWait
		configureDaemonProcessFn = oldCfg
	}()

	execCommandFn = func(string, ...string) *exec.Cmd {
		return exec.Command(os.Args[0], "-test.run=^$")
	}
	waitForSocketReadinessFn = func(string, time.Duration) bool { return true }
	configureDaemonProcessFn = func(*exec.Cmd) {}

	if !startDaemonProcess(filepath.Join(t.TempDir(), "bd.sock")) {
		t.Fatalf("expected startDaemonProcess true when readiness stubbed")
	}
}

func TestDaemonAutostart_RestartDaemonForVersionMismatch_Stubbed(t *testing.T) {
	oldExec := execCommandFn
	oldWait := waitForSocketReadinessFn
	oldRun := isDaemonRunningFn
	oldCfg := configureDaemonProcessFn
	defer func() {
		execCommandFn = oldExec
		waitForSocketReadinessFn = oldWait
		isDaemonRunningFn = oldRun
		configureDaemonProcessFn = oldCfg
	}()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	oldDB := dbPath
	defer func() { dbPath = oldDB }()
	dbPath = filepath.Join(beadsDir, "test.db")

	pidFile, err := getPIDFilePath()
	if err != nil {
		t.Fatalf("getPIDFilePath: %v", err)
	}
	sock := getSocketPath()
	if err := os.WriteFile(pidFile, []byte("999999\n"), 0o600); err != nil {
		t.Fatalf("WriteFile pid: %v", err)
	}
	if err := os.WriteFile(sock, []byte("stale"), 0o600); err != nil {
		t.Fatalf("WriteFile sock: %v", err)
	}

	execCommandFn = func(string, ...string) *exec.Cmd {
		return exec.Command(os.Args[0], "-test.run=^$")
	}
	waitForSocketReadinessFn = func(string, time.Duration) bool { return true }
	isDaemonRunningFn = func(string) (bool, int) { return false, 0 }
	configureDaemonProcessFn = func(*exec.Cmd) {}

	if !restartDaemonForVersionMismatch() {
		t.Fatalf("expected restartDaemonForVersionMismatch true when stubbed")
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("expected pidfile removed")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("expected socket removed")
	}
}
