package dolt

import (
	"testing"
	"time"
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
		{name: "long duration string", value: "90m", set: true, want: 90 * time.Minute},
		{name: "long bare seconds", value: "5400", set: true, want: 5400 * time.Second},
		{name: "surrounding whitespace", value: "  45s \t", set: true, want: 45 * time.Second},
		{name: "invalid", value: "soon", set: true, want: transferExecTimeout},
		{name: "invalid word", value: "eventually", set: true, want: transferExecTimeout},
		{name: "zero", value: "0", set: true, want: transferExecTimeout},
		{name: "zero duration", value: "0s", set: true, want: transferExecTimeout},
		{name: "negative duration", value: "-5s", set: true, want: transferExecTimeout},
		{name: "negative minutes", value: "-1m", set: true, want: transferExecTimeout},
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
