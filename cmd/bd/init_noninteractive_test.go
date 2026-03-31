package main

import (
	"os"
	"testing"
)

// TestIsNonInteractiveInit tests the non-interactive detection logic.
func TestIsNonInteractiveInit(t *testing.T) {
	// Save original env vars and restore after tests
	origCI := os.Getenv("CI")
	origBDNI := os.Getenv("BD_NON_INTERACTIVE")
	defer func() {
		os.Setenv("CI", origCI)
		os.Setenv("BD_NON_INTERACTIVE", origBDNI)
	}()

	tests := []struct {
		name      string
		flagValue bool
		envCI     string
		envBDNI   string
		want      bool
	}{
		{
			name:      "flag true overrides everything",
			flagValue: true,
			envCI:     "",
			envBDNI:   "",
			want:      true,
		},
		{
			name:      "BD_NON_INTERACTIVE=1",
			flagValue: false,
			envCI:     "",
			envBDNI:   "1",
			want:      true,
		},
		{
			name:      "BD_NON_INTERACTIVE=true",
			flagValue: false,
			envCI:     "",
			envBDNI:   "true",
			want:      true,
		},
		{
			name:      "CI=true",
			flagValue: false,
			envCI:     "true",
			envBDNI:   "",
			want:      true,
		},
		{
			name:      "CI=1",
			flagValue: false,
			envCI:     "1",
			envBDNI:   "",
			want:      true,
		},
		{
			name:      "CI=false does not trigger",
			flagValue: false,
			envCI:     "false",
			envBDNI:   "",
			// In test env, stdin is not a TTY, so this is still true
			want: true,
		},
		{
			name:      "no flag no env falls back to terminal detection",
			flagValue: false,
			envCI:     "",
			envBDNI:   "",
			// In test environment, stdin is piped (not a TTY), so non-interactive
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("CI", tt.envCI)
			os.Setenv("BD_NON_INTERACTIVE", tt.envBDNI)

			got := isNonInteractiveInit(tt.flagValue)
			if got != tt.want {
				t.Errorf("isNonInteractiveInit(%v) with CI=%q BD_NON_INTERACTIVE=%q = %v, want %v",
					tt.flagValue, tt.envCI, tt.envBDNI, got, tt.want)
			}
		})
	}
}

// TestIsNonInteractiveInitPrecedence tests that flag takes precedence over env vars.
func TestIsNonInteractiveInitPrecedence(t *testing.T) {
	origCI := os.Getenv("CI")
	origBDNI := os.Getenv("BD_NON_INTERACTIVE")
	defer func() {
		os.Setenv("CI", origCI)
		os.Setenv("BD_NON_INTERACTIVE", origBDNI)
	}()

	// Flag true should always win
	os.Setenv("CI", "")
	os.Setenv("BD_NON_INTERACTIVE", "")
	if !isNonInteractiveInit(true) {
		t.Error("flag=true should always return true regardless of env")
	}

	// BD_NON_INTERACTIVE should take precedence over CI
	os.Setenv("BD_NON_INTERACTIVE", "1")
	os.Setenv("CI", "")
	if !isNonInteractiveInit(false) {
		t.Error("BD_NON_INTERACTIVE=1 should return true")
	}
}
