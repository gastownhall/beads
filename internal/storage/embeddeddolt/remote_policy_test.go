//go:build cgo

package embeddeddolt

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/storage"
)

// setAllowedRemotePatternsForTest installs federation.allowed-remote-patterns
// for one test on a freshly initialized config, restoring the process config
// on cleanup.
func setAllowedRemotePatternsForTest(t *testing.T, patterns []string) {
	t.Helper()
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("federation.allowed-remote-patterns", patterns)
}

// policyRefusal reports whether err is the allowlist's refusal.
func policyRefusal(err error) bool {
	var policyErr *remotecache.RemotePolicyError
	return errors.As(err, &policyErr)
}

// GH#6233: federation.allowed-remote-patterns was a lint, not a boundary —
// embedded mode's remote verbs dialed whatever remote the name resolved to.
// withPeerAuth is the funnel every verb passes through, so the gate there
// covers them all; these tests pin the funnel's contract directly.
func TestWithPeerAuthRefusedByAllowedRemotePatterns(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	// Registered BEFORE the list tightens: the AC-2 requirement is that a
	// remote registered ahead of the lockdown is still refused at transfer
	// time, not grandfathered.
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "team", RemoteURL: "https://evil.example.com/attacker/team",
	}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}

	called := false
	fn := func(string) error { called = true; return nil }

	// An empty list is no gate at all, even for an out-of-policy URL.
	setAllowedRemotePatternsForTest(t, nil)
	if err := store.withPeerAuth(ctx, "team", fn); err != nil {
		t.Fatalf("withPeerAuth under empty list: %v", err)
	}
	if !called {
		t.Fatal("withPeerAuth under empty list must not refuse")
	}

	// A non-matching list refuses before fn runs.
	setAllowedRemotePatternsForTest(t, []string{"git+https://github.com/myorg/*"})
	called = false
	err := store.withPeerAuth(ctx, "team", fn)
	if !policyRefusal(err) {
		t.Fatalf("withPeerAuth under non-matching list error = %v, want *RemotePolicyError", err)
	}
	if called {
		t.Error("withPeerAuth ran the operation despite the policy refusal")
	}

	// A matching list lets the same remote through, unchanged.
	setAllowedRemotePatternsForTest(t, []string{"git+https://github.com/myorg/*", "git+https://evil.example.com/attacker/*"})
	called = false
	if err := store.withPeerAuth(ctx, "team", fn); err != nil {
		t.Fatalf("withPeerAuth under matching list: %v", err)
	}
	if !called {
		t.Error("withPeerAuth under a matching list must run the operation")
	}
}

// Under a non-empty list a name that resolves to no remote at all refuses
// (fail closed): an unresolved destination is not authorization to dial it.
// The pre-gate behavior fell back to the environment pair and let the engine
// interpret the bare name.
func TestWithPeerAuthUnresolvedRemoteFailsClosedUnderList(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)
	setAllowedRemotePatternsForTest(t, []string{"git+https://github.com/myorg/*"})

	called := false
	err := store.withPeerAuth(ctx, "ghost", func(string) error { called = true; return nil })
	if !policyRefusal(err) {
		t.Fatalf("withPeerAuth(ghost) under non-empty list error = %v, want *RemotePolicyError", err)
	}
	if called {
		t.Error("withPeerAuth(ghost) ran the operation instead of refusing")
	}
}

// The verbs themselves must refuse before their remote entry points: each
// swap seam records a dispatch, and the policy error must arrive with none
// recorded. The peer URLs are file:// so a base build (no gate) fails fast on
// a missing local path instead of timing out on network I/O.
func TestRemoteVerbsRefusedByAllowedRemotePatternsBeforeDispatch(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	for _, name := range []string{defaultRemote, "team"} {
		if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
			Name: name, RemoteURL: "file:///tmp/beads-policy-no-such-remote-" + name,
		}); err != nil {
			t.Fatalf("AddFederationPeer(%s): %v", name, err)
		}
	}
	setAllowedRemotePatternsForTest(t, []string{"git+https://github.com/myorg/*"})

	cases := []struct {
		name string
		call func(*EmbeddedDoltStore, context.Context) error
	}{
		{"Push", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.Push(ctx) }},
		{"Pull", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.Pull(ctx) }},
		{"PullWithStrategy", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.PullWithStrategy(ctx, "ours") }},
		{"ForcePush", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.ForcePush(ctx) }},
		{"PullRemoteWithStrategy", func(s *EmbeddedDoltStore, ctx context.Context) error {
			return s.PullRemoteWithStrategy(ctx, "team", "ours")
		}},
		{"PushRemote", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.PushRemote(ctx, "team", false) }},
		{"PullRemote", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.PullRemote(ctx, "team") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got presentedAuth
			captureRemoteEntryPoints(t, &got)

			err := tc.call(store, ctx)
			if !policyRefusal(err) {
				t.Fatalf("%s error = %v, want *RemotePolicyError", tc.name, err)
			}
			if got.remote != "" {
				t.Errorf("%s reached the remote entry point (%q) despite the policy refusal", tc.name, got.remote)
			}
		})
	}

	// The peer verbs share the funnel but bypass the vc* seams; their refusal
	// identity is the evidence (a dispatch would fail with a file:// or
	// fetch error naming the path, not the allowlist).
	t.Run("PushTo", func(t *testing.T) {
		if err := store.PushTo(ctx, "team"); !policyRefusal(err) {
			t.Fatalf("PushTo error = %v, want *RemotePolicyError", err)
		}
	})
	t.Run("Fetch", func(t *testing.T) {
		if err := store.Fetch(ctx, "team"); !policyRefusal(err) {
			t.Fatalf("Fetch error = %v, want *RemotePolicyError", err)
		}
	})
	t.Run("PullFrom", func(t *testing.T) {
		if _, err := store.PullFrom(ctx, "team"); !policyRefusal(err) {
			t.Fatalf("PullFrom error = %v, want *RemotePolicyError", err)
		}
	})
}

