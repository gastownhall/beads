package remotecache

import (
	"errors"
	"strings"
	"testing"
)

// GH#6233: an absent or empty allowlist is no policy at all. CheckRemotePolicy
// must refuse nothing, so every remote a workspace without patterns could use
// before stays usable, while the structural checks those call sites already
// run (ValidateRemoteURL) keep refusing what they refused.
func TestCheckRemotePolicyEmptyListIsNoPolicy(t *testing.T) {
	for _, patterns := range [][]string{nil, {}} {
		for _, u := range []string{
			"dolthub://org/repo",
			"https://github.com/org/repo",
			"git@github.com:org/repo.git",
			"file:///tmp/remote",
			"",
		} {
			if err := CheckRemotePolicy(RemoteSubject("origin"), u, patterns); err != nil {
				t.Errorf("CheckRemotePolicy(%q, %#v) = %v, want nil (no allowlist)", u, patterns, err)
			}
		}
	}
	// Structural validation is unchanged and still refuses option-shaped URLs.
	if err := ValidateRemoteURL("--upload-pack=touch /tmp/x"); err == nil {
		t.Fatal("ValidateRemoteURL accepted an option-shaped URL; structural validation must stay intact")
	}
}

func TestCheckRemotePolicyTable(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		patterns []string
		allowed  bool
	}{
		{"native match", "dolthub://myorg/beads", []string{"dolthub://myorg/*"}, true},
		{"native mismatch", "dolthub://other/beads", []string{"dolthub://myorg/*"}, false},
		{"file match", "file:///srv/remotes/beads", []string{"file:///srv/remotes/*"}, true},
		{"star does not cross slash", "dolthub://myorg/team/beads", []string{"dolthub://myorg/*"}, false},
		{"second entry matches", "dolthub://myorg/beads", []string{"s3://*", "dolthub://myorg/*"}, true},
		{"malformed entry matches nothing", "dolthub://myorg/beads", []string{"dolthub://myorg/["}, false},
		{"malformed entry beside a valid one", "dolthub://myorg/beads", []string{"dolthub://myorg/[", "dolthub://myorg/*"}, true},
		{"blank-only list denies all", "dolthub://myorg/beads", []string{""}, false},
		{"typed forge URL is evaluated in git+ form", "https://github.com/org/repo", []string{"git+https://github.com/org/*"}, true},
		{"raw https pattern does not match the git+ destination", "https://github.com/org/repo", []string{"https://github.com/org/*"}, false},
		{"registered git+ form", "git+https://github.com/org/repo", []string{"git+https://github.com/org/*"}, true},
		{"other org refused", "git+https://github.com/evil/repo", []string{"git+https://github.com/org/*"}, false},
		{"scp form normalized", "git@github.com:org/repo.git", []string{"git+ssh://git@github.com/org/*"}, true},
		{"ssh form normalized", "ssh://git@github.com/org/repo.git", []string{"git+ssh://git@github.com/org/*"}, true},
		{"unresolved URL refused", "", []string{"dolthub://*"}, false},
		{"structurally invalid refused", "dolthub://\x00evil", []string{"*"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckRemotePolicy(RemoteSubject("origin"), tt.url, tt.patterns)
			if tt.allowed {
				if err != nil {
					t.Fatalf("CheckRemotePolicy(%q, %q) = %v, want allowed", tt.url, tt.patterns, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckRemotePolicy(%q, %q) = nil, want a refusal", tt.url, tt.patterns)
			}
			if !errors.Is(err, ErrRemoteNotAllowed) {
				t.Fatalf("refusal %v does not wrap ErrRemoteNotAllowed", err)
			}
		})
	}
}

// A refusal names the subject and the key, never the URL (which can carry a
// token) or the patterns.
func TestCheckRemotePolicyDiagnosticIsSecretFree(t *testing.T) {
	const secretURL = "https://x-access-token:s3cr3t@github.com/evil/repo"
	err := CheckRemotePolicy(RemoteSubject("origin"), secretURL, []string{"git+https://github.com/private-org/*"})
	if err == nil {
		t.Fatal("want a refusal")
	}
	msg := err.Error()
	for _, leak := range []string{"s3cr3t", "x-access-token", "evil/repo", "private-org"} {
		if strings.Contains(msg, leak) {
			t.Errorf("refusal %q leaks %q", msg, leak)
		}
	}
	for _, want := range []string{`remote "origin"`, AllowedRemotePatternsKey} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not name %q", msg, want)
		}
	}
}
