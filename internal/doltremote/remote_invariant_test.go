package doltremote_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/doltremote"
	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

func TestS3RemotePreservesInternalBoundaries(t *testing.T) {
	// The "@" in the path is what the old SCP heuristic tripped on.
	const raw = "s3://bucket/team@prod/db?endpoint=https://acct.r2.example&region=auto&path-style=true"

	if err := remotecache.ValidateRemoteURL(raw); err != nil {
		t.Fatalf("ValidateRemoteURL(%q): %v", raw, err)
	}
	if got := doltremote.Normalize(raw); got != raw {
		t.Errorf("Normalize(%q) = %q, want unchanged", raw, got)
	}
	if doltutil.IsGitProtocolURL(raw) {
		t.Errorf("IsGitProtocolURL(%q) = true, want false", raw)
	}
}

// TestSCPGrammarDivergesFromRemotecacheOnlyOnDottedHost pins the one form on
// which doltremote's SCP grammar and remotecache's intentionally disagree.
//
// scpStyleGitURLPattern's first alternative is byte-identical to
// remotecache.gitSSHPattern; the whole pattern is a superset by exactly its
// second alternative, the user-less dotted-host form. Neither file referenced
// the other before this test, so silent drift was one edit away in either. This
// asserts the relationship through each package's public behavior rather than
// by comparing the two regexes, which would just be a third copy of the grammar.
func TestSCPGrammarDivergesFromRemotecacheOnlyOnDottedHost(t *testing.T) {
	// The divergence itself: doltremote converts it, remotecache does not
	// classify it as a remote URL at all.
	const dottedHost = "github.com:org/repo.git"
	if got, want := doltremote.Normalize(dottedHost), "git+ssh://github.com/org/repo.git"; got != want {
		t.Errorf("Normalize(%q) = %q, want %q", dottedHost, got, want)
	}
	if remotecache.IsRemoteURL(dottedHost) {
		t.Errorf("IsRemoteURL(%q) = true, want false - remotecache omits the dotted-host alternative", dottedHost)
	}

	// Everywhere else the two grammars agree, which is what makes the line
	// above the *only* divergence rather than one of several.
	agree := []string{
		"git@github.com:org/repo.git",
		"deploy@myserver.com:beads/data",
		"user.name@host.com:path",
	}
	for _, raw := range agree {
		t.Run(raw, func(t *testing.T) {
			if !remotecache.IsRemoteURL(raw) {
				t.Errorf("IsRemoteURL(%q) = false, want true", raw)
			}
			if got := doltremote.Normalize(raw); got == raw {
				t.Errorf("Normalize(%q) returned it unconverted, want an SCP conversion", raw)
			}
		})
	}

	// And neither treats these as SCP-style git remotes.
	reject := []string{
		"host:pa@th",
		"s3://bucket/team@prod/db",
	}
	for _, raw := range reject {
		t.Run(raw, func(t *testing.T) {
			if got := doltremote.Normalize(raw); got != raw {
				t.Errorf("Normalize(%q) = %q, want unchanged", raw, got)
			}
		})
	}
}
