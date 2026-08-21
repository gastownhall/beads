package doltremote

import "testing"

func TestIsSCPStyleGitURLRecognizesValidForms(t *testing.T) {
	tests := []string{
		"git@github.com:org/repo.git",
		"deploy@myserver.com:beads/data",
		"git@github:org/repo.git",
		"github.com:org/repo.git",
		// Dots in the user token are inside the accepted charset.
		"user.name@host.com:path",
	}

	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if !isSCPStyleGitURL(raw) {
				t.Errorf("isSCPStyleGitURL(%q) = false, want true", raw)
			}
		})
	}
}

func TestIsSCPStyleGitURLRejectsNonSCPInputs(t *testing.T) {
	tests := []string{
		"s3://bucket/team@prod/beads",
		"s3://bucket/db?endpoint=https://minio.local/api@v1",
		`C:\Users\alice\beads`,
		"C:/Users/alice/beads",
		// Empty path. The "@" alone used to classify this as SCP-style; the
		// anchored grammar requires at least one path character.
		"git@host.com:",
		// Non-ASCII userinfo is outside [a-zA-Z0-9._-]; the URL passes
		// through unconverted instead of being rewritten to git+ssh://.
		"usér@host.com:path",
		// IDN host, same charset limit.
		"git@bücher.example:repo",
	}

	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if isSCPStyleGitURL(raw) {
				t.Errorf("isSCPStyleGitURL(%q) = true, want false", raw)
			}
		})
	}
}
