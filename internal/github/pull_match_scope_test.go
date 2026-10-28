package github

import (
	"testing"

	"github.com/steveyegge/beads/internal/tracker"
)

// TestPullFallbackKeyIsRepoScoped reproduces gastownhall/beads#7781: when a
// pull's exact external_ref lookup misses, the engine falls back to the
// tracker's MatchKey (or ExtractIdentifier when it has none). A bead linked to
// issue 42 of acme/repo-a must not share that key with issue 42 of
// acme/repo-b, or pulling repo-b's #42 overwrites the repo-a bead.
func TestPullFallbackKeyIsRepoScoped(t *testing.T) {
	tr := &Tracker{client: &Client{BaseURL: DefaultBaseURL, Owner: "acme", Repo: "repo-b"}}
	// Mirrors tracker.Engine.matchKey.
	fallbackKey := func(ref string) string {
		if mk, ok := any(tr).(tracker.MatchKeyer); ok {
			return mk.MatchKey(ref)
		}
		return tr.ExtractIdentifier(ref)
	}

	pulledRef := tr.BuildExternalRef(&tracker.TrackerIssue{
		Identifier: "42",
		URL:        "https://github.com/acme/repo-b/issues/42",
	})
	localRef := "https://github.com/acme/repo-a/issues/42"

	pulledKey := fallbackKey(pulledRef)
	if pulledKey == "" {
		t.Fatalf("pulled ref %q has no fallback key", pulledRef)
	}
	if localKey := fallbackKey(localRef); localKey == pulledKey {
		t.Fatalf("repo-a bead %q and pulled repo-b issue %q share fallback key %q; pull would overwrite the repo-a bead",
			localRef, pulledRef, localKey)
	}
}
