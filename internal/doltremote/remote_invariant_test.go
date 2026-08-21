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
