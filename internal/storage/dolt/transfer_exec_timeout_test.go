package dolt

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
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

// TestAnnotateTransferTimeout pins that a read-deadline failure on the one-shot
// transfer connection names the deadline and the env var that raises it. The
// driver's own message is just "invalid connection", which sent an operator
// looking at the network and at GitHub rather than at the 5m deadline that had
// actually fired.
func TestAnnotateTransferTimeout(t *testing.T) {
	t.Run("nil stays nil", func(t *testing.T) {
		if err := annotateTransferTimeout(nil, time.Minute); err != nil {
			t.Fatalf("annotateTransferTimeout(nil) = %v, want nil", err)
		}
	})

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"mysql invalid conn", mysql.ErrInvalidConn},
		{"driver bad conn", driver.ErrBadConn},
		{"os deadline exceeded", os.ErrDeadlineExceeded},
		{"wrapped", fmt.Errorf("failed to push to origin/main: %w", mysql.ErrInvalidConn)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := annotateTransferTimeout(tc.err, 5*time.Minute)
			msg := got.Error()
			if !strings.Contains(msg, "5m0s") {
				t.Errorf("message does not name the deadline that fired: %q", msg)
			}
			if !strings.Contains(msg, transferExecTimeoutEnv) {
				t.Errorf("message does not name %s: %q", transferExecTimeoutEnv, msg)
			}
			if !errors.Is(got, tc.err) {
				t.Errorf("annotation broke the error chain: %v does not wrap %v", got, tc.err)
			}
		})
	}

	t.Run("unrelated error passes through unchanged", func(t *testing.T) {
		orig := errors.New("remote origin not found")
		got := annotateTransferTimeout(orig, 5*time.Minute)
		if got != orig { //nolint:errorlint // identity is the assertion
			t.Fatalf("annotateTransferTimeout rewrote a non-timeout error: %v", got)
		}
	})
}
