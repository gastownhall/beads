package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
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
		// proxy.IdleTimeoutNever is what an explicit 0 normalizes to
		// elsewhere in this file; the contradiction must still be caught
		// whichever of the two callers passes it (see call-site ordering
		// note on validateEphemeralIdleTimeout).
		{"already-normalized never sentinel", proxy.IdleTimeoutNever},
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

// TestEffectiveEphemeralIdleTimeout_DefaultsTo45sWhenUnset is acceptance
// criterion 2: BEADS_EPHEMERAL_ROOT=1 with --proxied-server-idle-timeout
// omitted computes an effective idle window of 45s. The literal duration is
// asserted here (not just equality with the package constant) so a change
// to the constant's value is caught as a contract change, not silently
// absorbed.
func TestEffectiveEphemeralIdleTimeout_DefaultsTo45sWhenUnset(t *testing.T) {
	got := effectiveEphemeralIdleTimeout(true, false, 0)
	want := 45 * time.Second
	if got != want {
		t.Errorf("effectiveEphemeralIdleTimeout(ephemeral=true, set=false, 0) = %s, want %s", got, want)
	}
}

// TestEffectiveEphemeralIdleTimeout_RespectsExplicitPositive is acceptance
// criterion 3: an explicit positive --proxied-server-idle-timeout is never
// overridden by the ephemeral default.
func TestEffectiveEphemeralIdleTimeout_RespectsExplicitPositive(t *testing.T) {
	explicit := 90 * time.Second
	got := effectiveEphemeralIdleTimeout(true, true, explicit)
	if got != explicit {
		t.Errorf("effectiveEphemeralIdleTimeout(ephemeral=true, set=true, %s) = %s, want unchanged %s", explicit, got, explicit)
	}
}

// TestEffectiveEphemeralIdleTimeout_UnchangedWhenNotEphemeral is acceptance
// criterion 4: BEADS_EPHEMERAL_ROOT unset (or not "1") must leave
// serverProxyIdleTimeout byte-for-byte unchanged, whatever its value,
// regardless of whether the flag was explicitly set.
func TestEffectiveEphemeralIdleTimeout_UnchangedWhenNotEphemeral(t *testing.T) {
	for _, tc := range []struct {
		name           string
		idleTimeoutSet bool
		timeout        time.Duration
	}{
		{"flag unset, zero-value duration", false, 0},
		{"flag explicitly set to never sentinel", true, proxy.IdleTimeoutNever},
		{"flag explicitly set positive", true, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := effectiveEphemeralIdleTimeout(false, tc.idleTimeoutSet, tc.timeout)
			if got != tc.timeout {
				t.Errorf("effectiveEphemeralIdleTimeout(ephemeral=false, %v, %s) = %s, want unchanged %s", tc.idleTimeoutSet, tc.timeout, got, tc.timeout)
			}
		})
	}
}
