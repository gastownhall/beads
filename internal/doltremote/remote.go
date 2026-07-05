package doltremote

import (
	"net/url"
	"strings"
)

// NativeSchemes are URL schemes that Dolt understands natively and should not
// be converted through FromGitURL.
var NativeSchemes = []string{
	"dolthub://",
	"file://",
	"aws://",
	"gs://",
	"git+https://",
	"git+ssh://",
	"git+http://",
	"git+file://",
}

// Normalize converts a remote URL to a Dolt-compatible format.
// Dolt-native URLs (dolthub://, file://, aws://, gs://, git+...) are returned
// as-is. Git URLs (https://, ssh://, git@...) are converted via FromGitURL.
// Unknown schemes are returned as-is and let dolt clone decide.
func Normalize(rawURL string) string {
	for _, scheme := range NativeSchemes {
		if strings.HasPrefix(rawURL, scheme) {
			return rawURL
		}
	}
	if strings.HasPrefix(rawURL, "https://") || strings.HasPrefix(rawURL, "http://") ||
		strings.HasPrefix(rawURL, "ssh://") {
		return FromGitURL(rawURL)
	}
	if isWindowsDrivePath(rawURL) {
		return FromGitURL(rawURL)
	}
	if isSCPStyleGitURL(rawURL) {
		return FromGitURL(rawURL)
	}
	return rawURL
}

// FromGitURL converts a git remote URL to Dolt's remote format.
// HTTPS URLs get "git+" prefix: https://... -> git+https://...
// SCP-style SSH URLs are converted: git@host:path -> git+ssh://git@host/path
// SSH URLs get "git+" prefix: ssh://... -> git+ssh://...
// URLs that already have "git+" prefix are returned as-is.
func FromGitURL(rawURL string) string {
	if strings.HasPrefix(rawURL, "git+") {
		return rawURL
	}
	if strings.HasPrefix(rawURL, "https://") || strings.HasPrefix(rawURL, "http://") {
		return "git+" + rawURL
	}
	if strings.HasPrefix(rawURL, "ssh://") {
		return "git+" + rawURL
	}
	if isWindowsDrivePath(rawURL) {
		return "git+" + rawURL
	}
	if idx := strings.Index(rawURL, ":"); idx > 0 && !strings.Contains(rawURL[:idx], "/") {
		return "git+ssh://" + rawURL[:idx] + "/" + rawURL[idx+1:]
	}
	return "git+" + rawURL
}

func isSCPStyleGitURL(rawURL string) bool {
	if idx := strings.Index(rawURL, ":"); idx > 0 && !strings.Contains(rawURL[:idx], "/") {
		return true
	}
	return false
}

// CanonicalForComparison returns a form of url suitable for equality checks
// between URLs that refer to the same repository but may use different schemes
// or representations. Concretely:
//   - https://github.com/org/repo.git  ≡  git+https://github.com/org/repo.git
//   - git@github.com:org/repo.git      ≡  git+ssh://git@github.com/org/repo.git
//
// Algorithm: normalize to Dolt's git+ prefix form, strip credentials, lowercase
// the host, and strip trailing slashes and .git.
func CanonicalForComparison(rawURL string) string {
	rawURL = Normalize(rawURL)
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Host != "" {
		parsed.User = nil
		parsed.Host = strings.ToLower(parsed.Host)
		rawURL = parsed.String()
	}
	rawURL = strings.TrimRight(rawURL, "/")
	rawURL = strings.TrimSuffix(rawURL, ".git")
	return rawURL
}

func isWindowsDrivePath(path string) bool {
	if len(path) < 3 || path[1] != ':' {
		return false
	}
	drive := path[0]
	return ((drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')) &&
		(path[2] == '/' || path[2] == '\\')
}
