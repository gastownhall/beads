package dolt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// TestTransferExecTimeoutDuration pins how BEADS_DOLT_TRANSFER_TIMEOUT
// resolves (GH#6375): positive duration strings and bare seconds are honored;
// unset, unparsable and non-positive values keep the 5m default.
func TestTransferExecTimeoutDuration(t *testing.T) {
	if transferExecTimeoutEnv != "BEADS_DOLT_TRANSFER_TIMEOUT" {
		t.Fatalf("transferExecTimeoutEnv = %q, want BEADS_DOLT_TRANSFER_TIMEOUT", transferExecTimeoutEnv)
	}
	if transferExecTimeout != 5*time.Minute {
		t.Fatalf("transferExecTimeout = %v, want 5m (the default must not change)", transferExecTimeout)
	}
	tests := []struct {
		name  string
		value string
		set   bool
		want  time.Duration
	}{
		{name: "unset", want: transferExecTimeout},
		{name: "empty", value: "", set: true, want: transferExecTimeout},
		{name: "duration string", value: "20m", set: true, want: 20 * time.Minute},
		{name: "bare seconds", value: "90", set: true, want: 90 * time.Second},
		{name: "surrounding whitespace", value: "  45s \t", set: true, want: 45 * time.Second},
		{name: "invalid", value: "soon", set: true, want: transferExecTimeout},
		{name: "zero", value: "0", set: true, want: transferExecTimeout},
		{name: "zero duration", value: "0s", set: true, want: transferExecTimeout},
		{name: "negative duration", value: "-5s", set: true, want: transferExecTimeout},
		{name: "negative bare seconds", value: "-30", set: true, want: transferExecTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(transferExecTimeoutEnv, tt.value)
			} else {
				unsetEnvForTest(t, transferExecTimeoutEnv)
			}
			if got := transferExecTimeoutDuration(); got != tt.want {
				t.Fatalf("transferExecTimeoutDuration() with %s=%q = %v, want %v", transferExecTimeoutEnv, tt.value, got, tt.want)
			}
		})
	}
}

// TestAnnotateTransferTimeout pins when a transfer failure gets the deadline
// hint: only for the driver's ErrInvalidConn after at least the captured
// deadline has elapsed. Everything else passes through as the identical error.
func TestAnnotateTransferTimeout(t *testing.T) {
	const deadline = 90 * time.Second
	sqlErr := &mysql.MySQLError{Number: 1105, Message: "remote rejected the push"}
	wrappedInvalid := fmt.Errorf("exec: %w", mysql.ErrInvalidConn)
	canceled := fmt.Errorf("exec: %w", context.Canceled)

	passThrough := []struct {
		name    string
		err     error
		elapsed time.Duration
	}{
		{name: "success", err: nil, elapsed: 2 * deadline},
		{name: "ordinary error", err: errors.New("boom"), elapsed: 2 * deadline},
		{name: "sql error", err: sqlErr, elapsed: 2 * deadline},
		{name: "invalid conn before deadline", err: mysql.ErrInvalidConn, elapsed: deadline - time.Millisecond},
		{name: "immediate reset", err: wrappedInvalid, elapsed: 0},
		{name: "canceled context", err: canceled, elapsed: 2 * deadline},
		{name: "expired context", err: context.DeadlineExceeded, elapsed: 2 * deadline},
	}
	for _, tt := range passThrough {
		t.Run(tt.name, func(t *testing.T) {
			if got := annotateTransferTimeout(tt.err, tt.elapsed, deadline); got != tt.err {
				t.Fatalf("annotateTransferTimeout(%v, %v, %v) = %v, want the identical error back", tt.err, tt.elapsed, deadline, got)
			}
		})
	}

	annotated := []struct {
		name    string
		err     error
		elapsed time.Duration
	}{
		{name: "sentinel at deadline", err: mysql.ErrInvalidConn, elapsed: deadline},
		{name: "sentinel past deadline", err: mysql.ErrInvalidConn, elapsed: deadline + time.Second},
		{name: "wrapped sentinel past deadline", err: wrappedInvalid, elapsed: 3 * deadline},
	}
	for _, tt := range annotated {
		t.Run(tt.name, func(t *testing.T) {
			got := annotateTransferTimeout(tt.err, tt.elapsed, deadline)
			if got == nil || got == tt.err {
				t.Fatalf("annotateTransferTimeout(%v, %v, %v) = %v, want an annotated error", tt.err, tt.elapsed, deadline, got)
			}
			if !errors.Is(got, mysql.ErrInvalidConn) || !errors.Is(got, tt.err) {
				t.Fatalf("annotated error %q no longer wraps %v", got, tt.err)
			}
			msg := got.Error()
			for _, want := range []string{"read deadline (1m30s) may have expired", transferExecTimeoutEnv + "=", tt.err.Error()} {
				if !strings.Contains(msg, want) {
					t.Fatalf("annotated error %q does not contain %q", msg, want)
				}
			}
			// The hint is advice, not a diagnosis: a reset after the deadline
			// is still possible, so it must not claim the deadline fired.
			if strings.Contains(msg, "timed out") || strings.Contains(msg, "deadline exceeded") {
				t.Fatalf("annotated error %q asserts a timeout it cannot prove", msg)
			}
		})
	}
}
