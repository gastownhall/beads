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

// TestNativeSchemesContainsEachNativeScheme guards the documented contents of
// NativeSchemes, nothing more. It deliberately asserts no behavior: the list is
// a fast path with no observable effect, because every entry contains "://",
// isSCPStyleGitURL rejects those outright, and Normalize's closing "return url"
// therefore hands back the same bytes for an entry that is present and for one
// that has been deleted. Removing an entry is caught here, as a documentation
// regression; no assertion over Normalize can catch it while that guard stands.
// The behavior this package actually relies on is pinned by the
// isSCPStyleGitURL tables above and by remote_invariant_test.go.
func TestNativeSchemesContainsEachNativeScheme(t *testing.T) {
	tests := []string{
		"dolthub://",
		"file://",
		"aws://",
		"gs://",
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

// TestFromGitURLLeavesSchemeURLsUnsplit pins the exported entry point against
// the misparse the PR removed from Normalize. Normalize never routes a scheme
// URL here, but FromGitURL is exported and gitURLToDoltRemote (cmd/bd) calls it
// directly, so the refusal has to live in FromGitURL itself rather than in its
// caller.
func TestFromGitURLLeavesSchemeURLsUnsplit(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		// Before the "://" guard this returned git+ssh://s3///bucket/team@prod/db:
		// the first colon sits at index 2 with no "/" before it, so the SCP split
		// fired and fabricated "s3" as the host.
		{"s3://bucket/team@prod/db", "git+s3://bucket/team@prod/db"},
		{
			"s3://bucket/db?endpoint=https://acct.r2.example&region=auto",
			"git+s3://bucket/db?endpoint=https://acct.r2.example&region=auto",
		},
		{"gs://bucket/team@prod/db", "git+gs://bucket/team@prod/db"},
		// az:// and oci:// are deliberately absent from NativeSchemes (#6227).
		// The guard is keyed on "://", not on list membership, so they are
		// protected here exactly as the listed schemes are - which is why
		// completing that list is not what closes this hole.
		{
			"az://account.blob.core.windows.net/container/beads",
			"git+az://account.blob.core.windows.net/container/beads",
		},
		{"oci://registry.example/tenant@prod/db", "git+oci://registry.example/tenant@prod/db"},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := FromGitURL(tt.raw); got != tt.want {
				t.Errorf("FromGitURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestFromGitURLStillConvertsSCPForms is the opposite-polarity twin of
// TestFromGitURLLeavesSchemeURLsUnsplit. The guard added there is scoped to
// "://" on purpose: gating the split on isSCPStyleGitURL instead would also
// stop converting the SCP hosts that predicate deliberately declines to
// classify, and the last three rows are exactly those. They reach FromGitURL
// only through a direct call, never through Normalize, and converting them is
// pre-existing behavior this change must not quietly drop.
func TestFromGitURLStillConvertsSCPForms(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"git@github.com:org/repo.git", "git+ssh://git@github.com/org/repo.git"},
		{"deploy@myserver.com:beads/data", "git+ssh://deploy@myserver.com/beads/data"},
		{"github.com:org/repo.git", "git+ssh://github.com/org/repo.git"},
		// Non-ASCII userinfo: outside scpStyleGitURLPattern's charset.
		{"usér@host.com:path", "git+ssh://usér@host.com/path"},
		// IDN host: same charset limit.
		{"git@bücher.example:repo", "git+ssh://git@bücher.example/repo"},
		// Dotless SSH config alias: no "@", no "." before the colon.
		{"github:org/repo.git", "git+ssh://github/org/repo.git"},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := FromGitURL(tt.raw); got != tt.want {
				t.Errorf("FromGitURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