// The accepting half of GH#6233: a list that matches the remote's
// destination leaves the verb's behavior unchanged — it passes the gate and
// reaches its remote entry point exactly as it does with no list (pinned for
// the no-list case by TestRemoteVerbsPresentStoredPeerCredentials). The seam
// returning errStopRemote is the proof of dispatch.
func TestRemoteVerbsDispatchUnderMatchingAllowedRemotePatterns(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "team", RemoteURL: "file:///tmp/beads-policy-ok-team",
	}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}
	setAllowedRemotePatternsForTest(t, []string{"git+https://github.com/myorg/*", "file:///tmp/beads-policy-ok-*"})

	var got presentedAuth
	captureRemoteEntryPoints(t, &got)

	if err := store.PushRemote(ctx, "team", false); !errors.Is(err, errStopRemote) {
		t.Fatalf("PushRemote error = %v, want errStopRemote (dispatch under a matching list)", err)
	}
	if got.remote != "team" {
		t.Errorf("remote = %q, want %q (the verb must reach the entry point under a matching list)", got.remote, "team")
	}
}

// GH#6233, registration half: an out-of-policy proposed URL must be refused
// before any record is written — no dolt remote, no federation_peers row —
// and the prior registrations must survive the refusal untouched.
func TestRemoteRegistrationRefusedByAllowedRemotePatterns(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	// Prior records, registered under an empty list.
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "team", RemoteURL: "file:///tmp/beads-policy-reg-team",
	}); err != nil {
		t.Fatalf("add prior federation peer: %v", err)
	}
	if err := store.AddRemote(ctx, "secondary", "file:///tmp/beads-policy-reg-secondary"); err != nil {
		t.Fatalf("add prior dolt remote: %v", err)
	}

	// Tighten after the fact; nothing in the list matches the proposals.
	setAllowedRemotePatternsForTest(t, []string{"git+https://github.com/myorg/*"})

	if err := store.AddRemote(ctx, "evil", "https://evil.example.com/attacker"); !policyRefusal(err) {
		t.Fatalf("AddRemote error = %v, want *RemotePolicyError", err)
	}
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "evilpeer", RemoteURL: "https://evil.example.com/attacker/peer",
	}); !policyRefusal(err) {
		t.Fatalf("AddFederationPeer error = %v, want *RemotePolicyError", err)
	}

	// The prior records survive; nothing refused is registered.
	remotes, err := store.ListRemotes(ctx)
	if err != nil {
		t.Fatalf("list remotes: %v", err)
	}
	got := map[string]string{}
	for _, r := range remotes {
		got[r.Name] = r.URL
	}
	if got["team"] != "file:///tmp/beads-policy-reg-team" || got["secondary"] != "file:///tmp/beads-policy-reg-secondary" {
		t.Errorf("prior remotes after refusal = %v, want team and secondary at their registered URLs", got)
	}
	if _, ok := got["evil"]; ok {
		t.Errorf("refused remote %q is registered", "evil")
	}
	peer, err := store.GetFederationPeer(ctx, "team")
	if err != nil || peer.RemoteURL != "file:///tmp/beads-policy-reg-team" {
		t.Errorf("prior federation peer after refusal = (%+v, %v), want its registered URL", peer, err)
	}
	if _, err := store.GetFederationPeer(ctx, "evilpeer"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("refused peer row exists or misreported: %v, want storage.ErrNotFound", err)
	}
}
