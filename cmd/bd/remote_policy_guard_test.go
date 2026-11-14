package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/storage"
)

// The GH#6233 registration half: an out-of-policy proposed URL must be
// refused where it is proposed — bd dolt remote add, git-origin adoption and
// bd config apply — before any mutation, so prior records survive and consent
// is never spent on a URL the workspace refuses.

// TestEnsureDoltRemoteRefusedByAllowedRemotePatterns: the overwrite path
// removes the existing remote before adding the new one; a policy-refused
// proposal must be rejected before any of that — no listing work, no prompt,
// no remove, no add, the prior record untouched.
func TestEnsureDoltRemoteRefusedByAllowedRemotePatterns(t *testing.T) {
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("federation.allowed-remote-patterns", []string{"git+https://github.com/myorg/*"})

	store := &fakeDoltRemoteAddStore{
		remotes: []storage.RemoteInfo{
			{Name: "origin", URL: "git+https://github.com/org/existing.git"},
		},
	}
	prompted := false

	_, err := ensureDoltRemote(context.Background(), store, "origin", "git+https://evil.example.com/attacker/repo.git",
		func(surface, name, existingURL, newURL string) bool {
			prompted = true
			return true
		})
	var policyErr *remotecache.RemotePolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("ensureDoltRemote error = %v, want *RemotePolicyError", err)
	}
	if prompted {
		t.Error("the overwrite confirmation was consulted for a policy refusal")
	}
	if len(store.calls) != 0 {
		t.Errorf("store calls = %v, want none (the refusal precedes the listing)", store.calls)
	}
	want := []storage.RemoteInfo{{Name: "origin", URL: "git+https://github.com/org/existing.git"}}
	if !reflect.DeepEqual(store.remotes, want) {
		t.Errorf("remotes = %+v, want %+v (the prior record must survive the refusal)", store.remotes, want)
	}
}

// TestAdoptGitOriginRemoteForPushRefusedByAllowedRemotePatterns: adoption
// derives its URL from git origin, so the refusal must land between the
// derivation and the consent prompt — consent granted (AssumeYes), the only
// thing that can stop adoption here is the allowlist, and nothing may be
// registered. The origin resolver is swapped for the seam because the real
// one goes through the process-wide RepoContext cache, which a unit test
// cannot point at a repo of its own.
func TestAdoptGitOriginRemoteForPushRefusedByAllowedRemotePatterns(t *testing.T) {
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("federation.allowed-remote-patterns", []string{"git+https://github.com/myorg/*"})

	oldURL := adoptGitOriginURL
	adoptGitOriginURL = func(context.Context) (string, error) {
		return "https://evil.example.com/attacker/repo.git", nil
	}
	t.Cleanup(func() { adoptGitOriginURL = oldURL })

	store := &fakeAdoptingDoltStore{}
	adopted, err := adoptGitOriginRemoteForPush(context.Background(), store, adoptPolicy{AssumeYes: true}, pushAdoptOptIn)
	var policyErr *remotecache.RemotePolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("adoptGitOriginRemoteForPush = (%v, %v), want the policy refusal", adopted, err)
	}
	if adopted {
		t.Error("adoption reported success despite the policy refusal")
	}
	if store.addRemoteCalls != 0 {
		t.Errorf("AddRemote called %d time(s) after a policy refusal", store.addRemoteCalls)
	}
}

// The seam is only sound while it still resolves the real origin.
func TestAdoptGitOriginURLIsWired(t *testing.T) {
	if reflect.ValueOf(adoptGitOriginURL).Pointer() != reflect.ValueOf(gitOriginGetURLForActiveRepo).Pointer() {
		t.Error("adoptGitOriginURL is not bound to gitOriginGetURLForActiveRepo; adoption would resolve its origin somewhere else")
	}
}

// TestApplyRemoteRefusedByAllowedRemotePatterns: bd config apply re-registers
// the Dolt origin from federation.remote by removing the old remote first;
// an out-of-policy federation.remote must be refused before that, without
// echoing the refused URL.
func TestApplyRemoteRefusedByAllowedRemotePatterns(t *testing.T) {
	newForgeSyncRemoteWorkspace(t, "")
	config.Set("federation.remote", "git+https://evil.example.com/attacker/repo.git")
	config.Set("federation.allowed-remote-patterns", []string{"git+https://github.com/myorg/*"})

	for _, dryRun := range []bool{true, false} {
		result := applyRemote(true, dryRun)
		if result.Status != applyStatusError ||
			!strings.Contains(result.Error, "federation.allowed-remote-patterns") {
			t.Errorf("applyRemote(dryRun=%v) = %+v, want the out-of-policy federation.remote refused", dryRun, result)
		}
		if strings.Contains(result.Error, "evil.example.com") {
			t.Errorf("applyRemote(dryRun=%v) error %q echoes the refused URL", dryRun, result.Error)
		}
	}
}

// The benign companion: a federation.remote inside the list must not hit the
// allowlist refusal (the option-shaped refusal tests cover the other
// neighbor; whatever else applyRemote reports here is not this gate's
// business).
func TestApplyRemoteMatchingFederationRemoteNotRefusedByPatterns(t *testing.T) {
	newForgeSyncRemoteWorkspace(t, "")
	config.Set("federation.remote", "git+https://github.com/myorg/beads.git")
	config.Set("federation.allowed-remote-patterns", []string{"git+https://github.com/myorg/*"})

	result := applyRemote(true, true)
	if result.Status == applyStatusError && strings.Contains(result.Error, "allowed-remote-patterns") {
		t.Fatalf("applyRemote = %+v, a matching federation.remote must not hit the allowlist refusal", result)
	}
}
