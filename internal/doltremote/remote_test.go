package doltremote

import (
	"slices"
	"testing"
)

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
		// Dotless host with the only "@" after the colon. The "@" alone used
		// to classify these as SCP-style (host:pa@th -> git+ssh://host/pa@th);
		// the anchored grammar wants user@host or a dotted host before the colon.
		"host:pa@th",
		"alias:repo@v1",
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

func TestNativeSchemesContainsEachNativeScheme(t *testing.T) {
	tests := []string{
		"dolthub://",
		"file://",
		"aws://",
		"gs://",
		// s3 URLs must take the native fast path rather than survive
		// Normalize by falling through past the git heuristics.
		"s3://",
		"git+https://",
		"git+ssh://",
		"git+http://",
		"git+file://",
	}

	for _, scheme := range tests {
		t.Run(scheme, func(t *testing.T) {
			if !slices.Contains(NativeSchemes, scheme) {
				t.Errorf("slices.Contains(NativeSchemes, %q) = false, want true", scheme)
			}
		})
	}
}
