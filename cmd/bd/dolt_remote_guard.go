package main

import (
	"github.com/steveyegge/beads/internal/doltremote"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// doltRemoteMatchesGitOrigin reports whether doltURL refers to the same
// repository as the git origin. Handles git+https://, git+ssh://, SCP-style,
// and plain https:// / ssh:// forms by normalizing both sides with
// doltremote.CanonicalForComparison before comparing.
// Returns false when there is no git origin.
func doltRemoteMatchesGitOrigin(doltURL string) bool {
	originURL, err := gitOriginGetURL()
	if err != nil {
		return false
	}
	return doltremote.CanonicalForComparison(doltURL) == doltremote.CanonicalForComparison(originURL)
}

// doltRemoteAddNeedsGitOriginOverride reports whether bd dolt remote add
// name url needs --allow-git-origin: the URL matches the git origin without
// being the canonical git-backed sync remote — the remote named origin on a
// Git transport, the topology bd itself wires (Dolt data travels on the git
// origin's refs/dolt/data), which is added silently, never refused or warned
// about. An alias pointing at the git origin on any form, and an origin on a
// raw http(s) URL that only addresses a Dolt remote server, still need the
// override (GH#6150).
func doltRemoteAddNeedsGitOriginOverride(name, url string) bool {
	return !doltutil.IsGitSyncRemote(name, url) && doltRemoteMatchesGitOrigin(url)
}
