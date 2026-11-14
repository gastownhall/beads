package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/remotecache"
)

// GH#6233, lint half: `bd config validate` used to match
// federation.allowed-remote-patterns against the raw spelling of
// federation.remote, so a typed https URL failed the lint a runtime gate
// accepted (runtime evaluates the Dolt-normalized destination). The lint now
// calls the same shared gate, and the diagnostic stays secret-free.
func TestAllowedRemotePatternsNormalizedDestinationParity(t *testing.T) {
	const orgPattern = "git+https://github.com/myorg/*"
	const otherOrgPattern = "git+https://github.com/otherorg/*"

	cases := []struct {
		name     string
		remote   string // the configured federation.remote spelling
		patterns string
	}{
		// A typed forge URL and its registered git+ spelling are the same
		// destination; both pass the org pattern at runtime, so both must
		// pass the lint.
		{"typed https spelling passes the org pattern", "https://github.com/myorg/repo", orgPattern},
		{"registered git+ spelling passes the org pattern", "git+https://github.com/myorg/repo", orgPattern},
		// And both fail a pattern for a different organization.
		{"typed https spelling fails another org pattern", "https://github.com/myorg/repo", otherOrgPattern},
		{"registered git+ spelling fails another org pattern", "git+https://github.com/myorg/repo", otherOrgPattern},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := lintSyncConfigForTest(t, tc.remote, tc.patterns)
			lintRefused := allowlistIssue(issues) != ""

			// Parity: the runtime gates give the same (patterns, url) pair
			// the same verdict the lint just gave.
			runtimeErr := remotecache.ValidateRemoteWithPolicy([]string{tc.patterns}, "federation.remote", tc.remote)
			if (runtimeErr != nil) != lintRefused {
				t.Fatalf("lint refused = %v, runtime gate error = %v: the two must agree on %q under %q", lintRefused, runtimeErr, tc.remote, tc.patterns)
			}

			if !lintRefused {
				return
			}
			issue := allowlistIssue(issues)
			if !strings.Contains(issue, "federation.allowed-remote-patterns") {
				t.Errorf("lint issue %q does not name the key", issue)
			}
			if strings.Contains(issue, "github.com") {
				t.Errorf("lint issue %q echoes the destination or the patterns", issue)
			}
		})
	}
}

// TestValidateSyncConfigAllowedRemotePatternsSecretFreeDiagnostics keeps the
// refusal free of the destination even when it carries userinfo: a config
// file is shared and its diagnostics get pasted into tickets.
func TestValidateSyncConfigAllowedRemotePatternsSecretFreeDiagnostics(t *testing.T) {
	issues := lintSyncConfigForTest(t,
		"https://x-access-token:s3cret@evil.example.com/attacker/repo.git",
		"git+https://github.com/myorg/*")
	issue := allowlistIssue(issues)
	if issue == "" {
		t.Fatal("validateSyncConfig did not refuse the out-of-list credentialed remote")
	}
	for _, secret := range []string{"s3cret", "x-access-token", "evil.example.com"} {
		if strings.Contains(issue, secret) {
			t.Errorf("lint issue %q leaks %q", issue, secret)
		}
	}
}

// lintSyncConfigForTest runs validateSyncConfig over a one-file workspace
// holding the given federation.remote and single allowlist pattern.
func lintSyncConfigForTest(t *testing.T, remote, pattern string) []string {
	t.Helper()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configYAML := "prefix: pl\nfederation:\n  remote: " + remote +
		"\n  allowed-remote-patterns:\n    - " + pattern + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return validateSyncConfig(tmpDir)
}

// allowlistIssue returns the allowlist refusal among the lint issues, "" when
// the lint accepted the remote.
func allowlistIssue(issues []string) string {
	if i := slices.IndexFunc(issues, func(s string) bool {
		return strings.Contains(s, "federation.allowed-remote-patterns")
	}); i >= 0 {
		return issues[i]
	}
	return ""
}
