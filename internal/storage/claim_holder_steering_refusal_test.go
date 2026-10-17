package storage

import (
	"errors"
	"strings"
	"testing"
)

// TestClaimHolderSteeringRefusalVariants pins both variants of the
// holder-steering refusal copy. The two claim bodies
// (issueops.ClaimIssueInTx and domain.issueUseCaseImpl.claim) compose it
// through this one function, so these assertions cover both call sites.
func TestClaimHolderSteeringRefusalVariants(t *testing.T) {
	const holder = "bob"
	enabled := ClaimHolderSteeringRefusal(holder, true)
	disabled := ClaimHolderSteeringRefusal(holder, false)

	for name, err := range map[string]error{"enabled": enabled, "disabled": disabled} {
		if !errors.Is(err, ErrAlreadyClaimed) {
			t.Errorf("%s variant = %v, want errors.Is(err, ErrAlreadyClaimed)", name, err)
		}
		if !strings.Contains(err.Error(), `already assigned to "bob"`) {
			t.Errorf("%s variant = %q, want the holder named", name, err)
		}
		// bd-at6rc: the parseable " by <assignee>" tail stays absent, so
		// beads.ParseClaimConflict recovers the holder from the typed field.
		if strings.Contains(err.Error(), ClaimedByFragment) {
			t.Errorf("%s variant = %q, must omit the parseable %q tail", name, err, ClaimedByFragment)
		}
	}

	// Only the enabled variant may promise lease-based recovery: its claim
	// really does carry a lease that bd reclaim can expire. Byte-for-byte
	// with the copy this composer replaced, before the gate existed.
	if !strings.Contains(enabled.Error(), "lease expiry will surface it for bd reclaim") {
		t.Errorf("enabled variant = %q, want the lease-expiry recovery sentence", enabled)
	}

	// The disabled variant (heartbeat.enabled=false, gastownhall/beads#7506)
	// must not promise recovery that cannot happen: no lease is armed, so
	// nothing expires and bd reclaim reverts nothing.
	if !strings.Contains(disabled.Error(), "heartbeat.enabled=false") {
		t.Errorf("disabled variant = %q, want the flag named", disabled)
	}
	for _, banned := range []string{"lease expiry", "reclaim", "unclaim"} {
		if strings.Contains(disabled.Error(), banned) {
			t.Errorf("disabled variant = %q, must not mention %q: that recovery does not exist", disabled, banned)
		}
	}
}
