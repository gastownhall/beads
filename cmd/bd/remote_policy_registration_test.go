package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

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

// stubAdoptionOriginURL makes git-origin adoption read url as the git origin,
// with no git repository and without the process-wide repository caches, which
// hold whatever repository an earlier test in this binary resolved.
func stubAdoptionOriginURL(t *testing.T, url string) {
	t.Helper()
	old := adoptionOriginURL
	adoptionOriginURL = func(context.Context) (string, error) { return url, nil }
	t.Cleanup(func() { adoptionOriginURL = old })
}

// GH#6233: a git origin the allowlist rejects is refused before consent is
// asked for, so nothing is adopted even with --yes, and the refusal names the
// git origin rather than a Dolt remote that does not exist. --no-adopt adopts
// nothing, so it vets nothing: the caller's no-remote handling runs, as it
// does without an allowlist.
func TestAdoptGitOriginRemoteRefusedByAllowedRemotePatterns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		policy  adoptPolicy
		refused bool
	}{
		{"--yes", adoptPolicy{AssumeYes: true}, true},
		{"interactive", adoptPolicy{Interactive: true}, true},
		{"non-interactive", adoptPolicy{}, true},
		{"--no-adopt", adoptPolicy{Disabled: true, Interactive: true}, false},
		{"--no-adopt beats --yes", adoptPolicy{Disabled: true, AssumeYes: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newForgeSyncRemoteWorkspace(t, "")
			t.Setenv("BD_NO_REMOTE_ADOPT", "")
			stubAdoptionOriginURL(t, policyRejectedURL)
			setRegistrationPolicy(t, "git+https://github.com/myorg/*")

			raw := &fakeAdoptingDoltStore{}
			var adopted bool
			var err error
			_, stderr := captureStdio(t, func() {
				adopted, err = adoptGitOriginRemoteForPush(context.Background(), raw, tc.policy, pushAdoptOptIn)
			})
			if adopted {
				t.Fatal("adoptGitOriginRemoteForPush adopted an origin the allowlist rejects")
			}
			if tc.refused {
				if !errors.Is(err, remotecache.ErrRemoteNotAllowed) {
					t.Fatalf("adoptGitOriginRemoteForPush error = %v, want a %s refusal", err, remotecache.AllowedRemotePatternsKey)
				}
				if msg := err.Error(); !strings.HasPrefix(msg, adoptionPolicySubject+" is not permitted") || strings.Contains(msg, "evil.example.com") {
					t.Errorf("refusal %q: want it to name the git origin, and never the URL", msg)
				}
			} else if err != nil {
				t.Fatalf("adoptGitOriginRemoteForPush error = %v, want nil: --no-adopt is a request, not a failure", err)
			}
			if strings.Contains(stderr, "Adopt this remote") {
				t.Errorf("consent was asked for an origin the allowlist rejects:\n%s", stderr)
			}
			if raw.addRemoteCalls != 0 {
				t.Errorf("AddRemote called %d times for a refused origin", raw.addRemoteCalls)
			}
			if got := config.GetString("sync.remote"); got != "" {
				t.Errorf("sync.remote = %q after a refused adoption, want unset", got)
			}
		})
	}
}

// GH#6233: --no-adopt (here BD_NO_REMOTE_ADOPT=1) on a rig with no Dolt remote
// and a git origin the allowlist rejects is the benign no-remote case it is
// without an allowlist: `bd dolt push` and `bd sync`, through the production
// syncAdoptGitOrigin binding, exit 0 and attempt no transfer.
func TestNoAdoptOriginOutsideAllowedRemotePatternsKeepsNoRemoteSkip(t *testing.T) {
	refusal := &remotecache.RemotePolicyError{Subject: remotecache.RemoteSubject("origin")}
	for _, verb := range []struct {
		name string
		run  func() error
	}{
		{"bd dolt push", func() error { return doltPushCmd.RunE(doltPushCmd, nil) }},
		{"bd sync", func() error {
			syncAdoptGitOrigin = adoptGitOriginRemoteForPush
			return runSyncCommand(syncCmd, nil)
		}},
	} {
		t.Run(verb.name, func(t *testing.T) {
			isolateBeadsDirForTest(t)
			t.Chdir(t.TempDir())
			t.Setenv("BD_NO_REMOTE_ADOPT", "1")
			fake := &fakeSyncStore{pullErr: refusal, pushErr: refusal}
			setupSyncCommandTest(t, fake)
			stubAdoptionOriginURL(t, policyRejectedURL)
			config.Set(remotecache.AllowedRemotePatternsKey, []string{"git+https://github.com/myorg/*"})

			var runErr error
			stdout, stderr := captureStdio(t, func() { runErr = verb.run() })
			if runErr != nil {
				t.Fatalf("%s error = %v, want nil (--no-adopt on a rig with no remote is the benign case)\nstderr:\n%s", verb.name, runErr, stderr)
			}
			if fake.pullCalls+fake.pushCalls != 0 {
				t.Errorf("%s: %d pull and %d push attempts on a rig with no remote, want none", verb.name, fake.pullCalls, fake.pushCalls)
			}
			if !strings.Contains(stdout, "No remote is configured") {
				t.Errorf("%s stdout = %q, want the no-remote guidance", verb.name, stdout)
			}
		})
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
