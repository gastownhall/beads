package externaldeps

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestClaimsAndClosesAskForALivePolicyProbe pins which entry points mark
// their policy probe live (storage.WithLivePolicyProbe): every claim and close
// guard, so a cached "the server enforces it" can never stand down their
// client-side refusal, and no read, so a listing may answer from the cache.
func TestClaimsAndClosesAskForALivePolicyProbe(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		live bool
		run  func(t *testing.T, s *Store)
	}{
		{"ready listing", false, func(t *testing.T, s *Store) {
			if _, err := s.GetReadyWork(ctx, types.WorkFilter{}); err != nil {
				t.Fatal(err)
			}
		}},
		{"lifecycle close", true, func(t *testing.T, s *Store) {
			lc, err := s.IssueLifecycle()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lc.Close(ctx, publicops.CloseRequest{IssueID: "a"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"lifecycle claim", true, func(t *testing.T, s *Store) {
			lc, err := s.IssueLifecycle()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lc.Update(ctx, publicops.UpdateRequest{IssueID: "a", Claim: true}); err != nil {
				t.Fatal(err)
			}
		}},
		{"claim ready", true, func(t *testing.T, s *Store) {
			if _, err := s.ClaimReadyIssue(ctx, types.WorkFilter{}, "me"); err != nil {
				t.Fatal(err)
			}
		}},
		{"batch close", true, func(t *testing.T, s *Store) {
			closer, err := s.BatchCloser()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := closer.CloseBatch(ctx, publicops.CloseBatchRequest{Actor: "me", Items: []publicops.BatchCloseItem{{IssueID: "a"}}}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := &fakeStore{lifecycle: &fakeLifecycle{}, batch: &fakeBatchCloser{}}
			tc.run(t, testStore(raw, &fakeStore{}, false))
			if len(raw.liveProbes) == 0 {
				t.Fatal("the policy was never probed")
			}
			for i, live := range raw.liveProbes {
				if live != tc.live {
					t.Errorf("probe %d: live = %v, want %v", i, live, tc.live)
				}
			}
		})
	}
	if storage.LivePolicyProbe(ctx) {
		t.Error("a plain context reads as live")
	}
}

// TestRemoteClaimsAndClosesAskForALivePolicyProbe is the same pin over a
// remote store's role views (remote_roles.go): direct claim, claim-next and
// batch close probe live; the reader's ready listing does not.
func TestRemoteClaimsAndClosesAskForALivePolicyProbe(t *testing.T) {
	for _, tc := range []struct {
		name string
		live bool
		run  func(t *testing.T, s *Store)
	}{
		{"ready", false, func(t *testing.T, s *Store) {
			r, err := s.IssueReader()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Ready(t.Context(), publicops.ReadyRequest{}); err != nil {
				t.Fatal(err)
			}
		}},
		{"claim", true, func(t *testing.T, s *Store) {
			c, err := s.IssueClaimer()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Claim(t.Context(), publicops.ClaimRequest{Actor: "a", IssueID: "be-a"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"claim next", true, func(t *testing.T, s *Store) {
			c, err := s.ReadyClaimer()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.ClaimNext(t.Context(), publicops.ClaimNextRequest{Actor: "a"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"batch close", true, func(t *testing.T, s *Store) {
			c, err := s.BatchCloser()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.CloseBatch(t.Context(), publicops.CloseBatchRequest{Actor: "a", Items: []publicops.BatchCloseItem{{IssueID: "be-a"}}}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, _ := newRemotePolicyStore(true)
			raw := &passthroughFake{remoteFake: base.inner.(*remoteFake), claimer: &recordingClaimer{}}
			store := New(raw, func(ProjectName) (string, bool) { return "", false }, nil)
			store.warnProject = nil
			tc.run(t, store)
			probes := raw.liveProbes
			if len(probes) == 0 {
				t.Fatal("the policy was never probed")
			}
			for i, live := range probes {
				if live != tc.live {
					t.Errorf("probe %d: live = %v, want %v", i, live, tc.live)
				}
			}
		})
	}
}
