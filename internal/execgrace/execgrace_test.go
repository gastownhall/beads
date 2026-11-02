package execgrace

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestGracePeriodIsFifteenSeconds pins the production window: fifteen seconds
// is long enough for a hung child's teardown to finish, short enough that an
// expired context does not hold its caller hostage (GH#7989).
func TestGracePeriodIsFifteenSeconds(t *testing.T) {
	// Cannot be parallel: the teardown tests in this package shrink GracePeriod.
	if GracePeriod != 15*time.Second {
		t.Errorf("GracePeriod = %s, want the 15s production default", GracePeriod)
	}
}

// TestConfigurePreservesCallerWaitDelay pins that Configure only fills in a
// missing WaitDelay: a caller with a shorter bound keeps it.
func TestConfigurePreservesCallerWaitDelay(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "true")
	cmd.WaitDelay = 10 * time.Second
	Configure(cmd)
	if cmd.WaitDelay != 10*time.Second {
		t.Errorf("Configure changed WaitDelay to %s, want the caller's 10s preserved", cmd.WaitDelay)
	}
}