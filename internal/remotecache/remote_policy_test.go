package remotecache

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateRemoteWithPolicyEmptyListIsNoOp pins AC-1 of GH#6233: an
// absent or empty allowlist means the setting is off, so the shared gate
// introduces no rejection — not even a structural one. Enforcement is
// opt-in; a workspace that never configured a list must observe no change.
func TestValidateRemoteWithPolicyEmptyListIsNoOp(t *testing.T) {
	for _, url := range []string{
		"https://evil.example.com/attacker/repo.git",
		"dolthub://anyorg/anyrepo",
		"file:///tmp/somewhere",
		// Even a URL the structural rules would refuse passes an empty list:
		// structural validation stays where it already was (registration
		// paths call ValidateRemoteURL directly) and is not added here.
		"dolthub://\x00evil",
	} {
		if err := ValidateRemoteWithPolicy(nil, "federation.remote", url); err != nil {
			t.Errorf("ValidateRemoteWithPolicy(nil, %q) = %v, want nil (empty list means unrestricted)", url, err)
		}
		if err := ValidateRemoteWithPolicy([]string{}, "federation.remote", url); err != nil {
			t.Errorf("ValidateRemoteWithPolicy([], %q) = %v, want nil (empty list means unrestricted)", url, err)
		}
	}
}

// TestValidateRemoteWithPolicyNormalizedDestination pins the shared gate's
// destination form (AC-7): the URL is evaluated in its Dolt-normalized
// spelling, so a typed forge URL and its registered git+ spelling get one
// verdict, and patterns match literally (path.Match: * does not cross /,
// malformed entries match nothing).
func TestValidateRemoteWithPolicyNormalizedDestination(t *testing.T) {
	orgPattern := "git+https://github.com/myorg/*"
	otherOrg := "git+https://github.com/otherorg/*"
	tests := []struct {
		name     string
		url      string
		patterns []string
		wantErr  bool
	}{
		{"typed https url passes org pattern", "https://github.com/myorg/repo", []string{orgPattern}, false},
		{"registered git+ spelling passes org pattern", "git+https://github.com/myorg/repo", []string{orgPattern}, false},
		{"typed https url fails other-org pattern", "https://github.com/myorg/repo", []string{otherOrg}, true},
		{"registered spelling fails other-org pattern", "git+https://github.com/myorg/repo", []string{otherOrg}, true},
		{"https pattern does not cover typed spelling", "https://github.com/myorg/repo", []string{"https://github.com/myorg/*"}, true},
		{"SCP spelling normalizes to git+ssh", "git@github.com:myorg/repo.git", []string{"git+ssh://git@github.com/myorg/*"}, false},
		{"ssh URL normalizes to git+ssh", "ssh://git@github.com/myorg/repo", []string{"git+ssh://git@github.com/myorg/*"}, false},
		{"native dolthub URL passes literal pattern", "dolthub://myorg/beads", []string{"dolthub://myorg/*"}, false},
		{"native file URL passes literal pattern", "file:///tmp/beads-remote", []string{"file:///tmp/*"}, false},
		// path.Match: * does not cross the separator.
		{"star does not cross slash", "git+https://github.com/myorg/team/repo", []string{orgPattern}, true},
		// A malformed pattern entry matches nothing rather than passing
		// everything through.
		{"malformed pattern entry matches nothing", "git+https://github.com/myorg/repo", []string{"[ab"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRemoteWithPolicy(tt.patterns, "federation.remote", tt.url)
			if tt.wantErr {
				var policyErr *RemotePolicyError
				if !errors.As(err, &policyErr) {
					t.Fatalf("ValidateRemoteWithPolicy(%q, %v) = %v, want *RemotePolicyError", tt.url, tt.patterns, err)
				}
			} else if err != nil {
				t.Fatalf("ValidateRemoteWithPolicy(%q, %v) = %v, want nil", tt.url, tt.patterns, err)
			}
		})
	}
}

// TestValidateRemoteWithPolicyBlankEntriesDeny pins the blank-entry rule: a
// non-empty list can never become unrestricted. A blank pattern matches no
// URL, so a list of blanks denies every remote.
func TestValidateRemoteWithPolicyBlankEntriesDeny(t *testing.T) {
	if err := ValidateRemoteWithPolicy([]string{""}, "federation.remote", "git+https://github.com/myorg/repo"); err == nil {
		t.Fatal("ValidateRemoteWithPolicy([\"\"], ...) = nil, want refusal: a blank-only list is deny-all, not allow-all")
	}
	err := ValidateRemoteWithPolicy([]string{"", "git+https://github.com/other/*"}, "federation.remote", "git+https://github.com/myorg/repo")
	if err == nil {
		t.Fatal("ValidateRemoteWithPolicy with a blank entry plus a non-matching entry = nil, want refusal")
	}
	// The blank entry adds nothing when another entry matches.
	if err := ValidateRemoteWithPolicy([]string{"", "git+https://github.com/myorg/*"}, "federation.remote", "git+https://github.com/myorg/repo"); err != nil {
		t.Fatalf("ValidateRemoteWithPolicy(blank + matching entry) = %v, want nil", err)
	}
}

// TestValidateRemoteWithPolicyStructuralCheckUnderList pins the
// structural-plus-pattern contract: under a non-empty list even "*" cannot
// admit a URL the structural rules refuse.
func TestValidateRemoteWithPolicyStructuralCheckUnderList(t *testing.T) {
	for _, url := range []string{
		"dolthub://\x00evil",           // control character
		"-uattack",                     // option-shaped URL
		"git+https://",                 // no hostname
		"weird+scheme://github.com/x",  // unknown scheme
	} {
		err := ValidateRemoteWithPolicy([]string{"*"}, "federation.remote", url)
		if err == nil {
			t.Errorf("ValidateRemoteWithPolicy([\"*\"], %q) = nil, want structural refusal under a non-empty list", url)
			continue
		}
		var policyErr *RemotePolicyError
		if !errors.As(err, &policyErr) {
			t.Errorf("ValidateRemoteWithPolicy([\"*\"], %q) = %T, want *RemotePolicyError", url, err)
		}
	}
}

// TestRemotePolicyErrorOmitsSecrets pins the diagnostic contract: the error
// names the subject but carries neither the URL nor the patterns, both of
// which can embed credentials that a refusal gets echoed into.
func TestRemotePolicyErrorOmitsSecrets(t *testing.T) {
	const credentialed = "https://x-access-token:s3cret@evil.example.com/attacker/repo.git"
	err := ValidateRemoteWithPolicy([]string{"git+https://github.com/myorg/*", "s3cret-pattern"}, "federation.remote", credentialed)
	if err == nil {
		t.Fatal("ValidateRemoteWithPolicy refused nothing for an out-of-list credentialed URL")
	}
	msg := err.Error()
	for _, secret := range []string{credentialed, "s3cret", "evil.example.com", "x-access-token", "s3cret-pattern"} {
		if strings.Contains(msg, secret) {
			t.Errorf("RemotePolicyError message %q leaks %q", msg, secret)
		}
	}
	if !strings.Contains(msg, "federation.remote") {
		t.Errorf("RemotePolicyError message %q does not name the subject key", msg)
	}
}
