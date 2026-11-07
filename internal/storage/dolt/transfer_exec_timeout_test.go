package dolt

import (
	"testing"
	"time"
)

// TestTransferExecTimeoutDuration verifies BEADS_DOLT_TRANSFER_TIMEOUT
// parsing (GH#6375): positive duration strings and bare seconds are honored;
// unset, invalid and non-positive values fall back to the compiled-in 5m
// default, the same ladder BEADS_CLI_TRANSFER_TIMEOUT already uses for the
// dolt-CLI transfer route.
func TestTransferExecTimeoutDuration(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"unset", "", transferExecTimeout},
		{"duration string", "20m", 20 * time.Minute},
		{"bare seconds", "90", 90 * time.Second},
		{"whitespace is trimmed", " 15m ", 15 * time.Minute},
		{"invalid text", "soon", transferExecTimeout},
		{"zero falls back", "0", transferExecTimeout},
		{"negative falls back", "-5s", transferExecTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(transferExecTimeoutEnv, tt.raw)
			if got := transferExecTimeoutDuration(); got != tt.want {
				t.Fatalf("transferExecTimeoutDuration() with %q = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}
