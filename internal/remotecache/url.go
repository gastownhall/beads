package remotecache

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

// remoteSchemes lists URL scheme prefixes recognized as dolt remote URLs.
var remoteSchemes = []string{
	"dolthub://",
	"gs://",
	"s3://",
	"file://",
	"https://",
	"http://",
	"ssh://",
	"git+ssh://",
	"git+https://",
}

// gitSSHPattern matches SCP-style git remote URLs (user@host:path).
var gitSSHPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+@[a-zA-Z0-9][a-zA-Z0-9._-]*:.+$`)

// IsRemoteURL returns true if s looks like a dolt remote URL rather than
// a local filesystem path. Recognized schemes: dolthub://, https://, http://,
// s3://, gs://, file://, ssh://, git+ssh://, git+https://, and SCP-style
// git@host:path.
func IsRemoteURL(s string) bool {
	for _, scheme := range remoteSchemes {
		if strings.HasPrefix(s, scheme) {
			return true
		}
	}
	return gitSSHPattern.MatchString(s)
}

// CacheKey returns a filesystem-safe identifier for a remote URL.
// It uses the first 16 hex characters of the SHA-256 hash, providing
// sufficient uniqueness without excessive path length.
func CacheKey(remoteURL string) string {
	h := sha256.Sum256([]byte(remoteURL))
	return fmt.Sprintf("%x", h[:8])
}
