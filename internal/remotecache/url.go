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
// It uses the first 16 hex characters (64 bits) of the SHA-256 hash.
// Birthday-bound collision risk is negligible for a local cache: 50% at
// ~4.3 billion entries, well beyond any realistic number of remotes.
func CacheKey(remoteURL string) string {
	h := sha256.Sum256([]byte(remoteURL))
	return fmt.Sprintf("%x", h[:8])
}
