package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/remotecache"
)

// GH#6233: `bd config validate` judges a configured remote the way the
// runtime gates do: under the effective allowlist, against the normalized
// destination (so a typed https://github.com/... URL is the git+https form
// Dolt uses), for sync.remote as well as federation.remote, and its issues
// name the key, never the URL.
func TestAllowedRemotePatternsNormalizedDestinationParity(t *testing.T) {
	cases := []struct {
		name     string
		yaml     string
		env      string
		key      string
		remote   string
		patterns []string
	}{
		{
			name:     "typed https accepted by a git+https pattern",
			key:      "federation.remote",
			remote:   "https://github.com/org/repo",
			patterns: []string{"git+https://github.com/org/*"},
		},
		{
			name:     "typed https refused by a bare https pattern",
			key:      "federation.remote",
			remote:   "https://github.com/org/repo",
			patterns: []string{"https://github.com/org/*"},
		},
		{
			name:     "credentialed remote refused without echoing it",
			key:      "federation.remote",
			remote:   "https://alice:s3cret-token@evil.example.com/x/repo",
			patterns: []string{"dolthub://myorg/*"},
		},
		{
			name:     "sync.remote refused",
			key:      "sync.remote",
			remote:   "https://github.com/other/repo",
			patterns: []string{"dolthub://myorg/*"},
		},
		{
			name:   "policy from the environment",
			env:    "dolthub://myorg/*",
			key:    "federation.remote",
			remote: "dolthub://otherorg/repo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("BD_FEDERATION_ALLOWED_REMOTE_PATTERNS", tc.env)
			repo := t.TempDir()
			beadsDir := filepath.Join(repo, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			yaml := "federation:\n"
			if tc.key == "federation.remote" {
				yaml += "  remote: \"" + tc.remote + "\"\n"
			} else {
				yaml += "  remote: \"dolthub://myorg/repo\"\n"
			}
			if len(tc.patterns) > 0 {
				yaml += "  allowed-remote-patterns:\n"
				for _, p := range tc.patterns {
					yaml += "    - \"" + p + "\"\n"
				}
			}
			if tc.key == "sync.remote" {
				yaml += "sync:\n  remote: \"" + tc.remote + "\"\n"
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(yaml), 0o644); err != nil {
				t.Fatal(err)
			}

			patterns := tc.patterns
			if tc.env != "" {
				patterns = strings.Fields(tc.env)
			}
			runtimeRefuses := remotecache.CheckRemotePolicy(tc.key, tc.remote, patterns) != nil

			var policyIssues []string
			for _, issue := range validateSyncConfig(repo) {
				if strings.Contains(issue, "not permitted") || strings.Contains(issue, "allowed pattern") {
					policyIssues = append(policyIssues, issue)
				}
			}
			if lintRefuses := len(policyIssues) > 0; lintRefuses != runtimeRefuses {
				t.Fatalf("lint refuses = %v (issues %q), runtime refuses = %v: the lint and the gates disagree", lintRefuses, policyIssues, runtimeRefuses)
			}
			for _, issue := range policyIssues {
				if !strings.HasPrefix(issue, tc.key+" ") {
					t.Errorf("issue %q does not name %s", issue, tc.key)
				}
				if strings.Contains(issue, "s3cret") || strings.Contains(issue, "://") {
					t.Errorf("issue %q echoes a URL", issue)
				}
			}
		})
	}
}
