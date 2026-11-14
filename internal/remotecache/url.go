package remotecache

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/doltremote"
)

// remoteSchemes lists URL scheme prefixes recognized as dolt remote URLs.
var remoteSchemes = []string{
	"dolthub://",
	"gs://",
	"s3://",
	"aws://",
	"az://",
	"oci://",
	"file://",
	"https://",
	"http://",
	"ssh://",
	"git://",
	"git+ssh://",
	"git+https://",
	"git+http://",
	"git+file://",
}

// allowedSchemes is the set of recognized URL schemes for validation.
var allowedSchemes = map[string]bool{
	"dolthub":   true,
	"gs":        true,
	"s3":        true,
	"aws":       true,
	"az":        true,
	"oci":       true,
	"file":      true,
	"https":     true,
	"http":      true,
	"ssh":       true,
	"git":       true,
	"git+ssh":   true,
	"git+https": true,
	"git+http":  true,
	"git+file":  true,
}

// gitSSHPattern matches SCP-style git remote URLs (user@host:path).
// The path portion excludes control characters (0x00-0x1f, 0x7f).
//
// This is byte-identical to the first alternative of
// doltremote.scpStyleGitURLPattern in internal/doltremote/remote.go. That one
// is a superset: it also accepts the user-less dotted-host form
// ("github.com:org/repo.git"), which IsRemoteURL intentionally reports as false
// (pinned by TestIsRemoteURLRejectsUserlessDottedHost). The two are one grammar
// maintained in two places, so keep them in sync.
var gitSSHPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+@[a-zA-Z0-9][a-zA-Z0-9._-]*:[^\x00-\x1f\x7f]+$`)

// validRemoteNameRegex matches valid remote names: starts with a letter,
// contains only alphanumeric characters, hyphens, and underscores.
// Aligned with peer-name validation in credentials.go.
var validRemoteNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// IsRemoteURL returns true if s looks like a dolt remote URL rather than
// a local filesystem path. Recognized schemes: dolthub://, https://, http://,
// s3://, gs://, az://, file://, ssh://, git+ssh://, git+https://, and SCP-style
// git@host:path.
func IsRemoteURL(s string) bool {
	for _, scheme := range remoteSchemes {
		if strings.HasPrefix(s, scheme) {
			return true
		}
	}
	return gitSSHPattern.MatchString(s)
}

// The parts of a remote URL that OptionShapedPart names.
const (
	OptionShapedURL  = "URL"
	OptionShapedUser = "user name"
	OptionShapedHost = "host"
)

// OptionShapedPart names the part of remote that a program the URL reaches
// would read as a command-line option, or returns "" when no part would.
// Remote URLs come from committed configuration (sync.remote, routing.*,
// repos.additional, federation.remote), so a cloned repository must not be
// able to pick an option for git or ssh:
//
//   - OptionShapedURL: the URL, ignoring surrounding space, or what follows
//     a git+ prefix, starts with "-". git reads such an argument as an
//     option (--upload-pack=<command>, -u<command>).
//   - OptionShapedUser, OptionShapedHost: the user name or host of an ssh://
//     or git+ssh:// URL starts with "-". Dolt's ssh:// transport runs
//     `ssh [-p port] [user@]host <command>` with nothing that ends ssh's
//     options before the destination, so ssh reads -oProxyCommand=<command>
//     there as an option, and an ssh without OpenSSH 9.6's host name check
//     (CVE-2023-51385) runs the command. The user name is checked
//     percent-decoded, the form Dolt hands to ssh.
//
// No usable remote has such a part: a DNS host name never starts with "-",
// and OpenSSH refuses a user name that does.
func OptionShapedPart(remote string) string {
	s := strings.TrimSpace(remote)
	if strings.HasPrefix(s, "-") {
		return OptionShapedURL
	}
	if len(s) >= len("git+") && strings.EqualFold(s[:len("git+")], "git+") {
		s = strings.TrimSpace(s[len("git+"):])
		if strings.HasPrefix(s, "-") {
			return OptionShapedURL
		}
	}
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok || !strings.EqualFold(strings.TrimSpace(scheme), "ssh") {
		return ""
	}
	// Split the authority as net/url does: it ends at the first "/", "?" or
	// "#", and the user information ends at its last "@".
	authority := rest
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	host := authority
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		user, _, _ := strings.Cut(authority[:at], ":")
		if optionShapedToken(user) {
			return OptionShapedUser
		}
		if decoded, err := url.PathUnescape(user); err == nil && optionShapedToken(decoded) {
			return OptionShapedUser
		}
		host = authority[at+1:]
	}
	if optionShapedToken(strings.TrimPrefix(strings.TrimSpace(host), "[")) {
		return OptionShapedHost
	}
	// Dolt parses the URL with net/url; ask it too, so no spelling it
	// accepts can name a user or host the split above did not see.
	if u, err := url.Parse(s); err == nil {
		if u.User != nil && optionShapedToken(u.User.Username()) {
			return OptionShapedUser
		}
		if optionShapedToken(u.Hostname()) {
			return OptionShapedHost
		}
	}
	return ""
}

// optionShapedToken reports whether s, ignoring leading space, starts with
// "-".
func optionShapedToken(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "-")
}

// ValidateRemoteURL performs strict security validation on a remote URL.
// It rejects URLs containing control characters (including null bytes),
// validates structural correctness per scheme, and rejects URLs with a part
// a program would read as a CLI flag (OptionShapedPart).
//
// This is a security boundary — all remote URLs should pass through this
// before reaching exec.Command arguments or SQL parameters.
func ValidateRemoteURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("remote URL cannot be empty")
	}

	// Reject control characters (null bytes, newlines, tabs, etc.)
	for i, c := range rawURL {
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("remote URL contains control character at position %d (0x%02x)", i, c)
		}
	}

	// Reject CLI flag injection via exec.Command arguments: a leading dash,
	// or an ssh:// user name or host that starts with one.
	switch part := OptionShapedPart(rawURL); part {
	case "":
	case OptionShapedURL:
		return fmt.Errorf("remote URL must not start with a dash")
	default:
		return fmt.Errorf("the %s in a remote URL must not start with a dash", part)
	}

	// SCP-style URLs (user@host:path) are validated separately
	if gitSSHPattern.MatchString(rawURL) {
		return validateSCPURL(rawURL)
	}

	// Parse as standard URL
	return validateSchemeURL(rawURL)
}

// validateSchemeURL validates a scheme-based URL (https://, dolthub://, etc.)
func validateSchemeURL(rawURL string) error {
	// net/url doesn't understand git+ssh:// etc., so we normalize first
	normalizedURL := rawURL
	scheme := ""
	if idx := strings.Index(rawURL, "://"); idx > 0 {
		scheme = rawURL[:idx]
		// For net/url parsing, replace git+ssh with a parseable scheme
		if strings.HasPrefix(scheme, "git+") {
			normalizedURL = rawURL[len(scheme)+3:] // strip scheme://
			normalizedURL = "placeholder://" + normalizedURL
		}
	}

	if scheme == "" {
		return fmt.Errorf("remote URL has no scheme (expected one of: %s)", strings.Join(sortedSchemes(), ", "))
	}

	if !allowedSchemes[scheme] {
		return fmt.Errorf("remote URL scheme %q is not allowed (expected one of: %s)", scheme, strings.Join(sortedSchemes(), ", "))
	}

	parsed, err := url.Parse(normalizedURL)
	if err != nil {
		return fmt.Errorf("remote URL is malformed: %w", err)
	}

	// Scheme-specific structural validation
	switch scheme {
	case "dolthub":
		// dolthub://org/repo — requires org and repo
		p := strings.TrimPrefix(parsed.Path, "/")
		host := parsed.Host
		combined := host
		if p != "" {
			combined = host + "/" + p
		}
		parts := strings.Split(combined, "/")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("dolthub:// URL must have org/repo format (e.g., dolthub://myorg/myrepo)")
		}
	case "https", "http", "git+https", "git+http":
		if parsed.Host == "" {
			return fmt.Errorf("%s:// URL must include a hostname", scheme)
		}
	case "ssh", "git", "git+ssh":
		if parsed.Host == "" {
			return fmt.Errorf("%s:// URL must include a hostname", scheme)
		}
	case "s3", "aws", "gs":
		// s3://bucket/path, aws://bucket/path, gs://bucket/path — host is the bucket
		if parsed.Host == "" {
			return fmt.Errorf("%s:// URL must include a bucket name", scheme)
		}
	case "az":
		// az://account.blob.core.windows.net/container/path
		if parsed.Host == "" {
			return fmt.Errorf("az:// URL must include a storage account hostname")
		}
	case "oci":
		if parsed.Host == "" {
			return fmt.Errorf("oci:// URL must include a namespace or bucket host")
		}
	case "file":
		// file:// is allowed with any path
	case "git+file":
		// git+file:// is Dolt's normalized form for local git remotes.
	}

	return nil
}

// validateSCPURL validates an SCP-style URL (user@host:path)
func validateSCPURL(rawURL string) error {
	// Already matched gitSSHPattern, so structure is valid.
	// Extract host and verify no control chars (already checked above).
	atIdx := strings.Index(rawURL, "@")
	colonIdx := strings.Index(rawURL[atIdx:], ":")
	if atIdx < 0 || colonIdx < 0 {
		return fmt.Errorf("SCP-style URL must be in user@host:path format")
	}
	return nil
}

// ValidateRemoteName checks that a remote name is safe for use as a Dolt
// remote identifier. Names must start with a letter and contain only
// alphanumeric characters, hyphens, and underscores. Max 64 characters.
func ValidateRemoteName(name string) error {
	if name == "" {
		return fmt.Errorf("remote name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("remote name too long (max 64 characters)")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("remote name must not start with a dash")
	}
	if !validRemoteNameRegex.MatchString(name) {
		return fmt.Errorf("remote name must start with a letter and contain only alphanumeric characters, hyphens, and underscores")
	}
	return nil
}

// MatchesRemotePattern checks whether a URL matches a glob-style pattern.
// Patterns use path.Match semantics (e.g., "dolthub://myorg/*").
func MatchesRemotePattern(rawURL, pattern string) bool {
	matched, err := path.Match(pattern, rawURL)
	if err != nil {
		return false
	}
	return matched
}

// RemotePolicyError is the refusal every federation.allowed-remote-patterns
// gate returns: the allowlist is configured and the destination is outside
// it, or the destination could not be resolved far enough to check. It is a
// distinct type so callers can classify a policy refusal (never retried,
// never converted to a benign no-remote result) without matching message
// text.
//
// Subject names what was refused — a config key ("federation.remote",
// "sync.remote") or a remote/peer name ("remote \"origin\""). Cause is a
// short, credential-free explanation. Neither field carries the URL or the
// patterns: both can embed userinfo (https://user:token@host/...) and a
// refusal is echoed into errors, hints and JSON by every layer above.
type RemotePolicyError struct {
	Subject string
	Cause   string
}

func (e *RemotePolicyError) Error() string {
	return fmt.Sprintf("%s is refused by federation.allowed-remote-patterns: %s", e.Subject, e.Cause)
}

// ValidateRemoteWithPolicy enforces the federation.allowed-remote-patterns
// allowlist (patterns) against rawURL and returns nil when the destination is
// allowed. This is the one shared gate every caller uses — every runtime
// enforcement point and the config lint (GH#6233) — so lint and runtime can
// never disagree about a destination.
//
// patterns is the effective config value: absent or empty means the setting
// is off, and the call is a no-op — enforcement is opt-in and must not change
// any behavior of a workspace that never configured a list. Blank entries
// are kept: path.Match("", url) never matches, so a list of blank entries
// denies every remote rather than allowing all of them, and dropping them
// would turn a deny-all list into an allow-all one.
//
// The destination is evaluated in its Dolt-normalized form
// (doltremote.Normalize), the same transform registration applies through
// doltRemoteURL before the URL reaches Dolt. Typed and registered spellings
// of one destination therefore get one verdict: https://github.com/org/repo
// and git+https://github.com/org/repo both satisfy git+https://github.com/org/*
// and neither satisfies a different organization's pattern. Patterns
// themselves are matched literally (path.Match semantics): * does not cross
// /, and a malformed entry matches nothing rather than failing the check.
//
// Under a non-empty list the normalized destination must also pass the
// structural validation (ValidateRemoteURL), so no pattern — not even "*" —
// can admit a URL the structural rules refuse. The error is always a
// *RemotePolicyError and names subject without echoing the URL or patterns.
func ValidateRemoteWithPolicy(patterns []string, subject, rawURL string) error {
	if len(patterns) == 0 {
		return nil
	}
	dest := doltremote.Normalize(rawURL)
	if err := ValidateRemoteURL(dest); err != nil {
		// The structural detail is dropped, not wrapped: url.Parse errors
		// embed the raw URL, and the lint reports structural problems
		// separately with its own redacted wording.
		return &RemotePolicyError{Subject: subject, Cause: "its remote URL is not structurally valid"}
	}
	for _, p := range patterns {
		if MatchesRemotePattern(dest, p) {
			return nil
		}
	}
	return &RemotePolicyError{Subject: subject, Cause: "its remote URL matches no pattern in the list"}
}

func sortedSchemes() []string {
	// Return in a consistent display order
	return []string{"dolthub", "https", "http", "ssh", "git", "git+ssh", "git+https", "git+http", "git+file", "s3", "aws", "gs", "az", "oci", "file"}
}

// CacheKey returns a filesystem-safe identifier for a remote URL.
// It uses the first 16 hex characters (64 bits) of the SHA-256 hash.
// Birthday-bound collision risk is negligible for a local cache: 50% at
// ~4.3 billion entries, well beyond any realistic number of remotes.
func CacheKey(remoteURL string) string {
	h := sha256.Sum256([]byte(remoteURL))
	return fmt.Sprintf("%x", h[:8])
}
