package doltremote

import "strings"

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
func Normalize(url string) string {
	for _, scheme := range NativeSchemes {
		if strings.HasPrefix(url, scheme) {
			return url
		}
	}
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") ||
		strings.HasPrefix(url, "ssh://") {
		return FromGitURL(url)
	}
	if isWindowsDrivePath(url) {
		return FromGitURL(url)
	}
	if isSCPStyleGitURL(url) {
		return FromGitURL(url)
	}
	return url
}

// FromGitURL converts a git remote URL to Dolt's remote format.
// HTTPS URLs get "git+" prefix: https://... -> git+https://...
// SCP-style SSH URLs are converted: git@host:path -> git+ssh://git@host/path
// SSH URLs get "git+" prefix: ssh://... -> git+ssh://...
// URLs that already have "git+" prefix are returned as-is.
func FromGitURL(url string) string {
	if strings.HasPrefix(url, "git+") {
		return url
	}
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		return "git+" + url
	}
	if strings.HasPrefix(url, "ssh://") {
		return "git+" + url
	}
	if isWindowsDrivePath(url) {
		return "git+" + url
	}
	if idx := strings.Index(url, ":"); idx > 0 && !strings.Contains(url[:idx], "/") {
		return "git+ssh://" + url[:idx] + "/" + url[idx+1:]
	}
	return "git+" + url
}

// isSCPStyleGitURL reports whether url looks like an SCP-style git remote:
// either the classic user form (git@host:path) or the user-less form
// (host:path). The user-less form is only recognized when the pre-colon
// token looks like a hostname (contains a ".") so that dotless tokens such
// as a Windows drive letter ("C:foo") are not mistaken for a host.
func isSCPStyleGitURL(url string) bool {
	idx := strings.Index(url, ":")
	if idx <= 0 || strings.Contains(url[:idx], "/") {
		return false
	}
	if strings.Contains(url, "@") {
		return true
	}
	return strings.Contains(url[:idx], ".")
}

// CanonicalForComparison returns a form of url suitable for equality checks
// between URLs that refer to the same repository but may use different schemes,
// representations, host casing, or embedded credentials. Concretely:
//   - https://github.com/org/repo.git       ≡  git+https://github.com/org/repo.git
//   - git@github.com:org/repo.git           ≡  git+ssh://git@github.com/org/repo.git
//   - github.com:org/repo.git               ≡  git@github.com:org/repo.git
//   - https://GitHub.com/org/repo           ≡  https://github.com/org/repo
//   - https://user:pass@github.com/org/repo ≡  https://github.com/org/repo
//
// http and https are kept distinct - scheme is never folded.
//
// Algorithm: normalize to Dolt's git+ prefix form, strip trailing slashes and
// .git, then strip embedded user[:pass]@ credentials and lowercase the host.
func CanonicalForComparison(url string) string {
	url = Normalize(url)
	url = strings.TrimRight(url, "/")
	url = strings.TrimSuffix(url, ".git")
	url = stripCredentialsAndFoldHostCase(url)
	return url
}

// stripCredentialsAndFoldHostCase removes any embedded user[:pass]@ from the
// authority of a scheme://authority/path URL and lowercases the host. The
// scheme and path are left untouched. URLs without "://" (e.g. an unknown
// scheme passthrough) are returned unchanged rather than risking corruption.
//
// Case is folded for the authority of every scheme://... form. This is correct
// for DNS hosts (git+https/git+ssh/http/https). For native non-DNS schemes
// (dolthub://, aws://) the authority is a case-sensitive identifier, so folding
// it is technically lossy, but the current callers only compare a Dolt remote
// against a git origin - which always canonicalizes to git+https/git+ssh - so a
// folded native-scheme authority can never string-equal it and no false-positive
// collision can occur.
func stripCredentialsAndFoldHostCase(url string) string {
	schemeEnd := strings.Index(url, "://")
	if schemeEnd < 0 {
		return url
	}
	authorityStart := schemeEnd + len("://")
	rest := url[authorityStart:]

	authority := rest
	tail := ""
	if slashIdx := strings.Index(rest, "/"); slashIdx >= 0 {
		authority = rest[:slashIdx]
		tail = rest[slashIdx:]
	}

	if atIdx := strings.LastIndex(authority, "@"); atIdx >= 0 {
		authority = authority[atIdx+1:]
	}
	authority = strings.ToLower(authority)

	return url[:authorityStart] + authority + tail
}

func isWindowsDrivePath(path string) bool {
	if len(path) < 3 || path[1] != ':' {
		return false
	}
	drive := path[0]
	return ((drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')) &&
		(path[2] == '/' || path[2] == '\\')
}
