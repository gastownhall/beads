package main

import (
	"strings"
	"testing"
)

// TestDecideRemoteAdoptionFailsClosed is the #5068 regression assertion in its
// purest form: with no consent input at all, the answer is refuse. Every
// variant below exists because a "sensible default" in any one of them is how
// the original bug read — bd derived a remote and uploaded to it because
// nothing said not to.
func TestDecideRemoteAdoptionFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name        string
		policy      adoptPolicy
		wantDecide  adoptDecision
		wantRefusal adoptRefusal
	}{
		{
			name:        "zero value refuses",
			policy:      adoptPolicy{},
			wantDecide:  adoptRefuse,
			wantRefusal: adoptRefusedNonInteractive,
		},
		{
			name:        "non-interactive without --yes refuses",
			policy:      adoptPolicy{Interactive: false},
			wantDecide:  adoptRefuse,
			wantRefusal: adoptRefusedNonInteractive,
		},
		{
			name:        "interactive asks rather than assuming",
			policy:      adoptPolicy{Interactive: true},
			wantDecide:  adoptAsk,
			wantRefusal: adoptRefusalNone,
		},
		{
			name:        "--yes is consent even when non-interactive",
			policy:      adoptPolicy{AssumeYes: true},
			wantDecide:  adoptProceed,
			wantRefusal: adoptRefusalNone,
		},
		{
			name:        "--no-adopt refuses, and it is not an error",
			policy:      adoptPolicy{Disabled: true, Interactive: true},
			wantDecide:  adoptRefuse,
			wantRefusal: adoptRefusedDisabled,
		},
		{
			// Contradictory instructions: the safer one wins. Otherwise a
			// scripted --yes buried in an alias would silently defeat a
			// deliberate BD_NO_REMOTE_ADOPT=1 on the machine.
			name:        "--no-adopt beats --yes",
			policy:      adoptPolicy{Disabled: true, AssumeYes: true, Interactive: true},
			wantDecide:  adoptRefuse,
			wantRefusal: adoptRefusedDisabled,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotDecide, gotRefusal := decideRemoteAdoption(tc.policy)
			if gotDecide != tc.wantDecide || gotRefusal != tc.wantRefusal {
				t.Errorf("decideRemoteAdoption(%+v) = (%v, %v), want (%v, %v)",
					tc.policy, gotDecide, gotRefusal, tc.wantDecide, tc.wantRefusal)
			}
		})
	}
}

func TestCurrentAdoptPolicyHonorsEnvKillSwitch(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want bool
	}{
		{"1", true},
		{"true", true},
		{"YES", true},
		{"0", false},
		{"", false},
		{"maybe", false},
	} {
		t.Run("BD_NO_REMOTE_ADOPT="+tc.env, func(t *testing.T) {
			t.Setenv("BD_NO_REMOTE_ADOPT", tc.env)
			if got := currentAdoptPolicy(false, false, true, false).Disabled; got != tc.want {
				t.Errorf("Disabled = %v, want %v", got, tc.want)
			}
		})
	}

	t.Setenv("BD_NO_REMOTE_ADOPT", "")
	if !currentAdoptPolicy(false, true, true, false).Disabled {
		t.Error("--no-adopt alone did not disable adoption")
	}
}

// --json is a machine contract: even on a TTY (agent harnesses often allocate
// one), the policy must be non-interactive so a JSON consumer never blocks on
// a prompt it cannot see. The refusal error is the correct surface instead.
func TestCurrentAdoptPolicyJSONModeIsNotInteractive(t *testing.T) {
	t.Setenv("BD_NO_REMOTE_ADOPT", "")
	p := currentAdoptPolicy(false, false, true, true)
	if p.Interactive {
		t.Fatal("jsonMode policy is Interactive; bd sync --json on a pty would hang on the consent prompt")
	}
	if decision, refusal := decideRemoteAdoption(p); decision != adoptRefuse || refusal != adoptRefusedNonInteractive {
		t.Fatalf("jsonMode without --yes: decision = %v/%v, want refuse/non-interactive", decision, refusal)
	}
	if decision, _ := decideRemoteAdoption(currentAdoptPolicy(true, false, true, true)); decision != adoptProceed {
		t.Fatal("jsonMode with --yes must still proceed")
	}
}

