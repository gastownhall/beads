package main

// guardNormalizeURL strips a trailing slash and ".git" suffix for URL comparison.
// This is intentionally simpler than normalizeRemoteURL (which converts git URLs
// to Dolt's git+… format): for the collision check we want two human-readable URLs
// that refer to the same repo to compare equal, regardless of .git/slash variations.
func guardNormalizeURL(url string) string {
	for len(url) > 0 && url[len(url)-1] == '/' {
		url = url[:len(url)-1]
	}
	if len(url) > 4 && url[len(url)-4:] == ".git" {
		url = url[:len(url)-4]
	}
	return url
}

// doltRemoteMatchesGitOrigin reports whether doltURL (after stripping .git/slash)
// matches the git origin URL. Returns false when there is no git origin.
func doltRemoteMatchesGitOrigin(doltURL string) bool {
	originURL, err := gitOriginGetURL()
	if err != nil {
		return false
	}
	return guardNormalizeURL(doltURL) == guardNormalizeURL(originURL)
}
