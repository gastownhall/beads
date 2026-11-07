//go:build !windows

package remotecache

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/execgrace/execgracetest"
)

// TestDoltCommandGracefulCancellation pins AC-5 of GH#7989 for cache
// transfers: canceling a doltCmd context sends the dolt child SIGTERM, so a
// dolt that cleans up its detached transport on SIGTERM finishes that before
// Wait returns, and the command keeps a fifteen-second bounded wait.
func TestDoltCommandGracefulCancellation(t *testing.T) {
	child := execgracetest.WriteFakeDolt(t, execgracetest.Cooperative, "1.0.0")
	t.Setenv("PATH", child.BinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cmd := doltCmd(ctx, t.TempDir(), "push", "origin", "main")
	if cmd.WaitDelay != 15*time.Second {
		t.Errorf("doltCmd WaitDelay = %s, want 15s", cmd.WaitDelay)
	}
	child.Attach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	child.WaitReady(t, 10*time.Second)
	cancel()
	execgracetest.WaitBounded(t, cmd, 30*time.Second)

	if !child.CleanedUp() {
		t.Errorf("dolt exited without its SIGTERM cleanup (exit: %v)", cmd.ProcessState)
	}
	if child.HelperAlive(t) {
		t.Error("dolt's detached transport outlived the command")
	}
}
