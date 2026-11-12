//go:build !windows

package dolt

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltversion"
	"github.com/steveyegge/beads/internal/execgrace/execgracetest"
)

// TestCLITransferGracefulCancellation pins AC-5 of GH#7989 for the server
// mode CLI transfer route: canceling the transfer sends the dolt child
// SIGTERM, so a dolt that cleans up its detached transport on SIGTERM
// finishes that before Wait returns, and the command keeps its ten-second
// bounded wait. A dolt whose version cannot be determined is still let
// through and canceled the same way; that it cleans up is the fake's
// behavior, not a promise about an arbitrary installed dolt.
func TestCLITransferGracefulCancellation(t *testing.T) {
	for _, version := range []string{doltversion.RequiredMin.String(), "unknown-build"} {
		t.Run("dolt "+version, func(t *testing.T) {
			child := execgracetest.WriteFakeDolt(t, execgracetest.Cooperative, version)
			t.Setenv("PATH", child.BinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			cmd, _, transferCancel, err := prepareDoltCLITransferCommand(ctx, t.TempDir(), nil, false, "push", "origin", "main")
			if err != nil {
				t.Fatalf("prepareDoltCLITransferCommand: %v", err)
			}
			defer transferCancel()
			if cmd.WaitDelay != cliExecWaitDelay || cliExecWaitDelay != 10*time.Second {
				t.Errorf("WaitDelay = %s (cliExecWaitDelay %s), want the existing 10s", cmd.WaitDelay, cliExecWaitDelay)
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
		})
	}
}
