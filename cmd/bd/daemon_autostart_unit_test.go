package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
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

	if isSandboxed() {
		t.Skip("sandboxed environment blocks unix socket operations")
	}

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
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || os.IsPermission(err) {
			cancel()
			t.Skipf("unix sockets not permitted in this environment: %v", err)
		}
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

func TestDaemonAutostart_AcquireStartLock_CreatesMissingDir(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "missing", "bd.sock")
	lockPath := socketPath + ".startlock"

	if _, err := os.Stat(filepath.Dir(lockPath)); !os.IsNotExist(err) {
		t.Fatalf("expected lock dir to be missing before test, got: %v", err)
	}

	if !acquireStartLock(lockPath, socketPath) {
		t.Fatalf("expected acquireStartLock to succeed when directory missing")
	}

	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("expected lock file to exist, stat error: %v", err)
	}
}

func TestDaemonAutostart_AcquireStartLock_FailsWhenRemoveFails(t *testing.T) {
	// This test verifies that acquireStartLock returns false (instead of
	// recursing infinitely) when os.Remove fails on a stale lock file.

	oldRemove := removeFileFn
	defer func() { removeFileFn = oldRemove }()

	// Stub removeFileFn to always fail
	removeFileFn = func(path string) error {
		return os.ErrPermission
	}

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "bd.sock.startlock")
	socketPath := filepath.Join(tmpDir, "bd.sock")

	// Create a stale lock file with PID 0 (will be detected as dead)
	if err := os.WriteFile(lockPath, []byte("0\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// acquireStartLock should return false since it can't remove the stale lock
	// Previously, this would cause infinite recursion and stack overflow
	if acquireStartLock(lockPath, socketPath) {
		t.Fatalf("expected acquireStartLock to fail when remove fails")
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

func TestDaemonAutostart_StartDaemonProcess_NoGitRepo(t *testing.T) {
	// Test that startDaemonProcess returns false immediately when not in a git repo
	tmpDir := t.TempDir()
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(oldDir)
	}()

	// Change to a temp directory that is NOT a git repo
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	// Capture stderr to verify the message
	output := captureStderr(t, func() {
		result := startDaemonProcess(filepath.Join(tmpDir, "bd.sock"))
		if result {
			t.Errorf("expected startDaemonProcess to return false when not in git repo")
		}
	})

	// Verify the correct message is shown
	if !strings.Contains(output, "No git repository initialized") {
		t.Errorf("expected output to contain 'No git repository initialized', got: %q", output)
	}
	if !strings.Contains(output, "running without background sync") {
		t.Errorf("expected output to contain 'running without background sync', got: %q", output)
	}
}

func TestDaemonAutostart_StartDaemonProcess_NoGitRepo_Quiet(t *testing.T) {
	// Test that startDaemonProcess suppresses the note when quietFlag is true
	tmpDir := t.TempDir()
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	oldQuiet := quietFlag
	defer func() {
		_ = os.Chdir(oldDir)
		quietFlag = oldQuiet
	}()

	// Change to a temp directory that is NOT a git repo
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	// Enable quiet mode
	quietFlag = true

	// Capture stderr to verify the message is suppressed
	output := captureStderr(t, func() {
		result := startDaemonProcess(filepath.Join(tmpDir, "bd.sock"))
		if result {
			t.Errorf("expected startDaemonProcess to return false when not in git repo")
		}
	})

	// Verify the message is NOT shown in quiet mode
	if strings.Contains(output, "No git repository initialized") {
		t.Errorf("expected no output in quiet mode, got: %q", output)
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
	// Create socket directory if needed (GH#1001 - socket may be in /tmp/beads-{hash}/)
	sockDir := filepath.Dir(sock)
	if err := os.MkdirAll(sockDir, 0o750); err != nil {
		t.Fatalf("MkdirAll sockDir: %v", err)
	}
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

// TestIsWispOperation tests the wisp operation detection for auto-daemon-bypass (bd-ta4r)
func TestIsWispOperation(t *testing.T) {
	// Helper to create a command with parent hierarchy
	makeCmd := func(names ...string) *cobra.Command {
		var current *cobra.Command
		for i, name := range names {
			cmd := &cobra.Command{Use: name}
			if i == 0 {
				current = cmd
			} else {
				current.AddCommand(cmd)
				current = cmd
			}
		}
		return current
	}

	tests := []struct {
		name     string
		cmdNames []string // hierarchy: root, child, grandchild...
		args     []string
		want     bool
	}{
		// Wisp subcommands
		{
			name:     "mol wisp (direct)",
			cmdNames: []string{"bd", "mol", "wisp"},
			args:     []string{},
			want:     true,
		},
		{
			name:     "mol wisp create",
			cmdNames: []string{"bd", "mol", "wisp", "create"},
			args:     []string{"some-proto"},
			want:     true,
		},
		{
			name:     "mol wisp list",
			cmdNames: []string{"bd", "mol", "wisp", "list"},
			args:     []string{},
			want:     true,
		},
		{
			name:     "mol wisp gc",
			cmdNames: []string{"bd", "mol", "wisp", "gc"},
			args:     []string{},
			want:     true,
		},
		// mol burn and squash (wisp-only operations)
		{
			name:     "mol burn",
			cmdNames: []string{"bd", "mol", "burn"},
			args:     []string{"bd-wisp-abc"},
			want:     true,
		},
		{
			name:     "mol squash",
			cmdNames: []string{"bd", "mol", "squash"},
			args:     []string{"bd-wisp-abc"},
			want:     true,
		},
		// Ephemeral issue IDs in args (wisp-* pattern)
		{
			name:     "close with bd-wisp ID",
			cmdNames: []string{"bd", "close"},
			args:     []string{"bd-wisp-abc123"},
			want:     true,
		},
		{
			name:     "show with gt-wisp ID",
			cmdNames: []string{"bd", "show"},
			args:     []string{"gt-wisp-xyz"},
			want:     true,
		},
		{
			name:     "update with wisp- prefix",
			cmdNames: []string{"bd", "update"},
			args:     []string{"wisp-test", "--status=closed"},
			want:     true,
		},
		// Legacy eph-* pattern (backwards compatibility)
		{
			name:     "close with legacy bd-eph ID",
			cmdNames: []string{"bd", "close"},
			args:     []string{"bd-eph-abc123"},
			want:     true,
		},
		{
			name:     "show with legacy gt-eph ID",
			cmdNames: []string{"bd", "show"},
			args:     []string{"gt-eph-xyz"},
			want:     true,
		},
		// Non-wisp operations (should NOT bypass)
		{
			name:     "regular show",
			cmdNames: []string{"bd", "show"},
			args:     []string{"bd-abc123"},
			want:     false,
		},
		{
			name:     "regular close",
			cmdNames: []string{"bd", "close"},
			args:     []string{"bd-xyz"},
			want:     false,
		},
		{
			name:     "mol pour (persistent)",
			cmdNames: []string{"bd", "mol", "pour"},
			args:     []string{"some-formula"},
			want:     false,
		},
		{
			name:     "list command",
			cmdNames: []string{"bd", "list"},
			args:     []string{},
			want:     false,
		},
		// Edge cases
		{
			name:     "flag that looks like wisp ID should be ignored",
			cmdNames: []string{"bd", "show"},
			args:     []string{"--format=bd-wisp-style", "bd-regular"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := makeCmd(tt.cmdNames...)
			got := isWispOperation(cmd, tt.args)
			if got != tt.want {
				t.Errorf("isWispOperation() = %v, want %v", got, tt.want)
			}
		})
	}
}
