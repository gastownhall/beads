package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/storage"
)

const policyRejectedURL = "https://evil.example.com/attacker/repo.git"

func setRegistrationPolicy(t *testing.T, patterns ...string) {
	t.Helper()
	config.Set(remotecache.AllowedRemotePatternsKey, patterns)
	t.Cleanup(func() { config.Set(remotecache.AllowedRemotePatternsKey, []string{}) })
}

// GH#6233: `bd dolt remote add` refuses a URL the allowlist rejects before it
// lists, prompts, removes or adds, so the remote already registered under the
// name keeps its URL.
func TestEnsureDoltRemoteRefusesDisallowedURLBeforeAnyWrite(t *testing.T) {
	newForgeSyncRemoteWorkspace(t, "")
	setRegistrationPolicy(t, "git+https://github.com/myorg/*")
	prior := []storage.RemoteInfo{{Name: "origin", URL: "git+https://github.com/myorg/repo.git"}}
	store := &fakeDoltRemoteAddStore{remotes: append([]storage.RemoteInfo(nil), prior...)}

	prompted := false
	_, err := ensureDoltRemote(context.Background(), store, "origin", policyRejectedURL, func(string, string, string, string) bool {
		prompted = true
		return true
	})
	if !errors.Is(err, remotecache.ErrRemoteNotAllowed) {
		t.Fatalf("ensureDoltRemote error = %v, want a %s refusal", err, remotecache.AllowedRemotePatternsKey)
	}
	if strings.Contains(err.Error(), "evil.example.com") {
		t.Errorf("refusal names the URL: %v", err)
	}
	if prompted {
		t.Error("the overwrite prompt ran for a refused URL")
	}
	if len(store.calls) != 0 {
		t.Errorf("store calls = %v, want none", store.calls)
	}
	if !reflect.DeepEqual(store.remotes, prior) {
		t.Errorf("remotes = %+v, want the prior %+v", store.remotes, prior)
	}
}

// GH#6233: a git origin the allowlist rejects is refused before consent is
// asked for, so nothing is adopted even with --yes.
func TestAdoptGitOriginRemoteRefusedByAllowedRemotePatterns(t *testing.T) {
	beadsDir := newForgeSyncRemoteWorkspace(t, "")
	repo := filepath.Dir(beadsDir)
	initGitRepoAt(t, repo)
	runGitForBootstrapTest(t, repo, "remote", "add", "origin", policyRejectedURL)
	beads.ResetCaches()
	t.Cleanup(beads.ResetCaches)
	setRegistrationPolicy(t, "git+https://github.com/myorg/*")

	raw := &fakeAdoptingDoltStore{}
	adopted, err := adoptGitOriginRemoteForPush(context.Background(), raw, adoptPolicy{AssumeYes: true}, pushAdoptOptIn)
	if adopted || !errors.Is(err, remotecache.ErrRemoteNotAllowed) {
		t.Fatalf("adoptGitOriginRemoteForPush = (%v, %v), want (false, a %s refusal)", adopted, err, remotecache.AllowedRemotePatternsKey)
	}
	if raw.addRemoteCalls != 0 {
		t.Errorf("AddRemote called %d times for a refused origin", raw.addRemoteCalls)
	}
	if got := config.GetString("sync.remote"); got != "" {
		t.Errorf("sync.remote = %q after a refused adoption, want unset", got)
	}
}

// GH#6233: bd config apply refuses a federation.remote the allowlist rejects
// before it opens the store, in dry run and for real, without echoing the URL.
func TestApplyRemoteRefusesDisallowedFederationRemote(t *testing.T) {
	newForgeSyncRemoteWorkspace(t, "")
	config.Set("federation.remote", policyRejectedURL)
	setRegistrationPolicy(t, "git+https://github.com/myorg/*")

	for _, dryRun := range []bool{true, false} {
		result := applyRemote(true, dryRun)
		if result.Status != applyStatusError || !strings.Contains(result.Message, remotecache.AllowedRemotePatternsKey) {
			t.Errorf("applyRemote(dryRun=%v) = %+v, want the federation.remote refused by %s", dryRun, result, remotecache.AllowedRemotePatternsKey)
		}
		if strings.Contains(result.Message+result.Error, "evil.example.com") {
			t.Errorf("applyRemote(dryRun=%v) names the URL: %+v", dryRun, result)
		}
	}
}

// GH#6233: on a rig with no remote at all, bd sync under a non-empty
// federation.allowed-remote-patterns reports the benign no-remote result
// (exit 0, JSON status "no-remote") without attempting a transfer the policy
// would refuse for want of a URL to check.
func TestSyncAllowedRemotePatternsNoRemote(t *testing.T) {
	policyRefusal := &remotecache.RemotePolicyError{Subject: remotecache.RemoteSubject("origin"), Reason: "remote not found, so its URL cannot be checked against " + remotecache.AllowedRemotePatternsKey}
	for _, asJSON := range []bool{false, true} {
		fake := &fakeSyncStore{pullErr: policyRefusal, pushErr: policyRefusal}
		setupSyncCommandTest(t, fake)
		config.Set(remotecache.AllowedRemotePatternsKey, []string{"dolthub://myorg/*"})
		jsonOutput = asJSON

		var runErr error
		stdout, _ := captureStdio(t, func() { runErr = runSyncCommand(syncCmd, nil) })
		if runErr != nil {
			t.Fatalf("json=%v: runSyncCommand() error = %v, want nil (no remote is the benign case)", asJSON, runErr)
		}
		if asJSON {
			var got syncOutcome
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("json.Unmarshal(%q): %v", stdout, err)
			}
			if got.Status != syncStatusNoRemote {
				t.Errorf("Status = %q, want %q", got.Status, syncStatusNoRemote)
			}
		}
		if fake.pullCalls+fake.pushCalls != 0 {
			t.Errorf("json=%v: %d pull and %d push attempts on a rig with no remote, want none", asJSON, fake.pullCalls, fake.pushCalls)
		}
	}
}