// TestAdoptionRefusedErrorNamesTheURL: the reporter's complaint was not only
// that bd adopted a remote, but that they could not tell where the upload was
// going. An error that omits the URL does not fix that half.
func TestAdoptionRefusedErrorNamesTheURL(t *testing.T) {
	const url = "git+ssh://git@github.com/someone/public-repo.git"
	msg := adoptionRefusedError(url, pushAdoptOptIn).Error()
	for _, want := range []string{url, "bd dolt push --yes", "--no-adopt", "BD_NO_REMOTE_ADOPT"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal message does not mention %q:\n%s", want, msg)
		}
	}
}

// The refusal must name the command the user actually ran: steering a sync
// user into `bd dolt push --yes` would trade their pull away.
func TestAdoptionRefusedErrorNamesTheCallersCommand(t *testing.T) {
	msg := adoptionRefusedError("git+ssh://git@github.com/x/y.git", syncAdoptOptIn).Error()
	if !strings.Contains(msg, "bd sync --yes") {
		t.Errorf("sync-path refusal does not offer bd sync --yes:\n%s", msg)
	}
	if strings.Contains(msg, "bd dolt push --yes") {
		t.Errorf("sync-path refusal steers the user to bd dolt push:\n%s", msg)
	}
}

// TestApplyAdoptionConsentGatesTheWrite is acceptance item 5 of #5068 at the
// gate itself: with no configured Dolt remote and a derived git-origin URL,
// nothing downstream may run without consent. Everything after this call in
// adoptGitOriginRemoteForPush writes (AddRemote, sync.remote, the git commit,
// then the upload), so proceed=false is exactly "nothing was uploaded".
func TestApplyAdoptionConsentGatesTheWrite(t *testing.T) {
	const url = "git+ssh://git@github.com/someone/public-repo.git"

	for _, tc := range []struct {
		name        string
		policy      adoptPolicy
		wantProceed bool
		wantErr     bool
	}{
		{
			// The reported bug: a scripted/hook invocation with a public git
			// origin and no configured remote must not upload.
			name:        "non-interactive without --yes does not proceed, and says so",
			policy:      adoptPolicy{Interactive: false},
			wantProceed: false,
			wantErr:     true,
		},
		{
			name:        "--yes proceeds",
			policy:      adoptPolicy{AssumeYes: true},
			wantProceed: true,
			wantErr:     false,
		},
		{
			// --no-adopt is a request, not a failure: the caller falls through
			// to ordinary no-remote handling.
			name:        "--no-adopt does not proceed and is not an error",
			policy:      adoptPolicy{Disabled: true, Interactive: true},
			wantProceed: false,
			wantErr:     false,
		},
		{
			name:        "--no-adopt beats --yes",
			policy:      adoptPolicy{Disabled: true, AssumeYes: true},
			wantProceed: false,
			wantErr:     false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BD_NO_REMOTE_ADOPT", "")
			proceed, err := applyAdoptionConsent(url, tc.policy, pushAdoptOptIn)
			if proceed != tc.wantProceed {
				t.Errorf("proceed = %v, want %v", proceed, tc.wantProceed)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), url) {
				t.Errorf("refusal did not name the remote it would have adopted: %v", err)
			}
		})
	}
}

// The zero-value policy is what a caller that forgets to wire the flags would
// pass. It must refuse, not adopt.
func TestApplyAdoptionConsentZeroPolicyRefuses(t *testing.T) {
	t.Setenv("BD_NO_REMOTE_ADOPT", "")
	proceed, err := applyAdoptionConsent("git+ssh://git@github.com/x/y.git", adoptPolicy{}, pushAdoptOptIn)
	if proceed {
		t.Fatal("zero-value policy proceeded with adoption")
	}
	if err == nil {
		t.Fatal("zero-value policy refused silently; the user gets no explanation")
	}
}
