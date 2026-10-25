package doltremote

import (
	"regexp"
	"strings"
)

// NativeSchemes are URL schemes that Dolt understands natively and should not
// be converted through FromGitURL.
//
// This list is deliberately narrower than remotecache.remoteSchemes, the repo's
// fuller scheme authority: az:// and oci:// are absent here. For az:// that gap
// is tracked in #6227, which also covers dolt.IsBackupURL - the site where the
// gap actually changes behavior - so completing this list on its own would
// close only half of it.
//
// Membership is not load-bearing either way. Every entry contains "://", which
// isSCPStyleGitURL rejects outright, so Normalize's closing "return url"
// already hands back any of these URLs byte-identical whether or not they are
// listed. The list records intent; the "://" guard is what preserves the bytes.
var NativeSchemes = []string{
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

// Normalize converts a remote URL to a Dolt-compatible format.
// Dolt-native URLs (dolthub://, file://, aws://, gs://, s3://, git+...) are returned
// as-is. Git URLs (https://, ssh://, git@...) are converted via FromGitURL.
// Unknown schemes are returned as-is and let dolt clone decide.
//
// The NativeSchemes loop is only a shortcut. What actually keeps every scheme
// URL intact is the "://" guard in isSCPStyleGitURL: it stops a path or query
// containing "@" from being read as an SCP-style git remote, so an unlisted
// scheme still falls through to the closing "return url".
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
//
// The SCP split obeys the same "://" rule as isSCPStyleGitURL, so a scheme URL
// handed to this exported entry point directly - rather than routed here by
// Normalize - cannot be rebuilt into git+ssh://s3///bucket/team@prod/db. The
// narrower shape test stays in isSCPStyleGitURL, which is what Normalize
// classifies on. Gating here on that predicate instead would additionally stop
// converting the SCP hosts it deliberately declines to classify (IDN hosts,
// non-ASCII userinfo, dotless aliases) - measured at 8 further inputs changed
// over the package's own corpus, none of them the bug this guard closes.
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
	if !strings.Contains(url, "://") {
		if idx := strings.Index(url, ":"); idx > 0 && !strings.Contains(url[:idx], "/") {
			return "git+ssh://" + url[:idx] + "/" + url[idx+1:]
		}
	}
	return "git+" + url
}

// scpStyleGitURLPattern matches SCP-style git remotes. Its first alternative
// (user@host) is byte-identical to remotecache.gitSSHPattern in
// internal/remotecache/url.go; this pattern is a superset by exactly its second
// alternative, the user-less dotted-host form ("github.com:org/repo.git"),
// which remotecache.IsRemoteURL intentionally reports as false. The two are
// otherwise one grammar with no cross-reference, so keep them in sync; the
// divergence is pinned by TestSCPGrammarDivergesFromRemotecacheOnlyOnDottedHost
// here and by TestIsRemoteURLRejectsUserlessDottedHost on the remotecache side.
var scpStyleGitURLPattern = regexp.MustCompile(`^(?:[a-zA-Z0-9._-]+@[a-zA-Z0-9][a-zA-Z0-9._-]*|[a-zA-Z0-9][a-zA-Z0-9._-]*\.[a-zA-Z0-9._-]*):[^\x00-\x1f\x7f]+$`)

// isSCPStyleGitURL reports whether url looks like an SCP-style git remote:
// user@host:path, or host:path when the pre-colon token contains a "." so a
// Windows drive letter ("C:foo") is not mistaken for a host. Anything that
// carries a "://" scheme is never SCP-style, whatever else it contains, so a
// query or path with "@" in it cannot turn an s3:// URL into a git remote.
//
// Known limitations: git also accepts dotless SSH config aliases
// ("github:org/repo.git"); those pass through unconverted and will not
// canonically match an equivalent git+ssh:// form. User and host are limited
// to [a-zA-Z0-9._-], so non-ASCII userinfo and IDN hosts are not recognized
// either.
func isSCPStyleGitURL(url string) bool {
	return !strings.Contains(url, "://") && scpStyleGitURLPattern.MatchString(url)
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

// stripCredentialsAndFoldHostCase removes embedded userinfo from the
// authority of a scheme://authority/path URL and lowercases the host. The
// scheme and path are left untouched. URLs without "://" (e.g. an unknown
// scheme passthrough) are returned unchanged rather than risking corruption.
//
// For HTTP(S) authorities, user[:pass]@ is transport credentials, not an
// account selector, and is stripped unconditionally. For SSH authorities
// (ssh://, git+ssh://) the userinfo selects the remote account or home
// directory - alice@host and bob@host on the same host are different
// endpoints - so it is preserved, except the conventional "git@" user, which
// git hosting services treat as the default account and which
// FromGitURL/isSCPStyleGitURL already fold bare host:path forms to (see
// CanonicalForComparison's github.com:org/repo.git ≡ git@github.com:org/repo.git
// example).
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
	scheme := url[:schemeEnd]
	authorityStart := schemeEnd + len("://")
	rest := url[authorityStart:]

	authority := rest
	tail := ""
	if slashIdx := strings.Index(rest, "/"); slashIdx >= 0 {
		authority = rest[:slashIdx]
		tail = rest[slashIdx:]
	}

	if atIdx := strings.LastIndex(authority, "@"); atIdx >= 0 {
		if !isSSHScheme(scheme) || authority[:atIdx] == "git" {
			authority = authority[atIdx+1:]
		}
	}
	authority = strings.ToLower(authority)

	return url[:authorityStart] + authority + tail
}

// isSSHScheme reports whether scheme (the part of a URL before "://")
// identifies an SSH transport, where userinfo selects the remote account
// rather than carrying transport credentials.
func isSSHScheme(scheme string) bool {
	return scheme == "ssh" || scheme == "git+ssh"
}

func isWindowsDrivePath(path string) bool {
	if len(path) < 3 || path[1] != ':' {
		return false
	}
	drive := path[0]
	return ((drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')) &&
		(path[2] == '/' || path[2] == '\\')
}
