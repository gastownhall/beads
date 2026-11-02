package github

import "testing"

// A pull whose exact external_ref lookup misses falls back to
// ExtractIdentifier; an initialized tracker must not hand back the number of
// an issue in another repository, or it matches (and overwrites) a bead
// linked to that other repository's issue with the same number (GH#7781).
func TestExtractIdentifierScopedToRepository(t *testing.T) {
	tr := &Tracker{client: &Client{BaseURL: "https://api.github.com", Owner: "owner", Repo: "repo"}}
	tests := []struct {
		name string
		ref  string
		want string
	}{
		{"html url", "https://github.com/owner/repo/issues/1313", "1313"},
		{"shorthand", "github:1313", "1313"},
		{"path case", "https://github.com/Owner/Repo/issues/1313", "1313"},
		{"host case", "https://GitHub.com/owner/repo/issues/1313", "1313"},
		{"rest url", "https://api.github.com/repos/owner/repo/issues/1313", "1313"},
		{"foreign owner", "https://github.com/other/repo/issues/1313", ""},
		{"same owner other repo", "https://github.com/owner/other/issues/1313", ""},
		{"foreign host", "https://gitlab.com/owner/repo/issues/1313", ""},
		{"other github host", "https://ghe.example.com/owner/repo/issues/1313", ""},
		{"trailing path", "https://github.com/owner/repo/issues/1313/notes", ""},
		{"pull request url", "https://github.com/owner/repo/pull/1313", ""},
		{"issue zero", "https://github.com/owner/repo/issues/0", ""},
		{"leading-zero shorthand", "github:007", ""},
		{"garbage", "not a ref", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		if got := tr.ExtractIdentifier(tt.ref); got != tt.want {
			t.Errorf("%s: ExtractIdentifier(%q) = %q, want %q", tt.name, tt.ref, got, tt.want)
		}
	}

	// A GitHub Enterprise tracker resolves its own host's HTML and API URLs
	// and rejects github.com, the host of another installation.
	t.Run("enterprise scope", func(t *testing.T) {
		ghes := &Tracker{client: &Client{BaseURL: "https://ghe.example.com/api/v3", Owner: "owner", Repo: "repo"}}
		for ref, want := range map[string]string{
			"https://ghe.example.com/owner/repo/issues/5":              "5",
			"https://ghe.example.com/api/v3/repos/owner/repo/issues/5": "5",
			"https://github.com/owner/repo/issues/5":                   "",
		} {
			if got := ghes.ExtractIdentifier(ref); got != want {
				t.Errorf("ExtractIdentifier(%q) = %q, want %q", ref, got, want)
			}
		}
	})
}