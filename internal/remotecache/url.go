package remotecache

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// remoteSchemes lists URL scheme prefixes recognized as dolt remote URLs.
var remoteSchemes = []string{
	"dolthub://",
	"gs://",
	"s3://",
	"az://",
	"file://",
	"https://",
	"http://",
	"ssh://",
	"git+ssh://",
	"git+https://",
	"git+http://",
}

// allowedSchemes is the set of recognized URL schemes for validation.
var allowedSchemes = map[string]bool{
	"dolthub":   true,
	"gs":        true,
	"s3":        true,
	"az":        true,
	"file":      true,
	"https":     true,
	"http":      true,
	"ssh":       true,
	"git+ssh":   true,
	"git+https": true,
	"git+http":  true,
}

// gitSSHPattern matches SCP-style git remote URLs (user@host:path).
// The path portion excludes control characters (0x00-0x1f, 0x7f).
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

// ValidateRemoteURL performs strict security validation on a remote URL.
// It rejects URLs containing control characters (including null bytes),
// validates structural correctness per scheme, and rejects leading dashes
// that could be interpreted as CLI flags.
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

	// Reject leading dash (CLI flag injection via exec.Command arguments)
	if strings.HasPrefix(rawURL, "-") {
		return fmt.Errorf("remote URL must not start with a dash")
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
	case "ssh", "git+ssh":
		if parsed.Host == "" {
			return fmt.Errorf("%s:// URL must include a hostname", scheme)
		}
	case "s3", "gs":
		// s3://bucket/path, gs://bucket/path — host is the bucket
		if parsed.Host == "" {
			return fmt.Errorf("%s:// URL must include a bucket name", scheme)
		}
	case "az":
		// az://account.blob.core.windows.net/container/path
		if parsed.Host == "" {
			return fmt.Errorf("az:// URL must include a storage account hostname")
		}
	case "file":
		// file:// is allowed with any path
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

// ValidateRemoteURLWithPatterns validates a URL and optionally checks it
// against an allowlist of glob patterns. If patterns is empty, only
// structural validation is performed.
func ValidateRemoteURLWithPatterns(rawURL string, patterns []string) error {
	if err := ValidateRemoteURL(rawURL); err != nil {
		return err
	}
	if len(patterns) == 0 {
		return nil
	}
	for _, p := range patterns {
		if MatchesRemotePattern(rawURL, p) {
			return nil
		}
	}
	return fmt.Errorf("remote URL %q does not match any allowed pattern", rawURL)
}

func sortedSchemes() []string {
	// Return in a consistent display order
	return []string{"dolthub", "https", "http", "ssh", "git+ssh", "git+https", "git+http", "s3", "gs", "az", "file"}
}

// CacheKey returns a filesystem-safe identifier for a remote URL.
// It uses the first 16 hex characters (64 bits) of the SHA-256 hash.
// Birthday-bound collision risk is negligible for a local cache: 50% at
// ~4.3 billion entries, well beyond any realistic number of remotes.
func CacheKey(remoteURL string) string {
	h := sha256.Sum256([]byte(remoteURL))
	return fmt.Sprintf("%x", h[:8])
}
