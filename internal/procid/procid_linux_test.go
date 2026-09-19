//go:build linux

package procid

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestParseStartTime(t *testing.T) {
	tests := []struct {
		name string
		stat string
		want string
	}{
		{
			name: "ordinary command",
			stat: "123 (sleep) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 987654 20",
			want: "987654",
		},
		{
			name: "spaces and parentheses in command",
			stat: "123 (a) b) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 42 20",
			want: "42",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStartTime(tt.stat)
			if err != nil {
				t.Fatalf("parseStartTime: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseStartTime = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCaptureBootIDFailureIsNotProcessGone(t *testing.T) {
	previous := bootIDPath
	bootIDPath = "/definitely-not-a-real-procid-boot-id"
	t.Cleanup(func() { bootIDPath = previous })

	_, err := Capture(os.Getpid())
	if err == nil {
		t.Fatal("Capture succeeded with missing boot_id, want error")
	}
	if IsProcessGone(err) {
		t.Fatalf("IsProcessGone(%v) = true, want false for environmental boot_id failure", err)
	}
	if matched, verifyErr := Verify(os.Getpid(), "linux-v1:irrelevant:1"); verifyErr == nil || matched {
		t.Fatalf("Verify with missing boot_id = (%v, %v), want (false, error)", matched, verifyErr)
	}
}

func TestFallbackFatalSignalAcceptsExitedTarget(t *testing.T) {
	previous := pidfdOpen
	pidfdOpen = func(int, int) (int, error) { return -1, unix.ENOSYS }
	t.Cleanup(func() { pidfdOpen = previous })

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	tok, err := Capture(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		<-waited
		t.Fatalf("Capture(child): %v", err)
	}
	handle, err := Open(cmd.Process.Pid, tok)
	if err != nil {
		_ = cmd.Process.Kill()
		<-waited
		t.Fatalf("Open(child): %v", err)
	}
	defer func() { _ = handle.Close() }()
	if handle.pidfd != -1 {
		t.Fatalf("Open fallback pidfd = %d, want -1", handle.pidfd)
	}
	if err := handle.Kill(); err != nil {
		t.Fatalf("fallback Kill: %v", err)
	}
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("fallback-signaled child did not exit")
	}
}

func requirePidfdSupport(t *testing.T) {
	t.Helper()
	fd, err := pidfdOpen(os.Getpid(), 0)
	if errors.Is(err, unix.ENOSYS) {
		t.Skip("kernel has no pidfd support")
	}
	if err != nil {
		t.Fatalf("pidfd open self: %v", err)
	}
	_ = unix.Close(fd)
}

// A fatal signal whose target exited naturally between Open and Signal must
// count as success on the pidfd path, matching the fallback path: the process
// is gone, which is the goal.
func TestPidfdFatalSignalAcceptsExitedTarget(t *testing.T) {
	requirePidfdSupport(t)

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	tok, err := Capture(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("Capture(child): %v", err)
	}
	handle, err := Open(cmd.Process.Pid, tok)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("Open(child): %v", err)
	}
	defer func() { _ = handle.Close() }()
	if handle.pidfd < 0 {
		t.Fatalf("Open pidfd = %d, want >= 0", handle.pidfd)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	_ = cmd.Wait() // reap, so the target is fully gone, not a zombie
	if err := handle.Kill(); err != nil {
		t.Fatalf("Kill after natural exit = %v, want nil", err)
	}
}

// Open must not hand back a handle for an exited process, and the failure has
// to classify as gone so lifecycle callers quarantine instead of refusing.
func TestOpenReapedChildReportsGone(t *testing.T) {
	requirePidfdSupport(t)

	cmd := exec.Command("sleep", "0.05")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	tok, err := Capture(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Wait()
		t.Fatalf("Capture(child): %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait child: %v", err)
	}
	_, err = Open(cmd.Process.Pid, tok)
	if err == nil {
		t.Fatal("Open(reaped child) succeeded, want error")
	}
	if !IsProcessGone(err) {
		t.Fatalf("IsProcessGone(Open(reaped child)) = false for %v", err)
	}
}

func TestVerifyZombieChildIsGone(t *testing.T) {
	cmd := exec.Command("sleep", "0.05")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	tok, err := Capture(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Wait()
		t.Fatalf("Capture(child): %v", err)
	}
	defer func() { _ = cmd.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		matched, verifyErr := Verify(cmd.Process.Pid, tok)
		if verifyErr != nil {
			t.Fatalf("Verify(zombie child): %v", verifyErr)
		}
		if !matched {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Verify(zombie child) remained true")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Signal 0 still sees an unreaped zombie's PID, proving Verify classified
	// process state rather than relying only on /proc/<pid> disappearing.
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil && !errors.Is(err, unix.EPERM) {
		t.Fatalf("signal 0 to unreaped zombie: %v", err)
	}
}
