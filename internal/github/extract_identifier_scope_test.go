package github

import "testing"

// A pull whose exact external_ref lookup misses falls back to
// ExtractIdentifier; an initialized tracker must not hand back the number of
// an issue in another repository, or it matches (and overwrites) a bead
// linked to that other repository's issue with the same number (GH#7781).
func TestExtractIdentifierScopedToRepository(t *testing.T) {
	tr := &Tracker{client: &Client{BaseURL: "https://api.github.com", Owner: "owner", Repo: "repo"}}
	tests := []struct {
		ref  string
		want string
	}{
		{"https://github.com/owner/repo/issues/1313", "1313"},
		{"github:1313", "1313"},
		{"https://github.com/other/repo/issues/1313", ""},
		{"https://github.com/owner/other/issues/1313", ""},
		// Spellings RefScope accepts for the configured repository.
		{"https://github.com/Owner/REPO/issues/1313", "1313"},
		{"https://api.github.com/repos/owner/repo/issues/1313", "1313"},
		{"https://GitHub.com/owner/repo/issues/1313", "1313"},
		// Foreign hosts and malformed paths.
		{"https://ghe.example.com/owner/repo/issues/1313", ""},
		{"https://gitlab.com/owner/repo/-/issues/1313", ""},
		{"https://github.com/owner/repo/issues/1313/comments", ""},
		{"https://github.com/owner/repo/issues/abc", ""},
		{"https://github.com/owner/repo/pull/1313", ""},
		{"not-a-url", ""},
		// The shorthand arm is unchanged (leading zeros are rejected by the
		// shorthand pattern itself, as before).
		{"github:681509", "681509"},
		{"github:0", ""},
		{"github:01313", ""},
	}
	for _, tt := range tests {
		if got := tr.ExtractIdentifier(tt.ref); got != tt.want {
			t.Errorf("ExtractIdentifier(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

// TestExtractIdentifierScopedToEnterpriseRepository covers a GitHub
// Enterprise client, whose API and HTML URLs share one host.
func TestExtractIdentifierScopedToEnterpriseRepository(t *testing.T) {
	tr := &Tracker{client: &Client{BaseURL: "https://ghe.example.com/api/v3", Owner: "owner", Repo: "repo"}}
	tests := []struct {
		ref  string
		want string
	}{
		{"https://ghe.example.com/owner/repo/issues/42", "42"},
		{"https://ghe.example.com/api/v3/repos/owner/repo/issues/42", "42"},
		{"https://GHE.example.com/OWNER/repo/issues/42", "42"},
		{"https://github.com/owner/repo/issues/42", ""},
		{"https://ghe.example.com/owner/other/issues/42", ""},
		{"https://ghe2.example.com/owner/repo/issues/42", ""},
		{"github:42", "42"},
	}
	for _, tt := range tests {
		if got := tr.ExtractIdentifier(tt.ref); got != tt.want {
			t.Errorf("ExtractIdentifier(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}
