package httpclient

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/issueops"
)

// TestUpdateForceClaimBlockedIsSentOnlyToAServerThatAdvertisesIt pins the
// blocked-claim override `bd update --claim --force` sends. Against a server
// advertising issues.update.forceClaimBlocked it is one updateIssue carrying
// `claim` and `force_claim_blocked`; against one that does not, it is the typed
// capability refusal naming the token, raised BEFORE the dial: the override is
// never dropped (an older server would claim past nothing, or refuse the claim
// the caller forced) and never split into a claimIssue that cannot carry it.
func TestUpdateForceClaimBlockedIsSentOnlyToAServerThatAdvertisesIt(t *testing.T) {
	forced := issueops.UpdateRequest{Actor: "alice", IssueID: "bd-1", Claim: true, ForceClaimBlocked: true}

	t.Run("advertised", func(t *testing.T) {
		w := &stubWire{update: &apigen.UpdateIssueResponse{Revision: "7", Changed: true}}
		lifecycle, err := New(testTarget(t), w, &apigen.ContextResponse{
			BdVersion: "1.2.3",
			Capabilities: []string{wire.CapIssuesUpdateAllowTemplate, wire.CapIssuesUpdateClaim,
				wire.CapIssuesUpdateForceClaimBlocked},
		}).IssueLifecycle()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lifecycle.Update(t.Context(), forced); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if len(w.calls) != 1 || w.calls[0] != "updateIssue:bd-1" {
			t.Fatalf("calls = %v, want one updateIssue", w.calls)
		}
		if want := (wire.UpdateFlags{Claim: true, ForceClaimBlocked: true}); w.lastFlags != want {
			t.Errorf("flags = %+v, want %+v", w.lastFlags, want)
		}
	})

	for _, tc := range []struct {
		name         string
		capabilities []string
	}{
		{"unadvertised", []string{wire.CapIssuesUpdateAllowTemplate, wire.CapIssuesUpdateClaim}},
		// Without issues.update.claim either, a claim alone would go to
		// claimIssue, which has no member for the override.
		{"unadvertised, and no update claim", []string{wire.CapIssuesUpdateAllowTemplate}},
		// The override rides a claim, so a server advertising it without
		// issues.update.claim is inconsistent; the claim route is decided by
		// that token, and claimIssue still cannot carry the override.
		{"advertised without update claim", []string{wire.CapIssuesUpdateAllowTemplate, wire.CapIssuesUpdateForceClaimBlocked}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &stubWire{update: &apigen.UpdateIssueResponse{Revision: "7"}}
			lifecycle, err := New(testTarget(t), w, &apigen.ContextResponse{BdVersion: "1.2.3", Capabilities: tc.capabilities}).IssueLifecycle()
			if err != nil {
				t.Fatal(err)
			}
			_, err = lifecycle.Update(t.Context(), forced)
			token := wire.CapIssuesUpdateForceClaimBlocked
			if strings.Contains(tc.name, "advertised without") {
				token = wire.CapIssuesUpdateClaim
			}
			requireCapabilityRefusal(t, err, token, w)
			if !strings.Contains(err.Error(), token) {
				t.Errorf("refusal %q does not name %s", err.Error(), token)
			}
			if token == wire.CapIssuesUpdateForceClaimBlocked && !errors.Is(err, wire.ErrCapabilityAbsent) {
				t.Errorf("err = %v, want it to match wire.ErrCapabilityAbsent", err)
			}
		})
	}

	t.Run("an unforced claim is unaffected", func(t *testing.T) {
		w := &stubWire{update: &apigen.UpdateIssueResponse{Revision: "7"}}
		lifecycle, err := stubStore(t, w).IssueLifecycle()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lifecycle.Update(t.Context(), issueops.UpdateRequest{Actor: "alice", IssueID: "bd-1", Claim: true}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if want := (wire.UpdateFlags{Claim: true}); w.lastFlags != want {
			t.Errorf("flags = %+v, want %+v", w.lastFlags, want)
		}
	})

	t.Run("the override is not a claim-only update", func(t *testing.T) {
		if isClaimOnlyUpdate(forced) {
			t.Error("isClaimOnlyUpdate accepted a forced claim; claimIssue would drop the override")
		}
	})
}
