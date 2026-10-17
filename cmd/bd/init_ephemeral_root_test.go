package main

import (
	"strings"
	"testing"
	"time"
)

// TestValidateEphemeralIdleTimeout_ContradictsExplicitZeroOrNegative is
// acceptance criterion 1 (be-djq0v): BEADS_EPHEMERAL_ROOT=1 combined with an
// explicit --proxied-server-idle-timeout of 0 or negative makes two
// contradictory claims about whether the proxy may idle-exit. Neither
// signal may be silently overridden, so this must be a hard error naming
// both.
func TestValidateEphemeralIdleTimeout_ContradictsExplicitZeroOrNegative(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
	}{
		{"explicit zero", 0},
		{"explicit negative", -5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEphemeralIdleTimeout(true, true, tc.timeout)
			if err == nil {
				t.Fatalf("validateEphemeralIdleTimeout(ephemeral=true, set=true, %s) = nil, want a contradiction error", tc.timeout)
			}
			if !strings.Contains(err.Error(), "BEADS_EPHEMERAL_ROOT") {
				t.Errorf("error does not name the env var signal: %v", err)
			}
			if !strings.Contains(err.Error(), "--proxied-server-idle-timeout") {
				t.Errorf("error does not name the flag signal: %v", err)
			}
		})
	}
}

// TestValidateEphemeralIdleTimeout_NoContradictionCases covers the paths
// where validateEphemeralIdleTimeout must return nil: either the flag
// wasn't explicitly set (nothing to contradict), the explicit value is
// positive (acceptance criterion 3), or BEADS_EPHEMERAL_ROOT isn't set at
// all (acceptance criterion 4 — the check is a no-op and defers entirely to
// the pre-existing, ephemeral-agnostic validation).
func TestValidateEphemeralIdleTimeout_NoContradictionCases(t *testing.T) {
	for _, tc := range []struct {
		name           string
		ephemeralRoot  bool
		idleTimeoutSet bool
		timeout        time.Duration
	}{
		{"ephemeral but flag not set", true, false, 0},
		{"ephemeral with explicit positive", true, true, 90 * time.Second},
		{"not ephemeral, flag unset", false, false, 0},
		{"not ephemeral, flag explicit zero", false, true, 0},
		{"not ephemeral, flag explicit negative", false, true, -5 * time.Second},
		{"not ephemeral, flag explicit positive", false, true, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEphemeralIdleTimeout(tc.ephemeralRoot, tc.idleTimeoutSet, tc.timeout)
			if err != nil {
				t.Errorf("validateEphemeralIdleTimeout(%v, %v, %s) = %v, want nil", tc.ephemeralRoot, tc.idleTimeoutSet, tc.timeout, err)
			}
		})
	}
}

// TestInitIdleTimeoutHelpNamesEphemeralRoot keeps BEADS_EPHEMERAL_ROOT
// discoverable: the flag it constrains is where its one effect is
// documented, and the generated CLI reference is built from this string.
func TestInitIdleTimeoutHelpNamesEphemeralRoot(t *testing.T) {
	flag := initCmd.Flags().Lookup("proxied-server-idle-timeout")
	if flag == nil {
		t.Fatal("init does not register --proxied-server-idle-timeout")
	}
	if !strings.Contains(flag.Usage, "BEADS_EPHEMERAL_ROOT=1") {
		t.Errorf("--proxied-server-idle-timeout help does not mention BEADS_EPHEMERAL_ROOT=1: %q", flag.Usage)
	}
}
