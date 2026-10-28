package dolt

import (
	"testing"
	"time"
)

// TestTransferExecTimeoutDuration verifies BEADS_DOLT_TRANSFER_TIMEOUT parsing:
// valid durations and bare seconds are honored, unset/invalid/non-positive
// values fall back to the compiled-in default. This is the server-mode sibling
// of TestCLIExecTimeoutDuration; before it existed the deadline on the one-shot
// CALL DOLT_PUSH / DOLT_PULL connection was a hardcoded 5m with no override, so
// a store large enough to need longer could not be pushed at all.
func TestTransferExecTimeoutDuration(t *testing.T) {
	t.Run("duration string", func(t *testing.T) {
		t.Setenv(transferExecTimeoutEnv, "90m")
		if got := transferExecTimeoutDuration(); got != 90*time.Minute {
			t.Fatalf("transferExecTimeoutDuration() = %v, want 90m", got)
		}
	})
	t.Run("bare seconds", func(t *testing.T) {
		t.Setenv(transferExecTimeoutEnv, "5400")
		if got := transferExecTimeoutDuration(); got != 5400*time.Second {
			t.Fatalf("transferExecTimeoutDuration() = %v, want 5400s", got)
		}
	})
	t.Run("unset falls back", func(t *testing.T) {
		t.Setenv(transferExecTimeoutEnv, "")
		if got := transferExecTimeoutDuration(); got != transferExecTimeout {
			t.Fatalf("transferExecTimeoutDuration() = %v, want default %v", got, transferExecTimeout)
		}
	})
	t.Run("invalid falls back", func(t *testing.T) {
		t.Setenv(transferExecTimeoutEnv, "eventually")
		if got := transferExecTimeoutDuration(); got != transferExecTimeout {
			t.Fatalf("transferExecTimeoutDuration() = %v, want default %v", got, transferExecTimeout)
		}
	})
	t.Run("non-positive falls back", func(t *testing.T) {
		t.Setenv(transferExecTimeoutEnv, "-1m")
		if got := transferExecTimeoutDuration(); got != transferExecTimeout {
			t.Fatalf("transferExecTimeoutDuration() = %v, want default %v", got, transferExecTimeout)
		}
	})
}
