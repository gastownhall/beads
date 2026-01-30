package main

import (
	"testing"
)

// TestIsPushPermissionDenied tests the isPushPermissionDenied function
// with various error messages from different Git hosting providers.
func TestIsPushPermissionDenied(t *testing.T) {
	tests := map[string]struct {
		output   string
		expected bool
	}{
		// GitHub errors
		"github_403": {
			output:   "remote: Permission to owner/repo.git denied to user.\nfatal: unable to access 'https://github.com/owner/repo.git/': The requested URL returned error: 403",
			expected: true,
		},
		"github_permission_denied": {
			output:   "ERROR: Permission to owner/repo.git denied to user.",
			expected: true,
		},
		"github_ssh_permission": {
			output:   "Permission denied (publickey).\nfatal: Could not read from remote repository.",
			expected: true,
		},

		// GitLab errors
		"gitlab_not_allowed": {
			output:   "remote: GitLab: You are not allowed to push code to this project.\nTo gitlab.com:owner/repo.git\n! [remote rejected] main -> main (pre-receive hook declined)",
			expected: true,
		},
		"gitlab_permission": {
			output:   "remote: You are not allowed to push code to protected branches on this project.",
			expected: true,
		},

		// Bitbucket errors
		"bitbucket_403": {
			output:   "remote: HTTP Basic: Access denied\nfatal: Authentication failed for 'https://bitbucket.org/owner/repo.git/'",
			expected: true,
		},

		// Generic permission errors
		"generic_permission_denied": {
			output:   "fatal: remote error: permission denied",
			expected: true,
		},
		"generic_could_not_read": {
			output:   "fatal: Could not read from remote repository.\n\nPlease make sure you have the correct access rights\nand the repository exists.",
			expected: true,
		},
		"generic_authentication_failed": {
			output:   "fatal: Authentication failed for 'https://example.com/repo.git/'",
			expected: true,
		},
		"generic_unable_to_access": {
			output:   "fatal: unable to access 'https://example.com/repo.git/': URL using bad/illegal format or missing URL",
			expected: true,
		},

		// Non-permission errors (should NOT trigger)
		"fetch_first": {
			output:   "To github.com:owner/repo.git\n! [rejected]        main -> main (fetch first)\nerror: failed to push some refs",
			expected: false,
		},
		"non_fast_forward": {
			output:   "To github.com:owner/repo.git\n! [rejected]        main -> main (non-fast-forward)\nerror: failed to push some refs",
			expected: false,
		},
		"network_error": {
			output:   "fatal: unable to connect to github.com:\nCould not connect to server",
			expected: false,
		},
		"no_upstream": {
			output:   "fatal: The current branch main has no upstream branch.\nTo push the current branch and set the remote as upstream, use\n\n    git push --set-upstream origin main",
			expected: false,
		},
		"branch_protected": {
			output:   "remote: error: GH006: Protected branch update failed for refs/heads/main.",
			expected: false,
		},
		"empty_output": {
			output:   "",
			expected: false,
		},
		"success_output": {
			output:   "To github.com:owner/repo.git\n   abc123..def456  main -> main",
			expected: false,
		},

		// Edge cases
		"case_insensitive_403": {
			output:   "The Requested URL Returned Error: 403",
			expected: true,
		},
		"case_insensitive_permission": {
			output:   "PERMISSION DENIED",
			expected: true,
		},
		"partial_match_403_in_sha": {
			// This is a tricky case - "403" in the middle of a hash
			// We accept this false positive since it's rare and better to over-detect
			output:   "commit 403abc123 pushed successfully",
			expected: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := isPushPermissionDenied(tt.output)
			if got != tt.expected {
				t.Errorf("isPushPermissionDenied(%q) = %v, want %v", tt.output, got, tt.expected)
			}
		})
	}
}

// TestIsPushPermissionDenied_ProviderSpecific tests specific provider error formats
// to ensure we handle the most common cases from major Git hosting platforms.
func TestIsPushPermissionDenied_ProviderSpecific(t *testing.T) {
	// These are real error messages captured from various providers
	providerErrors := []struct {
		name     string
		provider string
		output   string
	}{
		{
			name:     "GitHub HTTPS with PAT denied",
			provider: "GitHub",
			output:   "remote: Permission to steveyegge/beads.git denied to contributor.\nfatal: unable to access 'https://github.com/steveyegge/beads.git/': The requested URL returned error: 403",
		},
		{
			name:     "GitHub SSH key not authorized",
			provider: "GitHub",
			output:   "ERROR: Permission to owner/repo.git denied to user.\nfatal: Could not read from remote repository.\n\nPlease make sure you have the correct access rights\nand the repository exists.",
		},
		{
			name:     "GitLab protected branch",
			provider: "GitLab",
			output:   "remote: GitLab: You are not allowed to push code to this project.\nTo gitlab.com:owner/repo.git\n ! [remote rejected] main -> main (pre-receive hook declined)\nerror: failed to push some refs to 'gitlab.com:owner/repo.git'",
		},
		{
			name:     "Bitbucket access denied",
			provider: "Bitbucket",
			output:   "remote: You are not allowed to push to this repository.\nfatal: Could not read from remote repository.",
		},
	}

	for _, tc := range providerErrors {
		t.Run(tc.name, func(t *testing.T) {
			if !isPushPermissionDenied(tc.output) {
				t.Errorf("%s error not detected as permission denied:\n%s", tc.provider, tc.output)
			}
		})
	}
}
