package execgrace

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestGracefulCommandCancellation pins AC-5 of GH#7989 for the helper: on
// Unix a canceled command's immediate child gets SIGTERM, so a cooperative
// child reaps its detached transport before Wait returns, and os/exec's kill
// and pipe close remain the bounded fallback. Windows keeps the default
// cancellation with the same bounded wait.
func TestGracefulCommandCancellation(t *testing.T) {
	t.Run("bounded wait configured", func(t *testing.T) {
		cmd := exec.CommandContext(context.Background(), "dolt", "version")
		Configure(cmd, 7*time.Second)
		if cmd.WaitDelay != 7*time.Second {
			t.Errorf("WaitDelay = %s, want 7s", cmd.WaitDelay)
		}
	})
	testPlatformCancellation(t)
}
