package main

import "github.com/steveyegge/beads/internal/doltremote"

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
