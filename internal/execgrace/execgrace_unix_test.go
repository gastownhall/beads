//go:build !windows

package execgrace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/execgrace/execgracetest"
)

// startFake starts the fake dolt for mode under a cancellable context,
// configured with waitDelay, and waits for it to report ready.
func startFake(t *testing.T, mode execgracetest.Mode, waitDelay time.Duration) (*execgracetest.Child, *exec.Cmd, context.CancelFunc) {
	t.Helper()
	child := execgracetest.WriteFakeDolt(t, mode, "1.0.0")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, filepath.Join(child.BinDir, "dolt"), "push", "origin", "main")
	Configure(cmd, waitDelay)
	child.Attach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake dolt: %v", err)
	}
	child.WaitReady(t, 10*time.Second)
	return child, cmd, cancel
}

// waitBounded runs cmd.Wait and fails the test if it outlasts limit.
func waitBounded(t *testing.T, cmd *exec.Cmd, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(limit):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("Wait did not return within %s of cancellation", limit)
		return 0
	}
}

func exitSignal(cmd *exec.Cmd) syscall.Signal {
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 0
	}
	return ws.Signal()
}

func testPlatformCancellation(t *testing.T) {
	t.Run("cooperative child reaps its transport before Wait returns", func(t *testing.T) {
		child, cmd, cancel := startFake(t, execgracetest.Cooperative, 10*time.Second)
		cancel()
		waitBounded(t, cmd, 20*time.Second)
		if !child.CleanedUp() {
			t.Errorf("child exited without running its SIGTERM cleanup (exit: %v)", cmd.ProcessState)
		}
		if child.HelperAlive(t) {
			t.Error("the child's detached helper outlived the command")
		}
	})

	t.Run("child ignoring SIGTERM is killed after the wait delay", func(t *testing.T) {
		const waitDelay = 300 * time.Millisecond
		_, cmd, cancel := startFake(t, execgracetest.IgnoresTerm, waitDelay)
		cancel()
		elapsed := waitBounded(t, cmd, 10*time.Second)
		if sig := exitSignal(cmd); sig != syscall.SIGKILL {
			t.Errorf("child ended by %v (state %v), want SIGKILL escalation", sig, cmd.ProcessState)
		}
		if elapsed < waitDelay {
			t.Errorf("child killed after %s, want the %s wait delay first", elapsed, waitDelay)
		}
	})

	t.Run("descendant holding the pipes does not block Wait", func(t *testing.T) {
		_, cmd, cancel := startFake(t, execgracetest.PipeHolder, 300*time.Millisecond)
		cancel()
		waitBounded(t, cmd, 10*time.Second)
		if sig := exitSignal(cmd); sig != syscall.SIGTERM {
			t.Errorf("child ended by %v (state %v), want SIGTERM", sig, cmd.ProcessState)
		}
	})

	t.Run("exited process reports ErrProcessDone", func(t *testing.T) {
		child := execgracetest.WriteFakeDolt(t, execgracetest.IgnoresTerm, "1.0.0")
		cmd := exec.CommandContext(context.Background(), filepath.Join(child.BinDir, "dolt"), "version")
		Configure(cmd, time.Second)
		if err := cmd.Run(); err != nil {
			t.Fatalf("dolt version: %v", err)
		}
		if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("Cancel after exit = %v, want os.ErrProcessDone", err)
		}
	})
}
